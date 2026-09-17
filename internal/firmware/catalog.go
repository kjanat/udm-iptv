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
)

var (
	errUnknownModel           = errors.New("unknown model")
	errInvalidImageRepository = errors.New("invalid image repository")
	errReleasePairRequired    = errors.New("two firmware releases required")
	errInvalidReleaseMetadata = errors.New("invalid firmware metadata")
	errUnorderedReleases      = errors.New("firmware releases must be distinct and ascending")
	errCatalogStatus          = errors.New("firmware catalog")
	errStablePairRequired     = errors.New("two stable releases required")
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
	Version  string    `json:"version"`
	Created  time.Time `json:"created"`
	SHA256   string    `json:"sha256_checksum"`
	Links    struct {
		Data struct {
			Href string `json:"href"`
		} `json:"data"`
	} `json:"_links"`
}

func stable(version string) bool {
	return strings.Count(version, ".") == 2 && semver.IsValid("v"+version) &&
		semver.Prerelease("v"+version) == "" && semver.Build("v"+version) == ""
}

func compare(a, b string) int { return semver.Compare("v"+a, "v"+b) }

func boardFor(model string) (string, error) {
	for _, item := range models {
		if item.Name == model {
			return item.Board, nil
		}
	}

	return "", fmt.Errorf("%w: %s", errUnknownModel, model)
}

var (
	downloadURL = regexp.MustCompile(`^https://fw-download\.ubnt\.com/[A-Za-z0-9/_.-]+\.bin$`)
	imageName   = regexp.MustCompile(`^ghcr\.io/[a-z0-9][a-z0-9_.-]*/unifi-os$`)
)

// ValidateImage reports whether image is a valid ghcr.io/*/unifi-os repository.
func ValidateImage(image string) error {
	if !imageName.MatchString(image) {
		return fmt.Errorf("%w: %s", errInvalidImageRepository, image)
	}

	return nil
}

// ValidatePair reports whether releases is a distinct, ascending pair for model.
func ValidatePair(model string, releases []Release) error {
	board, err := boardFor(model)
	if err != nil {
		return err
	}
	if len(releases) != releasesInPair {
		return fmt.Errorf("%w for %s", errReleasePairRequired, model)
	}
	for _, release := range releases {
		checksum, err := hex.DecodeString(release.SHA256)
		if release.Board != board || !stable(release.Version) || !downloadURL.MatchString(release.URL) || err != nil || len(checksum) != 32 {
			return fmt.Errorf("%w for %s", errInvalidReleaseMetadata, model)
		}
	}
	if compare(releases[0].Version, releases[1].Version) >= 0 {
		return errUnorderedReleases
	}

	return nil
}

// Discover fetches the latest stable firmware pairs from endpoint.
func Discover(ctx context.Context, client *http.Client, endpoint, image, model string, cutoff time.Time) (Matrix, error) {
	address, err := url.Parse(endpoint)
	if err != nil {
		return Matrix{}, fmt.Errorf("parse firmware catalog endpoint %s: %w", endpoint, err)
	}
	query := address.Query()
	query.Add("filter", "eq~~product~~unifi-dream")
	query.Add("filter", "eq~~channel~~release")
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
	matrix, result := discoverFromResponse(response, image, model, cutoff)
	if closeErr := response.Body.Close(); closeErr != nil {
		result = errors.Join(result, fmt.Errorf("close firmware catalog response: %w", closeErr))
	}

	return matrix, result
}

func discoverFromResponse(response *http.Response, image, model string, cutoff time.Time) (Matrix, error) {
	if response.StatusCode != http.StatusOK {
		return Matrix{}, fmt.Errorf("%w: HTTP %d", errCatalogStatus, response.StatusCode)
	}

	return SelectCatalog(io.LimitReader(response.Body, catalogResponseLimit), image, model, cutoff)
}

// SelectCatalog picks the latest two stable, non-prerelease firmware versions
// per model from a catalog API response.
func SelectCatalog(reader io.Reader, image, model string, cutoff time.Time) (Matrix, error) {
	err := validateCatalogSelectors(image, model)
	if err != nil {
		return Matrix{}, err
	}
	entries, err := decodeCatalog(reader)
	if err != nil {
		return Matrix{}, err
	}
	matrix := Matrix{}
	for _, device := range models {
		if model != "all" && model != device.Name {
			continue
		}
		pair, err := catalogPair(device.Name, device.Board, image, newestPerVersion(entries, device.Board, cutoff))
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
	if model == "all" {
		return nil
	}
	_, err = boardFor(model)

	return err
}

func decodeCatalog(reader io.Reader) ([]catalogRelease, error) {
	var catalog struct {
		Embedded struct {
			Firmware []catalogRelease `json:"firmware"`
		} `json:"_embedded"`
	}
	err := json.NewDecoder(reader).Decode(&catalog)
	if err != nil {
		return nil, fmt.Errorf("decode firmware catalog: %w", err)
	}

	return catalog.Embedded.Firmware, nil
}

func newestPerVersion(entries []catalogRelease, board string, cutoff time.Time) map[string]catalogRelease {
	latest := make(map[string]catalogRelease)
	for _, entry := range entries {
		version, _, _ := strings.Cut(strings.TrimPrefix(entry.Version, "v"), "+")
		if entry.Platform != board || entry.Created.After(cutoff) || !stable(version) {
			continue
		}
		previous, exists := latest[version]
		if !exists || entry.Created.After(previous.Created) {
			latest[version] = entry
		}
	}

	return latest
}

func catalogPair(name, board, image string, latest map[string]catalogRelease) (Pair, error) {
	versions := make([]string, 0, len(latest))
	for version := range latest {
		versions = append(versions, version)
	}
	slices.SortFunc(versions, compare)
	if len(versions) < releasesInPair {
		return Pair{}, fmt.Errorf("%w for %s", errStablePairRequired, name)
	}
	pair := Pair{Model: name}
	for _, version := range versions[len(versions)-2:] {
		entry := latest[version]
		pair.Firmwares = append(pair.Firmwares, Release{board, version, entry.Links.Data.Href, entry.SHA256})
	}
	err := ValidatePair(name, pair.Firmwares)
	if err != nil {
		return Pair{}, err
	}
	pair.From, pair.To = image+":"+name+"-"+versions[len(versions)-2], image+":"+name+"-"+versions[len(versions)-1]

	return pair, nil
}

// Published selects the latest two published firmware tags per model.
func Published(tags, image string) (Matrix, error) {
	err := ValidateImage(image)
	if err != nil {
		return Matrix{}, err
	}
	matrix := Matrix{}
	for _, device := range models {
		versions := make([]string, 0)
		for tag := range strings.FieldsSeq(tags) {
			version, found := strings.CutPrefix(tag, device.Name+"-")
			if found && stable(version) {
				versions = append(versions, version)
			}
		}
		slices.SortFunc(versions, compare)
		versions = slices.Compact(versions)
		if len(versions) < releasesInPair {
			return Matrix{}, fmt.Errorf("%w for %s", errPublishedPairRequired, device.Name)
		}
		matrix.Include = append(matrix.Include, Pair{Model: device.Name, From: image + ":" + device.Name + "-" + versions[len(versions)-2], To: image + ":" + device.Name + "-" + versions[len(versions)-1]})
	}

	return matrix, nil
}
