package firmware

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const testImage = "ghcr.io/example/unifi-os"

func pairFor(model string) []Release {
	board, _ := boardFor(model)

	return []Release{
		{board, "5.1.9", "https://fw-download.ubnt.com/old.bin", strings.Repeat("a", 64)},
		{board, "5.1.10", "https://fw-download.ubnt.com/new.bin", strings.Repeat("b", 64)},
	}
}

func catalogFixture(t *testing.T, cutoff time.Time) (string, string) {
	t.Helper()
	versions := []string{"5.1.8", "5.1.9", "5.1.9", "5.1.10", "6.0.0-beta", "latest"}
	entries := make([]catalogRelease, 0, len(versions)*len(models))
	tags := make([]string, 0, len(versions)*len(models))
	for _, model := range models {
		for _, version := range versions {
			entry := catalogRelease{Platform: model.Board, Version: "v" + version + "+1", Created: cutoff, SHA256: strings.Repeat("a", 64)}
			entry.Links.Data.Href = "https://fw-download.ubnt.com/firmware.bin"
			entries = append(entries, entry)
			tags = append(tags, model.Name+"-"+version)
		}
	}
	encoded, err := json.Marshal(map[string]any{"_embedded": map[string]any{"firmware": entries}})
	if err != nil {
		t.Fatal(err)
	}

	return string(encoded), strings.Join(append(tags, "UDMPRO-9.0.0", "udmpro-UDMPRO-9.0.0"), "\n")
}

func assertLatestPairs(t *testing.T, result Matrix) {
	t.Helper()
	if len(result.Include) != len(models) {
		t.Fatalf("missing model coverage: %+v", result)
	}
	for i, pair := range result.Include {
		if pair.Model != models[i].Name || pair.From != testImage+":"+pair.Model+"-5.1.9" || pair.To != testImage+":"+pair.Model+"-5.1.10" {
			t.Fatalf("wrong pair: %+v", pair)
		}
	}
}

func TestCatalogAndPublishedPairs(t *testing.T) {
	cutoff := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	encoded, tags := catalogFixture(t, cutoff)
	t.Run("catalog", func(t *testing.T) {
		matrix, err := SelectCatalog(strings.NewReader(encoded), testImage, "all", cutoff)
		if err != nil {
			t.Fatal(err)
		}
		assertLatestPairs(t, matrix)
	})
	t.Run("published", func(t *testing.T) {
		published, err := Published(tags, testImage)
		if err != nil {
			t.Fatal(err)
		}
		assertLatestPairs(t, published)
	})
	for _, test := range []struct {
		name   string
		reason string
		pairs  func() (Matrix, error)
	}{
		{"future releases", "future releases accepted", func() (Matrix, error) {
			return SelectCatalog(strings.NewReader(encoded), testImage, "all", cutoff.Add(-time.Second))
		}},
		{"unknown model", "unknown model accepted", func() (Matrix, error) {
			return SelectCatalog(strings.NewReader(encoded), testImage, "unknown", cutoff)
		}},
		{"incomplete tags", "incomplete matrix accepted", func() (Matrix, error) {
			return Published("udm-5.1.9", testImage)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.pairs(); err == nil {
				t.Fatal(test.reason)
			}
		})
	}
}

func TestPairValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]Release) []Release
	}{
		{"missing", func(p []Release) []Release { return p[:1] }},
		{"duplicate", func(p []Release) []Release { return []Release{p[0], p[0]} }},
		{"reverse", func(p []Release) []Release { return []Release{p[1], p[0]} }},
		{"board", func(p []Release) []Release {
			p[0].Board = "UDM"
			return p
		}},
		{"url", func(p []Release) []Release {
			p[0].URL = "https://example.com/f.bin"
			return p
		}},
		{"checksum", func(p []Release) []Release {
			p[0].SHA256 = "no"
			return p
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePair("udmpro", test.mutate(pairFor("udmpro")))
			if err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
}
