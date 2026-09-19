package firmware

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryTracksFilterChannelsAndPrereleases(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	items := []struct{ version, channel string }{
		{"5.0.0", channelRelease},
		{"5.1.0", channelRelease},
		{"6.0.0-rc.1", channelBeta},
		{"6.0.0-rc.2", channelBeta},
		{"7.0.0", "beta-private"},
		{"8.0.0", ""},
	}
	entries := make([]catalogRelease, 0, len(items))
	for _, item := range items {
		entry := catalogRelease{Platform: "UDMPRO", Version: "v" + item.version + "+123", Channel: item.channel, Created: cutoff, SHA256: strings.Repeat("a", 64)}
		entry.Links.Data.Href = "https://fw-download.ubnt.com/firmware.bin"
		entries = append(entries, entry)
	}
	body, err := json.Marshal(map[string]any{"_embedded": map[string]any{"firmware": entries}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
		if !slices.Contains(request.URL.Query()["filter"], "eq~~product~~unifi-dream") {
			t.Error("missing product filter")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	for _, test := range []struct {
		track    Track
		from, to string
	}{
		{ReleaseTrack(), "5.0.0", "5.1.0"}, {BetaTrack(), "6.0.0-rc.1", "6.0.0-rc.2"},
	} {
		matrix, err := Discover(t.Context(), client, "https://example.invalid/catalog", testImage, "udmpro", cutoff, test.track)
		if err != nil {
			t.Fatal(err)
		}
		pair := matrix.Include[0]
		if pair.Firmwares[0].Version != test.from || pair.Firmwares[1].Version != test.to {
			t.Fatalf("pair=%+v", pair)
		}
		if err := test.track.ValidatePair("udmpro", pair.Firmwares); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrackRejectsInvalidVersions(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"6", "6.0", "6.0.0-rc.01", "6.0.0+build.1", "v6.0.0"} {
		if BetaTrack().accepts(version) {
			t.Errorf("accepted %q", version)
		}
	}
}

func TestBetaBuildAndPublishKeepStableAliases(t *testing.T) {
	t.Parallel()
	pair := pairFor("udmpro")
	pair[1].Version = "6.0.0-rc.1"
	runner := &fakeRunner{visibility: "public", fingerprints: map[string]string{}}
	for _, release := range pair {
		runner.fingerprints[testImage+":udmpro-beta-"+release.Version] = Fingerprint("udmpro", release)
	}
	pipeline := Pipeline{Track: BetaTrack(), Runner: runner, Images: runner, Log: io.Discard}
	if err := pipeline.Build(t.Context(), testImage, "udmpro", t.TempDir(), pair); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Publish(t.Context(), testImage, "udmpro", pair); err != nil {
		t.Fatal(err)
	}
	if runner.count("docker push "+testImage+":beta") != 1 {
		t.Fatal("missing beta alias")
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "latest") {
			t.Fatalf("beta changed stable alias: %s", call)
		}
	}
	if err := ReleaseTrack().ValidatePair("udmpro", pair); err == nil {
		t.Fatal("stable validation accepted prerelease")
	}
}

func TestPublishedTracks(t *testing.T) {
	t.Parallel()
	_, tags := catalogFixture(t, time.Now())
	var versions strings.Builder
	versions.WriteString(tags)
	for _, model := range models {
		versions.WriteString("\n" + model.Name + "-beta-6.0.0-rc.1")
		versions.WriteString("\n" + model.Name + "-beta-6.0.0")
	}
	stable, err := ReleaseTrack().Published(versions.String(), testImage)
	if err != nil {
		t.Fatal(err)
	}
	assertLatestPairs(t, stable)
	matrix, err := BetaTrack().Published(versions.String(), testImage)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range matrix.Include {
		if !strings.HasSuffix(pair.From, "-beta-6.0.0-rc.1") || !strings.HasSuffix(pair.To, "-beta-6.0.0") {
			t.Fatalf("pair=%+v", pair)
		}
	}
}

func TestBetaPublishedIncludesStableImages(t *testing.T) {
	t.Parallel()
	_, tags := catalogFixture(t, time.Now())
	// The beta track can use an existing stable image without rebuilding it.
	selected := BetaTrack().publishedVersions(tags+"\nudmpro-beta-6.0.0", "udmpro")
	if selected["5.1.10"] != "udmpro-5.1.10" || selected["6.0.0"] != "udmpro-beta-6.0.0" {
		t.Fatalf("selected=%v", selected)
	}
}
