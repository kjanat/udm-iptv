package updater

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-github/v80/github"

	"github.com/kjanat/udm-iptv/internal/installer"
)

var errInterruptedDownload = errors.New("download interrupted")

func lockCandidate() upgradeCandidate {
	return upgradeCandidate{version: "5.0.1", release: &github.RepositoryRelease{
		TagName: new("v5.0.1"), Assets: []*github.ReleaseAsset{
			{Name: new(standaloneAssetName()), URL: new("https://example.com/binary")},
			{Name: new(packageAssetName()), URL: new("https://example.com/package")},
			{Name: new("SHA256SUMS"), URL: new("https://example.com/checksums")},
		},
	}}
}

func TestStandaloneUpdateHoldsLockDuringDownload(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	fetched := false
	application := Upgrader{StateDir: stateDir, Out: io.Discard, fetch: func(context.Context, upgradeCandidate, string, string, string, string, os.FileMode) (string, error) {
		fetched = true
		release, err := installer.AcquireLock(stateDir)
		if err == nil {
			if err := release(); err != nil {
				t.Fatal(err)
			}
			t.Fatal("standalone update left installation unlocked during download")
		}
		if !strings.Contains(err.Error(), "in progress") {
			t.Fatalf("lock failed for another reason: %v", err)
		}
		return "", errInterruptedDownload
	}}
	if err := application.applyStandaloneRelease(t.Context(), lockCandidate()); !errors.Is(err, errInterruptedDownload) || !fetched {
		t.Fatalf("download outcome: %v, fetched=%v", err, fetched)
	}
	release, err := installer.AcquireLock(stateDir)
	if err != nil {
		t.Fatalf("failed download retained operation lock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestPackageUpdateLeavesLockToMaintainerScripts(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	installed := false
	application := Upgrader{
		StateDir: stateDir, Out: io.Discard, Err: io.Discard,
		fetch: func(_ context.Context, _ upgradeCandidate, directory, name, _, _ string, _ os.FileMode) (string, error) {
			return filepath.Join(directory, name), nil
		},
		packages: packageCommands{
			record: func(context.Context) (installer.PackageRecord, error) { return installer.PackageRecord{}, nil },
			install: func(context.Context, string, bool, io.Writer, io.Writer) error {
				installed = true
				release, err := installer.AcquireLock(stateDir)
				if err != nil {
					t.Fatalf("package maintainer script cannot acquire its lock: %v", err)
				}
				return release()
			},
		},
	}
	if err := application.applyPackageRelease(t.Context(), lockCandidate(), false); err != nil || !installed {
		t.Fatalf("package update: %v, installed=%v", err, installed)
	}
}
