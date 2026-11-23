// Package firmware builds and publishes tested UniFi OS images.
package firmware

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// CatalogURL is Ubiquiti's firmware catalog API endpoint.
const CatalogURL = "https://fw-update.ui.com/api/firmware"

const (
	// releasesInPair is the old and new firmware release a migration needs.
	releasesInPair = 2
	// catalogResponseLimit bounds the firmware catalog HTTP response body.
	catalogResponseLimit = 16 << 20
	// channelRelease and channelBeta are Ubiquiti's names for the channels that
	// carry console firmware.
	channelRelease = "release"
	channelBeta    = "beta-public"
	// trackRelease and trackBeta are the names --track accepts.
	trackRelease = "release"
	trackBeta    = "beta"
	trackPinned  = "pinned"
	modelAll     = "all"
)

var (
	errUnknownModel           = errors.New("unknown model")
	errInvalidImageRepository = errors.New("invalid image repository")
	errReleasePairRequired    = errors.New("two firmware releases required")
	errInvalidReleaseMetadata = errors.New("invalid firmware metadata")
	errUnorderedReleases      = errors.New("firmware releases must be distinct and ascending")
	errCatalogStatus          = errors.New("firmware catalog")
	errCatalogPairRequired    = errors.New("two firmware releases required on the selected track")
	errPublishedPairRequired  = errors.New("two published firmware versions required")
)

var models = []struct{ Name, Board string }{
	{"udm", "UDM"},
	{"udmpro", "UDMPRO"},
	{"udmse", "UDMPROSE"},
	{"udmpromax", "UDMPROMAX"},
	{"udmbeast", "UDMEA4C"},
}

// Release is one downloadable firmware image for a board.
type Release struct {
	Board   string `json:"board"`
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

// Pair is a model's old and new firmware, and the container image tags built for both.
type Pair struct {
	Model     string    `json:"model"`
	Firmwares []Release `json:"firmwares,omitempty"`
	From      string    `json:"from"`
	To        string    `json:"to"`
}

// Matrix is a GitHub Actions build matrix of firmware pairs.
type Matrix struct {
	Include []Pair `json:"include"`
}

type catalogRelease struct {
	Platform string    `json:"platform"`
	Channel  string    `json:"channel"`
	Version  string    `json:"version"`
	Created  time.Time `json:"created"`
	SHA256   string    `json:"sha256_checksum"`
	Links    struct {
		Data struct {
			Href string `json:"href"`
		} `json:"data"`
	} `json:"_links"`
}

type catalogDocument struct {
	Embedded struct {
		Firmware []catalogRelease `json:"firmware"`
	} `json:"_embedded"`
}

// Track is the set of firmware channels a matrix draws from, and whether a
// prerelease version counts as a candidate. The zero value selects releases.
type Track struct {
	Channels   []string
	Prerelease bool
}

// ReleaseTrack is the channel every console is offered.
func ReleaseTrack() Track { return Track{Channels: []string{channelRelease}} }

// BetaTrack adds the channel carrying beta and release-candidate builds.
func BetaTrack() Track {
	return Track{Channels: []string{channelRelease, channelBeta}, Prerelease: true}
}

// PinnedTrack exercises explicitly recorded builds absent from the catalog.
// Its image tags and moving aliases stay separate from released firmware.
func PinnedTrack() Track { return Track{Channels: []string{trackPinned}, Prerelease: true} }

func (track Track) pinned() bool { return slices.Contains(track.Channels, trackPinned) }

// TrackNamed returns the track name selects, and whether that name exists.
func TrackNamed(name string) (Track, bool) {
	switch name {
	case "", trackRelease:
		return ReleaseTrack(), true
	case trackBeta:
		return BetaTrack(), true
	case trackPinned:
		return PinnedTrack(), true
	}

	return Track{}, false
}

func (track Track) carries(channel string) bool {
	if track.Channels == nil {
		return channel == channelRelease
	}
	return slices.Contains(track.Channels, channel)
}

// accepts reports whether version is a candidate on this track. Build metadata
// never is, since two builds of one version are the same firmware.
func (track Track) accepts(version string) bool {
	core, _, _ := strings.Cut(version, "-")
	if strings.Count(core, ".") != 2 || !semver.IsValid("v"+version) || semver.Build("v"+version) != "" {
		return false
	}
	if semver.Prerelease("v"+version) == "" {
		return true
	}

	return track.Prerelease
}

func compare(a, b string) int { return semver.Compare("v"+a, "v"+b) }

func (track Track) versionTag(model, version string) string {
	if track.pinned() {
		return model + "-pinned-" + version
	}
	if track.Prerelease {
		return model + "-beta-" + version
	}
	return model + "-" + version
}

func (track Track) latestAlias() string {
	if track.pinned() {
		return trackPinned
	}
	if track.Prerelease {
		return trackBeta
	}
	return "latest"
}

func boardFor(model string) (string, error) {
	for _, item := range models {
		if item.Name == model {
			return item.Board, nil
		}
	}

	if validModel.MatchString(model) && model != modelAll {
		return strings.ToUpper(model), nil
	}

	return "", fmt.Errorf("%w: %s", errUnknownModel, model)
}

var (
	downloadURL = regexp.MustCompile(`^https://fw-download\.ubnt\.com/[A-Za-z0-9/_.-]+\.bin$`)
	imageName   = regexp.MustCompile(`^ghcr\.io/[a-z0-9][a-z0-9_.-]*/unifi-os$`)
	validModel  = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
)

// ValidateImage reports whether image is a valid ghcr.io/*/unifi-os repository.
func ValidateImage(image string) error {
	if !imageName.MatchString(image) {
		return fmt.Errorf("%w: %s", errInvalidImageRepository, image)
	}

	return nil
}

// ValidatePair reports whether releases is a distinct, ascending release pair
// for model.
func ValidatePair(model string, releases []Release) error {
	return ReleaseTrack().ValidatePair(model, releases)
}

// ValidatePair reports whether releases is a distinct, ascending pair for model
// on this track.
func (track Track) ValidatePair(model string, releases []Release) error {
	board, err := boardFor(model)
	if err != nil {
		return err
	}
	if len(releases) != releasesInPair {
		return fmt.Errorf("%w for %s", errReleasePairRequired, model)
	}
	for _, release := range releases {
		checksum, err := hex.DecodeString(release.SHA256)
		if release.Board != board || !track.accepts(release.Version) || !downloadURL.MatchString(release.URL) || err != nil || len(checksum) != 32 {
			return fmt.Errorf("%w for %s", errInvalidReleaseMetadata, model)
		}
	}
	if compare(releases[0].Version, releases[1].Version) >= 0 {
		return errUnorderedReleases
	}

	return nil
}

// Discover fetches the latest firmware pairs on track from endpoint.
func Discover(ctx context.Context, client *http.Client, endpoint, image, model string, cutoff time.Time, track Track) (Matrix, error) {
	address, err := url.Parse(endpoint)
	if err != nil {
		return Matrix{}, fmt.Errorf("parse firmware catalog endpoint %s: %w", endpoint, err)
	}
	query := address.Query()
	query.Add("filter", "eq~~product~~unifi-dream")
	// The channel is filtered here rather than in the query, which takes one
	// equality per field.
	query.Set("sort", "-created")
	query.Set("limit", "1000")
	address.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return Matrix{}, fmt.Errorf("build firmware catalog request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return Matrix{}, fmt.Errorf("fetch firmware catalog: %w", err)
	}
	matrix, result := discoverFromResponse(response, image, model, cutoff, track)
	if closeErr := response.Body.Close(); closeErr != nil {
		result = errors.Join(result, fmt.Errorf("close firmware catalog response: %w", closeErr))
	}

	return matrix, result
}

func discoverFromResponse(response *http.Response, image, model string, cutoff time.Time, track Track) (Matrix, error) {
	if response.StatusCode != http.StatusOK {
		return Matrix{}, fmt.Errorf("%w: HTTP %d", errCatalogStatus, response.StatusCode)
	}

	return track.SelectCatalog(io.LimitReader(response.Body, catalogResponseLimit), image, model, cutoff)
}

// SelectCatalog picks the latest two stable, non-prerelease firmware versions
// per model from a catalog API response.
func SelectCatalog(reader io.Reader, image, model string, cutoff time.Time) (Matrix, error) {
	return ReleaseTrack().SelectCatalog(reader, image, model, cutoff)
}

// SelectCatalog picks the latest two firmware versions on this track per model.
func (track Track) SelectCatalog(reader io.Reader, image, model string, cutoff time.Time) (Matrix, error) {
	err := validateCatalogSelectors(image, model)
	if err != nil {
		return Matrix{}, err
	}
	entries, err := decodeCatalog(reader)
	if err != nil {
		return Matrix{}, err
	}
	if track.pinned() {
		return selectPinnedCatalog(entries, image, model, cutoff)
	}
	devices, err := catalogModels(entries, model, cutoff, track)
	if err != nil {
		return Matrix{}, err
	}
	matrix := Matrix{}
	for _, device := range devices {
		pair, err := track.catalogPair(device.Name, device.Board, image, newestPerVersion(entries, device.Board, cutoff, track))
		if err != nil {
			return Matrix{}, err
		}
		matrix.Include = append(matrix.Include, pair)
	}

	return matrix, nil
}

func validateCatalogSelectors(image, model string) error {
	err := ValidateImage(image)
	if err != nil {
		return err
	}
	if model == modelAll {
		return nil
	}
	_, err = boardFor(model)

	return err
}

func decodeCatalog(reader io.Reader) ([]catalogRelease, error) {
	var catalog catalogDocument
	err := json.NewDecoder(reader).Decode(&catalog)
	if err != nil {
		return nil, fmt.Errorf("decode firmware catalog: %w", err)
	}

	return catalog.Embedded.Firmware, nil
}

func newestPerVersion(entries []catalogRelease, board string, cutoff time.Time, track Track) map[string]catalogRelease {
	latest := make(map[string]catalogRelease)
	for _, entry := range entries {
		version, _, _ := strings.Cut(strings.TrimPrefix(entry.Version, "v"), "+")
		if entry.Platform != board || entry.Created.After(cutoff) || !track.accepts(version) || !track.carries(entry.Channel) {
			continue
		}
		previous, exists := latest[version]
		if !exists || entry.Created.After(previous.Created) {
			latest[version] = entry
		}
	}

	return latest
}

func (track Track) catalogPair(name, board, image string, latest map[string]catalogRelease) (Pair, error) {
	versions := make([]string, 0, len(latest))
	for version := range latest {
		versions = append(versions, version)
	}
	slices.SortFunc(versions, compare)
	if len(versions) < releasesInPair {
		return Pair{}, fmt.Errorf("%w for %s", errCatalogPairRequired, name)
	}
	pair := Pair{Model: name}
	for _, version := range versions[len(versions)-2:] {
		entry := latest[version]
		pair.Firmwares = append(pair.Firmwares, Release{board, version, entry.Links.Data.Href, entry.SHA256})
	}
	err := track.ValidatePair(name, pair.Firmwares)
	if err != nil {
		return Pair{}, err
	}
	pair.From, pair.To = image+":"+track.versionTag(name, versions[len(versions)-2]), image+":"+track.versionTag(name, versions[len(versions)-1])

	return pair, nil
}

// Published selects the latest two published firmware tags per model.
func Published(tags, image string) (Matrix, error) {
	return ReleaseTrack().Published(tags, image)
}

// Published selects version tags accepted by this track.
func (track Track) Published(tags, image string) (Matrix, error) {
	err := ValidateImage(image)
	if err != nil {
		return Matrix{}, err
	}
	if track.pinned() {
		return publishedPins(tags, image)
	}
	matrix := Matrix{}
	for _, device := range track.publishedModels(tags) {
		selected := track.publishedVersions(tags, device.Name)
		versions := make([]string, 0, len(selected))
		for version := range selected {
			versions = append(versions, version)
		}
		slices.SortFunc(versions, compare)
		if len(versions) < releasesInPair {
			// Newly published models may have only one image so far; they must
			// not prevent testing complete upgrade pairs on other models.
			continue
		}
		matrix.Include = append(matrix.Include, Pair{Model: device.Name, From: image + ":" + selected[versions[len(versions)-2]], To: image + ":" + selected[versions[len(versions)-1]]})
	}
	if len(matrix.Include) == 0 {
		return Matrix{}, errPublishedPairRequired
	}

	return matrix, nil
}

func (track Track) publishedVersions(tags, model string) map[string]string {
	selected := make(map[string]string)
	for tag := range strings.FieldsSeq(tags) {
		name, version, found := strings.Cut(tag, "-")
		if !found || !sameModel(name, model) {
			continue
		}
		beta := false
		if track.pinned() {
			version, found = strings.CutPrefix(version, trackPinned+"-")
			if !found {
				continue
			}
		} else if track.Prerelease {
			version, beta = strings.CutPrefix(version, "beta-")
		}
		if track.accepts(version) && (selected[version] == "" || beta) {
			selected[version] = tag
		}
	}
	return selected
}
