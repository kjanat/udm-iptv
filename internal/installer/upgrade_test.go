package installer

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-github/v80/github"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestVerifyChecksum(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	binary := filepath.Join(directory, "udm-iptv-linux-arm64")
	checksums := filepath.Join(directory, "SHA256SUMS")
	err := atomicfile.Write(binary, []byte("binary"), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	err = atomicfile.Write(checksums, []byte("9a3a45d01531a20e89ac6ae10b0b0beb0492acd7216a368aa062d1a5fecaf9cd  udm-iptv-linux-arm64\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyChecksum(binary, checksums, "udm-iptv-linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
}

func TestReleaseAssetsUseAuthenticatedAPIURLs(t *testing.T) {
	t.Parallel()
	release := &github.RepositoryRelease{Assets: []*github.ReleaseAsset{
		{Name: new("udm-iptv-linux-arm64"), URL: new("https://api.github.com/repos/kjanat/udm-iptv/releases/assets/1"), BrowserDownloadURL: new("https://github.com/kjanat/udm-iptv/releases/download/v1/udm-iptv-linux-arm64")},
		{Name: new("SHA256SUMS"), URL: new("https://api.github.com/repos/kjanat/udm-iptv/releases/assets/2"), BrowserDownloadURL: new("https://github.com/kjanat/udm-iptv/releases/download/v1/SHA256SUMS")},
	}}
	binary, checksums := releaseAssetURLs(release, "udm-iptv-linux-arm64")
	if binary != release.Assets[0].GetURL() || checksums != release.Assets[1].GetURL() {
		t.Fatalf("release asset URLs = %q, %q", binary, checksums)
	}
}

func TestReleaseAssetDownloadScopesAuthentication(t *testing.T) {
	t.Parallel()
	var requests []*http.Request
	client := &http.Client{Transport: &bearerTransport{
		token: "test-token",
		base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests = append(requests, request)

			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("asset")), Header: make(http.Header), Request: request}, nil
		}),
	}}
	target := filepath.Join(t.TempDir(), "asset")
	if err := download(context.Background(), client, "https://api.github.com/repos/kjanat/udm-iptv/releases/assets/1", target, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := requests[0].Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("API authorization header = %q", got)
	}
	if got := requests[0].Header.Get("Accept"); got != "application/octet-stream" {
		t.Fatalf("API accept header = %q", got)
	}
	unrelated, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/asset", nil)
	response, err := client.Transport.RoundTrip(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	defer closeIgnoringError(response.Body)
	if got := requests[1].Header.Get("Authorization"); got != "" {
		t.Fatalf("token leaked to unrelated host: %q", got)
	}
}

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type roundTripFunc func(*http.Request) (*http.Response, error)
