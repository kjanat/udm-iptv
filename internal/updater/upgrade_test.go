package updater

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
	"github.com/kjanat/udm-iptv/internal/installer"
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

// An executable swapped in behind dpkg's back leaves the package record
// behind; the next upgrade must reinstall the package even when the
// executable already is the candidate, and say why.
func TestPlanUpgradeRepairsAPackageRecordBehindTheExecutable(t *testing.T) {
	t.Parallel()
	installed := installer.PackageRecord{Status: "installed", Version: "5.0.0~preview.2"}
	stale := installer.PackageRecord{Status: "installed", Version: "5.0.0~preview.1"}
	for name, test := range map[string]struct {
		running, candidate string
		record             installer.PackageRecord
		force              bool
		want               upgradePlan
	}{
		"standalone up to date":     {"5.0.0", "5.0.0", installer.PackageRecord{}, false, upgradePlan{note: "udm-iptv 5.0.0 is already installed. Use --force to reinstall."}},
		"standalone forced":         {"5.0.0", "5.0.0", installer.PackageRecord{}, true, upgradePlan{proceed: true}},
		"standalone newer":          {"5.0.0", "5.0.1", installer.PackageRecord{}, false, upgradePlan{proceed: true}},
		"package up to date":        {"5.0.0-preview.2", "5.0.0-preview.2", installed, false, upgradePlan{note: "udm-iptv 5.0.0-preview.2 is already installed. Use --force to reinstall."}},
		"package forced":            {"5.0.0-preview.2", "5.0.0-preview.2", installed, true, upgradePlan{proceed: true, viaPackage: true}},
		"package newer":             {"5.0.0-preview.1", "5.0.0-preview.2", installed, false, upgradePlan{proceed: true, viaPackage: true, note: "dpkg recorded udm-iptv 5.0.0-preview.2 while 5.0.0-preview.1 is running; the package is reinstalled to bring the two in line."}},
		"record behind the binary":  {"5.0.0-preview.2", "5.0.0-preview.2", stale, false, upgradePlan{proceed: true, viaPackage: true, note: "dpkg recorded udm-iptv 5.0.0-preview.1 while 5.0.0-preview.2 is running; the package is reinstalled to bring the two in line."}},
		"half configured, in line":  {"5.0.0-preview.2", "5.0.0-preview.2", installer.PackageRecord{Status: "half-configured", Version: "5.0.0~preview.2"}, false, upgradePlan{note: "udm-iptv 5.0.0-preview.2 is already installed. Use --force to reinstall."}},
		"config-files is not owned": {"5.0.0", "5.0.0", installer.PackageRecord{Status: "config-files", Version: "4.3.1"}, false, upgradePlan{note: "udm-iptv 5.0.0 is already installed. Use --force to reinstall."}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := planUpgrade(test.running, test.candidate, test.record, test.force)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("plan = %+v, want %+v", got, test.want)
			}
		})
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
		Version: "5.0.1", StateDir: "/data/udm-iptv", Out: &out, Err: &errOut,
		installed: func(string) bool { return true },
		Restart: func(context.Context, bool) error {
			t.Fatal("a package upgrade restarted the service itself")

			return nil
		},
		packages: packageCommands{
			record: func(context.Context) (installer.PackageRecord, error) {
				return installer.PackageRecord{Status: "installed", Version: "5.0.1"}, nil
			},
			install: func(_ context.Context, packagePath string, allowDowngrade bool, _, _ io.Writer) error {
				if !allowDowngrade {
					t.Fatal("forced downgrade did not reach apt")
				}
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
	upgrader.resolve = func(context.Context, UpgradeOptions, releaseChannel) (upgradeCandidate, error) {
		return upgradeCandidate{release: release, version: "5.0.0"}, nil
	}
	if err := upgrader.Upgrade(t.Context(), UpgradeOptions{}); !errors.Is(err, errDowngrade) || fetched != "" || installed != "" {
		t.Fatalf("unforced downgrade: err=%v fetched=%q installed=%q", err, fetched, installed)
	}
	if err := upgrader.Upgrade(t.Context(), UpgradeOptions{Force: true}); err != nil {
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
	upgrader := &Upgrader{StateDir: "/data/udm-iptv", Out: io.Discard, Err: io.Discard, packages: packageCommands{record: func(context.Context) (installer.PackageRecord, error) {
		return installer.PackageRecord{Status: "installed", Version: "5.0.0"}, nil
	}}}
	release := &github.RepositoryRelease{TagName: new("v5.0.0"), Assets: []*github.ReleaseAsset{{Name: new("udm-iptv-linux-arm64"), URL: new("https://api.github.com/1")}}}
	err := upgrader.applyPackageRelease(context.Background(), upgradeCandidate{release: release, version: "5.0.0"}, false)
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

func TestChannelFollowsTheRunningBuild(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		running    string
		prerelease bool
		want       releaseChannel
	}{
		"a preview build follows prereleases":    {"5.0.0-preview.5", false, prereleaseChannel},
		"a stable build follows stable releases": {"4.3.1", false, stableChannel},
		"--prerelease overrides a stable build":  {"4.3.1", true, prereleaseChannel},
		"a dev build follows stable releases":    {"dev", false, stableChannel},
		"--prerelease overrides a dev build":     {"dev", true, prereleaseChannel},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := channelFor(test.running, test.prerelease); got != test.want {
				t.Fatalf("channel = %s, want %s", got, test.want)
			}
		})
	}
}

func TestFetchLatestReleaseFollowsTheChannel(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		channel releaseChannel
		path    string
		body    string
		tag     string
	}{
		"stable asks GitHub for its latest release":      {stableChannel, "/repos/kjanat/udm-iptv/releases/latest", `{"tag_name":"v4.3.1"}`, "v4.3.1"},
		"prerelease lists releases and takes the newest": {prereleaseChannel, "/repos/kjanat/udm-iptv/releases", `[{"tag_name":"v5.0.0-preview.5","prerelease":true},{"tag_name":"v4.3.1"}]`, "v5.0.0-preview.5"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var paths []string
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				paths = append(paths, request.URL.Path)

				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header), Request: request}, nil
			})
			release, err := fetchRelease(context.Background(), github.NewClient(&http.Client{Transport: transport}), "kjanat", "udm-iptv", "latest", test.channel)
			if err != nil {
				t.Fatal(err)
			}
			if len(paths) != 1 || paths[0] != test.path {
				t.Fatalf("requested %v, want %s", paths, test.path)
			}
			if release.GetTagName() != test.tag {
				t.Fatalf("resolved %q, want %s", release.GetTagName(), test.tag)
			}
		})
	}
}

func TestPlanUpgradeRefusesADowngrade(t *testing.T) {
	t.Parallel()
	installed := installer.PackageRecord{Status: "installed", Version: "5.0.0~preview.5"}
	for name, test := range map[string]struct {
		running, candidate string
		record             installer.PackageRecord
		force              bool
		refused            bool
	}{
		"the stable latest behind a preview":   {"5.0.0-preview.5", "4.3.1", installed, false, true},
		"an older preview":                     {"5.0.0-preview.5", "5.0.0-preview.4", installed, false, true},
		"an older standalone release":          {"5.0.1", "5.0.0", installer.PackageRecord{}, false, true},
		"forced":                               {"5.0.0-preview.5", "4.3.1", installed, true, false},
		"the release a preview leads to":       {"5.0.0-preview.5", "5.0.0", installed, false, false},
		"a newer preview":                      {"5.0.0-preview.5", "5.0.0-preview.6", installed, false, false},
		"a dev build has no order":             {"dev", "4.3.1", installer.PackageRecord{}, false, false},
		"a tag without a version has no order": {"5.0.0", "nightly", installer.PackageRecord{}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			plan, err := planUpgrade(test.running, test.candidate, test.record, test.force)
			if !test.refused {
				if err != nil {
					t.Fatal(err)
				}
				if !plan.proceed {
					t.Fatalf("plan = %+v", plan)
				}

				return
			}
			if !errors.Is(err, errDowngrade) {
				t.Fatalf("err = %v", err)
			}
			for _, version := range []string{test.running, test.candidate} {
				if !strings.Contains(err.Error(), version) {
					t.Fatalf("%v does not name %s", err, version)
				}
			}
			if plan.proceed {
				t.Fatal("a refused plan proceeds")
			}
		})
	}
}

func TestDryRunDescribesThePlanWithoutInstalling(t *testing.T) {
	t.Parallel()
	release := &github.RepositoryRelease{TagName: new("v5.0.0-preview.6"), Assets: []*github.ReleaseAsset{
		{Name: new(packageAssetName()), URL: new("https://api.github.com/repos/kjanat/udm-iptv/releases/assets/1")},
		{Name: new(standaloneAssetName()), URL: new("https://api.github.com/repos/kjanat/udm-iptv/releases/assets/2")},
		{Name: new("SHA256SUMS"), URL: new("https://api.github.com/repos/kjanat/udm-iptv/releases/assets/3")},
	}}
	candidate := upgradeCandidate{release: release, version: "5.0.0-preview.6"}
	for name, test := range map[string]struct {
		running string
		record  installer.PackageRecord
		want    string
	}{
		"package":    {"5.0.0-preview.5", installer.PackageRecord{Status: "installed", Version: "5.0.0~preview.5"}, "Would download " + packageAssetName() + " and install it with apt-get.\n"},
		"standalone": {"5.0.0-preview.5", installer.PackageRecord{}, "Would download " + standaloneAssetName() + ", replace /data/udm-iptv/bin/udm-iptv and restart udm-iptv.service.\n"},
		"up to date": {"5.0.0-preview.6", installer.PackageRecord{}, "udm-iptv 5.0.0-preview.6 is already installed. Use --force to reinstall.\n"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			upgrader := &Upgrader{
				Version: test.running, StateDir: "/data/udm-iptv", Out: &out, Err: io.Discard,
				installed: func(path string) bool { return path == "/data/udm-iptv" },
				resolve: func(_ context.Context, options UpgradeOptions, channel releaseChannel) (upgradeCandidate, error) {
					if !options.DryRun || channel != prereleaseChannel {
						t.Fatal("wrong resolution options")
					}
					return candidate, nil
				},
				Restart: func(context.Context, bool) error { t.Fatal("dry run restarted service"); return nil },
				packages: packageCommands{
					record: func(context.Context) (installer.PackageRecord, error) { return test.record, nil },
					install: func(context.Context, string, bool, io.Writer, io.Writer) error {
						t.Fatal("a dry run installed the package")

						return nil
					},
				},
				fetch: func(context.Context, upgradeCandidate, string, string, string, string, os.FileMode) (string, error) {
					t.Fatal("a dry run downloaded an asset")

					return "", nil
				},
			}
			if err := upgrader.Upgrade(t.Context(), UpgradeOptions{Repository: "kjanat/udm-iptv", DryRun: true}); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != "Dry run: nothing is downloaded or installed.\nRunning udm-iptv "+test.running+" on the prerelease channel.\nRelease found: udm-iptv 5.0.0-preview.6.\n"+test.want {
				t.Fatalf("output = %q", got)
			}
		})
	}
	missing := upgradeCandidate{release: &github.RepositoryRelease{TagName: new("v4.3.1")}, version: "4.3.1"}
	err := (&Upgrader{StateDir: "/data/udm-iptv", Out: io.Discard, Err: io.Discard}).describe(missing, upgradePlan{proceed: true, viaPackage: true})
	if !errors.Is(err, errReleaseAssetsMissing) {
		t.Fatalf("release without the package asset: %v", err)
	}
}
