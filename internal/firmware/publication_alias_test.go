package firmware

import (
	"io"
	"slices"
	"strings"
	"testing"
)

func TestPublishKeepsHistoricalPlatformNames(t *testing.T) {
	t.Parallel()
	for _, device := range []struct {
		model, legacy, board string
	}{
		{"udmse", "udmprose", "UDMPROSE"},
		{"udmbeast", "udmea4c", "UDMEA4C"},
	} {
		for _, track := range []struct {
			name, prefix, latest string
			value                Track
		}{
			{"release", "", "latest", ReleaseTrack()},
			{"beta", "beta-", "beta", BetaTrack()},
			{"pinned", "pinned-", "pinned", PinnedTrack()},
		} {
			for _, selected := range []string{device.model, device.legacy} {
				t.Run(selected+"/"+track.name, func(t *testing.T) {
					runner := &fakeRunner{visibility: "public"}
					pipeline := Pipeline{Track: track.value, Runner: runner, Images: runner, Log: io.Discard}
					if err := pipeline.Publish(t.Context(), testImage, selected, pairFor(device.model)); err != nil {
						t.Fatal(err)
					}
					var want []string
					for _, alias := range []string{device.model, device.legacy, device.board} {
						want = append(want, alias+"-"+track.prefix+"5.1.9", alias+"-"+track.prefix+"5.1.10", alias+"-"+track.latest)
					}
					slices.Sort(want)
					assertPublishedAliases(t, runner, want)
				})
			}
		}
	}
}

func assertPublishedAliases(t *testing.T, runner *fakeRunner, want []string) {
	t.Helper()
	for _, operation := range []string{"push", "pull"} {
		var got []string
		for _, call := range runner.calls {
			if tag, ok := strings.CutPrefix(call, "docker "+operation+" "+testImage+":"); ok {
				got = append(got, tag)
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("%s aliases = %v, want %v", operation, got, want)
		}
	}
}
