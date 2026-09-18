package installer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-github/v80/github"
	"github.com/klauspost/compress/snappy"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

func TestNewestPublishedReleaseSkipsDrafts(t *testing.T) {
	t.Parallel()
	pre := &github.RepositoryRelease{TagName: new("v5.0.0-preview.1"), Prerelease: new(true), Draft: new(false)}
	stable := &github.RepositoryRelease{TagName: new("v4.3.0"), Prerelease: new(false), Draft: new(false)}
	draft := &github.RepositoryRelease{TagName: new("v9.0.0"), Draft: new(true)}
	got := newestPublishedRelease([]*github.RepositoryRelease{draft, pre, stable})
	if got == nil || got.GetTagName() != "v5.0.0-preview.1" {
		t.Fatalf("got %#v", got)
	}
	if newestPublishedRelease([]*github.RepositoryRelease{draft, nil}) != nil {
		t.Fatal("drafts only")
	}
}

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

func TestFileDigestMatchesSHA256(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "asset")
	if err := atomicfile.Write(path, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := fileDigest(filepath.Split(path))
	if err != nil {
		t.Fatal(err)
	}
	if digest != "9a3a45d01531a20e89ac6ae10b0b0beb0492acd7216a368aa062d1a5fecaf9cd" {
		t.Fatalf("file digest = %q", digest)
	}
}

func TestWorkflowIdentityBindsToReleaseTagWorkflow(t *testing.T) {
	t.Parallel()
	release := &github.RepositoryRelease{TagName: new("v4.3.0")}
	identity, err := workflowIdentity(upgradeCandidate{owner: "kjanat", repository: "udm-iptv", release: release})
	if err != nil {
		t.Fatal(err)
	}
	signer := func(san, issuer, source string) certificate.Summary {
		extensions := certificate.Extensions{Issuer: issuer, SourceRepositoryURI: source}

		return certificate.Summary{SubjectAlternativeName: san, Extensions: extensions}
	}
	const released = "https://github.com/kjanat/udm-iptv/.github/workflows/release.yml@refs/tags/v4.3.0"
	if err := identity.Verify(signer(released, attestationIssuer, "https://github.com/kjanat/udm-iptv")); err != nil {
		t.Fatalf("the release workflow identity must verify: %v", err)
	}
	rejected := map[string]certificate.Summary{
		"another repository's workflow": signer(
			"https://github.com/attacker/udm-iptv/.github/workflows/release.yml@refs/tags/v4.3.0",
			attestationIssuer, "https://github.com/attacker/udm-iptv"),
		"a branch instead of a tag": signer(
			"https://github.com/kjanat/udm-iptv/.github/workflows/release.yml@refs/heads/master",
			attestationIssuer, "https://github.com/kjanat/udm-iptv"),
		"another release's tag": signer(
			"https://github.com/kjanat/udm-iptv/.github/workflows/release.yml@refs/tags/v4.2.0",
			attestationIssuer, "https://github.com/kjanat/udm-iptv"),
		"a tag that extends the candidate's": signer(
			"https://github.com/kjanat/udm-iptv/.github/workflows/release.yml@refs/tags/v4.3.0-rc1",
			attestationIssuer, "https://github.com/kjanat/udm-iptv"),
		"another workflow in the repository": signer(
			"https://github.com/kjanat/udm-iptv/.github/workflows/ci.yml@refs/tags/v4.3.0",
			attestationIssuer, "https://github.com/kjanat/udm-iptv"),
		"a non-GitHub issuer":            signer(released, "https://accounts.google.com", "https://github.com/kjanat/udm-iptv"),
		"a mismatched source repository": signer(released, attestationIssuer, "https://github.com/attacker/udm-iptv"),
	}
	for name, summary := range rejected {
		if err := identity.Verify(summary); err == nil {
			t.Fatalf("identity must reject %s", name)
		}
	}
}

const parseableBundle = `{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json",` +
	`"verificationMaterial":{"certificate":{"rawBytes":"AA=="},"tlogEntries":[]},` +
	`"dsseEnvelope":{"payload":"e30=","payloadType":"application/vnd.in-toto+json","signatures":[{"sig":"AA=="}]}}`

const unusableBundle = `{"mediaType":"nonsense"}`

func decodedRefs(t *testing.T, body string) []attestationRef {
	t.Helper()
	refs, err := decodeAttestations(strings.NewReader(body), "aabb")
	if err != nil {
		t.Fatal(err)
	}

	return refs
}

// usableBundles counts the refs that yield a bundle the verifier can use.
func usableBundles(t *testing.T, body string) int {
	t.Helper()
	usable := 0
	for _, ref := range decodedRefs(t, body) {
		if _, ok := parseBundle(ref.inline); ok {
			usable++
		}
	}

	return usable
}

func TestDecodeAttestationsSkipsUnusableBundles(t *testing.T) {
	t.Parallel()
	if got := len(decodedRefs(t, `{"attestations":[]}`)); got != 0 {
		t.Fatalf("empty response decoded %d attestations", got)
	}
	if got := usableBundles(t, `{"attestations":[{"bundle":`+unusableBundle+`}]}`); got != 0 {
		t.Fatalf("a bundle with an unsupported media type yielded %d usable bundles", got)
	}
	if got := usableBundles(t, `{"attestations":[{"bundle":`+parseableBundle+`}]}`); got != 1 {
		t.Fatalf("a well formed bundle yielded %d usable bundles", got)
	}
	// An unusable attestation published beside a usable one must not hide it.
	both := `{"attestations":[{"bundle":` + unusableBundle + `},{"bundle":` + parseableBundle + `}]}`
	if got := usableBundles(t, both); got != 1 {
		t.Fatalf("an unusable sibling hid the usable bundle: %d usable", got)
	}
	if _, err := decodeAttestations(strings.NewReader(`not json`), "aabb"); err == nil {
		t.Fatal("malformed attestation response must fail")
	}
}

// TestDecodeAttestationsKeepsBundleURL pins the shape API version 2026-03-10
// returns, where the bundle is addressed rather than inlined.
func TestDecodeAttestationsKeepsBundleURL(t *testing.T) {
	t.Parallel()
	refs := decodedRefs(t, `{"attestations":[{"bundle_url":"https://example.com/b.json.sn","initiator":"x"}]}`)
	if len(refs) != 1 {
		t.Fatalf("attestations = %d", len(refs))
	}
	if refs[0].source != "https://example.com/b.json.sn" {
		t.Fatalf("bundle_url = %q", refs[0].source)
	}
	if len(refs[0].inline) != 0 {
		t.Fatal("no bundle is inlined by the current API version")
	}
}

// TestParseBundleAcceptsSnappyBlock covers what blob storage actually serves.
func TestParseBundleAcceptsSnappyBlock(t *testing.T) {
	t.Parallel()
	if _, ok := parseBundle([]byte(parseableBundle)); !ok {
		t.Fatal("plain JSON bundle must parse")
	}
	compressed := snappy.Encode(nil, []byte(parseableBundle))
	if bytes.Equal(compressed, []byte(parseableBundle)) {
		t.Fatal("fixture did not compress, so this asserts nothing")
	}
	if _, ok := parseBundle(compressed); !ok {
		t.Fatal("Snappy block encoded bundle must parse")
	}
	if _, ok := parseBundle([]byte("neither json nor snappy")); ok {
		t.Fatal("unreadable body must not yield a bundle")
	}
}

func TestVerifyAttestationRejectsUnattestedAsset(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "asset")
	if err := atomicfile.Write(path, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := upgradeCandidate{
		owner:      "kjanat",
		repository: "udm-iptv",
		client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`{"attestations":[]}`)),
				Header:     make(http.Header),
				Request:    request,
			}, nil
		})},
	}
	err := verifyAttestation(context.Background(), candidate, path)
	if err == nil {
		t.Fatal("an asset with no attestation must not install")
	}
	if !strings.Contains(err.Error(), "9a3a45d01531a20e89ac6ae10b0b0beb0492acd7216a368aa062d1a5fecaf9cd") {
		t.Fatalf("error must name the rejected digest: %v", err)
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
	assertTokenWithheld := func(endpoint string) {
		t.Helper()
		unrelated, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
		response, err := client.Transport.RoundTrip(unrelated)
		if err != nil {
			t.Fatal(err)
		}
		defer closeIgnoringError(response.Body)
		if got := requests[len(requests)-1].Header.Get("Authorization"); got != "" {
			t.Fatalf("token leaked to %s: %q", endpoint, got)
		}
	}
	assertTokenWithheld("https://example.com/asset")
	assertTokenWithheld("https://tmaproduction.blob.core.windows.net/attestations/1/bundle.json.sn")
	assertTokenWithheld("http://api.github.com/repos/kjanat/udm-iptv/releases/assets/1")
}

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func TestDecompressBundleRejectsInflatedBlock(t *testing.T) {
	t.Parallel()
	inflated := binary.AppendUvarint(nil, uint64(attestationDecodedLimit)+1)
	inflated = append(inflated, 0x00, 0x01, 0x02)
	size, err := snappy.DecodedLen(inflated)
	if err != nil || size <= attestationDecodedLimit {
		t.Fatalf("fixture declares %d bytes (err %v), so this asserts nothing", size, err)
	}
	if got := decompressBundle(inflated); !bytes.Equal(got, inflated) {
		t.Fatalf("oversized block was decoded into %d bytes", len(got))
	}
	if _, ok := parseBundle(inflated); ok {
		t.Fatal("oversized block yielded a bundle")
	}
}

// A dpkg-tracked installation is upgraded through its package, so dpkg's
// database and the shipped files advance with the executable.
func TestPackageManagedUpgradeInstallsThePackage(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	installed := ""
	fetched := ""
	upgrader := &Upgrader{
		StateDir: "/data/udm-iptv", Out: &out, Err: &errOut,
		Restart: func(context.Context, bool) error {
			t.Fatal("a package upgrade restarted the service itself")

			return nil
		},
		packages: packageCommands{
			installed: func(context.Context) (bool, error) { return true, nil },
			install: func(_ context.Context, packagePath string, _, _ io.Writer) error {
				installed = packagePath

				return nil
			},
		},
		fetch: func(_ context.Context, _ upgradeCandidate, directory, assetName, _, _ string, mode os.FileMode) (string, error) {
			fetched = assetName
			if mode != filemode.SharedFile {
				t.Fatalf("package downloaded with mode %o", mode)
			}

			return filepath.Join(directory, assetName), nil
		},
	}
	release := &github.RepositoryRelease{TagName: new("v5.0.0"), Assets: []*github.ReleaseAsset{
		{Name: new(packageAssetName()), URL: new("https://api.github.com/repos/kjanat/udm-iptv/releases/assets/1")},
		{Name: new("SHA256SUMS"), URL: new("https://api.github.com/repos/kjanat/udm-iptv/releases/assets/2")},
	}}
	if err := upgrader.applyPackageRelease(context.Background(), upgradeCandidate{release: release, version: "5.0.0"}); err != nil {
		t.Fatal(err)
	}
	if fetched != packageAssetName() || filepath.Base(installed) != packageAssetName() {
		t.Fatalf("fetched %q, installed %q", fetched, installed)
	}
	if !strings.Contains(out.String(), "apt-get") {
		t.Fatalf("output does not say how the package is installed: %q", out.String())
	}
}

func TestPackageManagedUpgradeNeedsThePackageAsset(t *testing.T) {
	t.Parallel()
	upgrader := &Upgrader{StateDir: "/data/udm-iptv", Out: io.Discard, Err: io.Discard, packages: packageCommands{installed: func(context.Context) (bool, error) { return true, nil }}}
	release := &github.RepositoryRelease{TagName: new("v5.0.0"), Assets: []*github.ReleaseAsset{{Name: new("udm-iptv-linux-arm64"), URL: new("https://api.github.com/1")}}}
	err := upgrader.applyPackageRelease(context.Background(), upgradeCandidate{release: release, version: "5.0.0"})
	if !errors.Is(err, errReleaseAssetsMissing) {
		t.Fatalf("release without a package: %v", err)
	}
}

func TestDownloadErrorsDropTheSignedQuery(t *testing.T) {
	t.Parallel()
	signed := "https://objects.githubusercontent.com/github-production-release-asset/1/udm-iptv?X-Amz-Signature=deadbeef&X-Amz-Expires=300#frag"
	if got := displayURL(signed); got != "https://objects.githubusercontent.com/github-production-release-asset/1/udm-iptv" {
		t.Fatal(got)
	}
	if got := displayURL("::bad"); got != "::bad" {
		t.Fatal(got)
	}
}
