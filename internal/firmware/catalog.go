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

const CatalogURL = "https://fw-update.ui.com/api/firmware"

var models = []struct{ Name, Board string }{
	{"udm", "UDM"},
	{"udmpro", "UDMPRO"},
	{"udmse", "UDMPROSE"},
	{"udmpromax", "UDMPROMAX"},
	{"udmbeast", "UDMEA4C"},
}

type Release struct {
	Board   string `json:"board"`
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

type Pair struct {
	Model     string    `json:"model"`
	Firmwares []Release `json:"firmwares,omitempty"`
	From      string    `json:"from"`
	To        string    `json:"to"`
}

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

	return "", fmt.Errorf("unknown model: %s", model)
}

var (
	downloadURL = regexp.MustCompile(`^https://fw-download\.ubnt\.com/[A-Za-z0-9/_.-]+\.bin$`)
	imageName   = regexp.MustCompile(`^ghcr\.io/[a-z0-9][a-z0-9_.-]*/unifi-os$`)
)

func ValidateImage(image string) error {
	if !imageName.MatchString(image) {
		return fmt.Errorf("invalid image repository: %s", image)
	}

	return nil
}

func ValidatePair(model string, releases []Release) error {
	board, err := boardFor(model)
	if err != nil {
		return err
	}
	if len(releases) != 2 {
		return fmt.Errorf("two firmware releases required for %s", model)
	}
	for _, release := range releases {
		checksum, err := hex.DecodeString(release.SHA256)
		if release.Board != board || !stable(release.Version) || !downloadURL.MatchString(release.URL) || err != nil || len(checksum) != 32 {
			return fmt.Errorf("invalid firmware metadata for %s", model)
		}
	}
	if compare(releases[0].Version, releases[1].Version) >= 0 {
		return errors.New("firmware releases must be distinct and ascending")
	}

	return nil
}

func Discover(ctx context.Context, client *http.Client, endpoint, image, model string, cutoff time.Time) (Matrix, error) {
	address, err := url.Parse(endpoint)
	if err != nil {
		return Matrix{}, err
	}
	query := address.Query()
	query.Add("filter", "eq~~product~~unifi-dream")
	query.Add("filter", "eq~~channel~~release")
	query.Set("sort", "-created")
	query.Set("limit", "1000")
	address.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return Matrix{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return Matrix{}, err
	}
	matrix, result := discoverFromResponse(response, image, model, cutoff)
	if closeErr := response.Body.Close(); closeErr != nil {
		result = errors.Join(result, fmt.Errorf("close firmware catalog response: %w", closeErr))
	}

	return matrix, result
}

func discoverFromResponse(response *http.Response, image, model string, cutoff time.Time) (Matrix, error) {
	if response.StatusCode != http.StatusOK {
		return Matrix{}, fmt.Errorf("firmware catalog: HTTP %d", response.StatusCode)
	}

	return SelectCatalog(io.LimitReader(response.Body, 16<<20), image, model, cutoff)
}

func SelectCatalog(reader io.Reader, image, model string, cutoff time.Time) (Matrix, error) {
	err := ValidateImage(image)
	if err != nil {
		return Matrix{}, err
	}
	if model != "all" {
		if _, err := boardFor(model); err != nil {
			return Matrix{}, err
		}
	}
	var catalog struct {
		Embedded struct {
			Firmware []catalogRelease `json:"firmware"`
		} `json:"_embedded"`
	}
	err = json.NewDecoder(reader).Decode(&catalog)
	if err != nil {
		return Matrix{}, fmt.Errorf("decode firmware catalog: %w", err)
	}
	matrix := Matrix{}
	for _, device := range models {
		if model != "all" && model != device.Name {
			continue
		}
		latest := make(map[string]catalogRelease)
		for _, entry := range catalog.Embedded.Firmware {
			version, _, _ := strings.Cut(strings.TrimPrefix(entry.Version, "v"), "+")
			if entry.Platform != device.Board || entry.Created.After(cutoff) || !stable(version) {
				continue
			}
			previous, exists := latest[version]
			if !exists || entry.Created.After(previous.Created) {
				latest[version] = entry
			}
		}
		versions := make([]string, 0, len(latest))
		for version := range latest {
			versions = append(versions, version)
		}
		slices.SortFunc(versions, compare)
		if len(versions) < 2 {
			return Matrix{}, fmt.Errorf("two stable releases required for %s", device.Name)
		}
		pair := Pair{Model: device.Name}
		for _, version := range versions[len(versions)-2:] {
			entry := latest[version]
			pair.Firmwares = append(pair.Firmwares, Release{device.Board, version, entry.Links.Data.Href, entry.SHA256})
		}
		err := ValidatePair(device.Name, pair.Firmwares)
		if err != nil {
			return Matrix{}, err
		}
		pair.From, pair.To = image+":"+device.Name+"-"+versions[len(versions)-2], image+":"+device.Name+"-"+versions[len(versions)-1]
		matrix.Include = append(matrix.Include, pair)
	}

	return matrix, nil
}

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
		if len(versions) < 2 {
			return Matrix{}, fmt.Errorf("two published firmware versions required for %s", device.Name)
		}
		matrix.Include = append(matrix.Include, Pair{Model: device.Name, From: image + ":" + device.Name + "-" + versions[len(versions)-2], To: image + ":" + device.Name + "-" + versions[len(versions)-1]})
	}

	return matrix, nil
}
