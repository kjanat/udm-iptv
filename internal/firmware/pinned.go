package firmware

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"time"
)

// These explicit builds and checksums were preserved from master's
// .github/actions/unifi-os-matrix/pinned.json when the shell pipeline migrated.
// Ubiquiti's public firmware catalog does not list these Early Access builds.
//
//go:embed pinned.json
var pinnedJSON []byte

type pinnedBuild struct {
	Platform string `json:"platform"`
	Version  string `json:"version"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
}

func selectPinnedCatalog(entries []catalogRelease, image, model string, cutoff time.Time) (Matrix, error) {
	pins, err := pinnedBuilds()
	if err != nil {
		return Matrix{}, err
	}
	matrix := Matrix{}
	for _, pin := range pins {
		name := modelForBoard(pin.Platform)
		if model != modelAll {
			if !sameModel(model, name) {
				continue
			}
			name = model
		}
		latest := newestPerVersion(entries, pin.Platform, cutoff, ReleaseTrack())
		from, found := newestRelease(latest)
		if !found {
			return Matrix{}, fmt.Errorf("%w for pinned %s", errCatalogPairRequired, name)
		}
		pair := Pair{Model: name, Firmwares: []Release{
			from,
			{Board: pin.Platform, Version: pin.Version, URL: pin.URL, SHA256: pin.SHA256},
		}}
		track := PinnedTrack()
		if err := track.ValidatePair(name, pair.Firmwares); err != nil {
			return Matrix{}, fmt.Errorf("validate pinned pair: %w", err)
		}
		pair.From = image + ":" + track.versionTag(name, from.Version)
		pair.To = image + ":" + track.versionTag(name, pin.Version)
		matrix.Include = append(matrix.Include, pair)
	}
	if len(matrix.Include) == 0 {
		return Matrix{}, fmt.Errorf("%w: no pinned build for %s", errUnknownModel, model)
	}

	return matrix, nil
}

func newestRelease(entries map[string]catalogRelease) (Release, bool) {
	var newest Release
	for version, entry := range entries {
		if newest.Version == "" || compare(version, newest.Version) > 0 {
			newest = Release{Board: entry.Platform, Version: version, URL: entry.Links.Data.Href, SHA256: entry.SHA256}
		}
	}

	return newest, newest.Version != ""
}

func pinnedBuilds() ([]pinnedBuild, error) {
	var pins []pinnedBuild
	if err := json.Unmarshal(pinnedJSON, &pins); err != nil {
		return nil, fmt.Errorf("decode pinned firmware builds: %w", err)
	}

	return pins, nil
}

// The shell pipeline published these exact versions before tracks had their
// own tags. Reuse those images until a pinned-tagged replacement is published.
// Pins without a complete published pair cannot run a lifecycle test yet.
func publishedPins(tags, image string) (Matrix, error) {
	pins, err := pinnedBuilds()
	if err != nil {
		return Matrix{}, err
	}
	matrix := Matrix{Include: []Pair{}}
	for _, pin := range pins {
		model := modelForBoard(pin.Platform)
		versions := ReleaseTrack().publishedVersions(tags, model)
		maps.Copy(versions, PinnedTrack().publishedVersions(tags, model))
		to := versions[pin.Version]
		if to == "" {
			continue
		}
		fromVersion := ""
		for version := range versions {
			if compare(version, pin.Version) < 0 && (fromVersion == "" || compare(version, fromVersion) > 0) {
				fromVersion = version
			}
		}
		if fromVersion != "" {
			matrix.Include = append(matrix.Include, Pair{Model: model, From: image + ":" + versions[fromVersion], To: image + ":" + to})
		}
	}

	return matrix, nil
}
