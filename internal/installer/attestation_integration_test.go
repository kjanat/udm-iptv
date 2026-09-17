//go:build integration

package installer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

func publishedAsset(t *testing.T) (upgradeCandidate, string) {
	t.Helper()
	candidate, err := resolveUpgrade(t.Context(), UpgradeOptions{Repository: "kjanat/udm-iptv"})
	if err != nil {
		t.Skipf("resolve latest release: %v", err)
	}
	candidate.stateDir = t.TempDir()
	name, assetURL := attestedReleaseAsset(candidate)
	if assetURL == "" {
		t.Skipf("release %s publishes no assets", candidate.release.GetTagName())
	}
	path := filepath.Join(t.TempDir(), name)
	if err := download(t.Context(), candidate.client, assetURL, path, filemode.Executable); err != nil {
		t.Skipf("download %s: %v", name, err)
	}

	return candidate, path
}

func attestedReleaseAsset(candidate upgradeCandidate) (string, string) {
	binaryName := "udm-iptv-linux-" + runtime.GOARCH
	name, assetURL := "", ""
	for _, asset := range candidate.release.Assets {
		if asset.GetName() == binaryName {
			return asset.GetName(), asset.GetURL()
		}
		if assetURL == "" && asset.GetName() != "SHA256SUMS" {
			name, assetURL = asset.GetName(), asset.GetURL()
		}
	}

	return name, assetURL
}

func TestVerifyAttestationAcceptsPublishedRelease(t *testing.T) {
	candidate, asset := publishedAsset(t)
	if err := verifyAttestation(t.Context(), candidate, asset); err != nil {
		t.Fatalf("published %s must verify: %v", filepath.Base(asset), err)
	}
	cache := filepath.Join(candidate.stateDir, "sigstore", "tuf")
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) == 0 {
		t.Fatalf("trusted root cache missing from %s: %v", cache, err)
	}
}

func TestVerifyAttestationRejectsTamperedRelease(t *testing.T) {
	candidate, asset := publishedAsset(t)
	original, err := os.ReadFile(asset)
	if err != nil {
		t.Fatal(err)
	}
	tampered := filepath.Join(t.TempDir(), filepath.Base(asset))
	if err := atomicfile.Write(tampered, append(original, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyAttestation(t.Context(), candidate, tampered); err == nil {
		t.Fatal("a modified release asset must not verify")
	}
}

func TestVerifyAttestationRejectsForeignRepository(t *testing.T) {
	candidate, asset := publishedAsset(t)
	digest, err := fileDigest(filepath.Split(asset))
	if err != nil {
		t.Fatal(err)
	}
	bundles, err := fetchAttestations(t.Context(), candidate, digest)
	if err != nil {
		t.Skipf("fetch attestations for sha256:%s: %v", digest, err)
	}
	if len(bundles) == 0 {
		t.Fatalf("published %s has no attestation to test the identity policy against", filepath.Base(asset))
	}
	foreign := candidate
	foreign.owner, foreign.repository = "sigstore", "sigstore"
	verifier, policy, err := attestationVerifier(t.Context(), foreign, digest)
	if err != nil {
		t.Fatal(err)
	}
	for _, attestation := range bundles {
		if _, err := verifier.Verify(attestation, policy); err == nil {
			t.Fatalf("identity policy accepted a kjanat/udm-iptv attestation under sigstore/sigstore: %s", filepath.Base(asset))
		}
	}
}
