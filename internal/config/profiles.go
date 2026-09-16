package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Profile represents a provider profile with its ID, name, note,
// and configuration.
type Profile struct {
	ID     string
	Name   string
	Note   string
	Config Config
}

// Country is a market with at least one provider.
type Country struct {
	Code      string
	Name      string
	LocalName string
}

// Provider is a brand a subscriber recognizes; it maps to one or more profiles.
type Provider struct {
	ID        string
	Name      string
	Countries []string
	Profiles  []string
}

// Catalog is the country → provider → profile hierarchy the wizard walks.
// Slices are sorted by display name.
type Catalog struct {
	Countries []Country
	Providers []Provider
	Profiles  []Profile
}

//go:embed profiles.json
var embeddedCatalog []byte

//go:embed profiles.schema.json
var embeddedCatalogSchema []byte

const catalogSchemaURL = "https://raw.githubusercontent.com/kjanat/udm-iptv/refs/heads/go/internal/config/profiles.schema.json"

var errCatalogReference = errors.New("catalog reference")

type catalogDocument struct {
	Schema        string                        `json:"$schema"`
	SchemaVersion int                           `json:"schemaVersion"`
	Countries     map[string]countryDefinition  `json:"countries"`
	Providers     map[string]providerDefinition `json:"providers"`
	Profiles      map[string]profileDefinition  `json:"profiles"`
}

type countryDefinition struct {
	Name      string `json:"name"`
	LocalName string `json:"localName"`
}

type providerDefinition struct {
	Name      string   `json:"name"`
	Countries []string `json:"countries"`
	Profiles  []string `json:"profiles"`
}

type profileDefinition struct {
	Name         string     `json:"name"`
	Note         string     `json:"note"`
	WAN          profileWAN `json:"wan"`
	SourceRanges []string   `json:"sourceRanges"`
}

type profileWAN struct {
	Interface       string   `json:"interface"`
	VLAN            int      `json:"vlan"`
	DHCP            bool     `json:"dhcp"`
	DHCPOptions     []string `json:"dhcpOptions"`
	StaticAddress   string   `json:"staticAddress"`
	NATDestinations []string `json:"natDestinations"`
}

var catalogSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(embeddedCatalogSchema))
	if err != nil {
		return nil, fmt.Errorf("parse catalog schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(catalogSchemaURL, document); err != nil {
		return nil, fmt.Errorf("register catalog schema: %w", err)
	}
	schema, err := compiler.Compile(catalogSchemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile catalog schema: %w", err)
	}

	return schema, nil
})

// ParseCatalog validates data against the embedded catalog schema, checks
// every cross-reference, and validates each resolved profile configuration.
func ParseCatalog(data []byte) (Catalog, error) {
	schema, err := catalogSchema()
	if err != nil {
		return Catalog{}, err
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return Catalog{}, fmt.Errorf("parse catalog: %w", err)
	}
	if err := schema.Validate(document); err != nil {
		return Catalog{}, fmt.Errorf("validate catalog: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var parsed catalogDocument
	if err := decoder.Decode(&parsed); err != nil {
		return Catalog{}, fmt.Errorf("decode catalog: %w", err)
	}

	return parsed.resolve()
}

func (document catalogDocument) providers() ([]Provider, map[string]bool, map[string]bool, error) {
	var providers []Provider
	usedCountries := map[string]bool{}
	usedProfiles := map[string]bool{}
	for id, definition := range document.Providers {
		for _, code := range definition.Countries {
			if _, found := document.Countries[code]; !found {
				return nil, nil, nil, fmt.Errorf("%w: provider %s lists unknown country %s", errCatalogReference, id, code)
			}
			usedCountries[code] = true
		}
		for _, profile := range definition.Profiles {
			if _, found := document.Profiles[profile]; !found {
				return nil, nil, nil, fmt.Errorf("%w: provider %s lists unknown profile %s", errCatalogReference, id, profile)
			}
			usedProfiles[profile] = true
		}
		providers = append(providers, Provider{ID: id, Name: definition.Name, Countries: definition.Countries, Profiles: definition.Profiles})
	}

	return providers, usedCountries, usedProfiles, nil
}

func (document catalogDocument) resolve() (Catalog, error) {
	providers, usedCountries, usedProfiles, err := document.providers()
	if err != nil {
		return Catalog{}, err
	}
	catalog := Catalog{Providers: providers}
	for code, definition := range document.Countries {
		if !usedCountries[code] {
			return Catalog{}, fmt.Errorf("%w: country %s has no provider", errCatalogReference, code)
		}
		catalog.Countries = append(catalog.Countries, Country{Code: code, Name: definition.Name, LocalName: definition.LocalName})
	}
	for id, definition := range document.Profiles {
		if !usedProfiles[id] {
			return Catalog{}, fmt.Errorf("%w: profile %s has no provider", errCatalogReference, id)
		}
		profile := definition.resolve(id)
		if err := profile.Config.Validate(); err != nil {
			return Catalog{}, fmt.Errorf("profile %s: %w", id, err)
		}
		catalog.Profiles = append(catalog.Profiles, profile)
	}
	sort.Slice(catalog.Countries, func(left, right int) bool { return catalog.Countries[left].Name < catalog.Countries[right].Name })
	sort.Slice(catalog.Providers, func(left, right int) bool { return catalog.Providers[left].Name < catalog.Providers[right].Name })
	sort.Slice(catalog.Profiles, func(left, right int) bool { return catalog.Profiles[left].Name < catalog.Profiles[right].Name })

	return catalog, nil
}

var embedded = sync.OnceValue(func() Catalog {
	catalog, err := ParseCatalog(embeddedCatalog)
	if err != nil {
		panic(err)
	}

	return catalog
})

// DefaultCatalog returns the catalog compiled into the binary.
func DefaultCatalog() Catalog {
	return embedded()
}

// ProvidersIn lists the providers serving the country code, sorted by name.
func (catalog Catalog) ProvidersIn(code string) []Provider {
	var result []Provider
	for _, provider := range catalog.Providers {
		if slices.Contains(provider.Countries, code) {
			result = append(result, provider)
		}
	}

	return result
}

// ProviderByID looks up a provider.
func (catalog Catalog) ProviderByID(id string) (Provider, bool) {
	for _, provider := range catalog.Providers {
		if provider.ID == id {
			return provider, true
		}
	}

	return Provider{}, false
}

// ProfilesOf lists the profiles a provider offers, in the provider's order.
func (catalog Catalog) ProfilesOf(providerID string) []Profile {
	provider, found := catalog.ProviderByID(providerID)
	if !found {
		return nil
	}
	result := make([]Profile, 0, len(provider.Profiles))
	for _, id := range provider.Profiles {
		if profile, found := catalog.Profile(id); found {
			result = append(result, profile)
		}
	}

	return result
}

// Profile looks up a provider profile by ID.
func (catalog Catalog) Profile(id string) (Profile, bool) {
	for _, profile := range catalog.Profiles {
		if profile.ID == id {
			return profile, true
		}
	}

	return Profile{}, false
}

// Locate returns the country code and provider ID that lead to the profile,
// preferring the provider that shares the profile's ID.
func (catalog Catalog) Locate(profileID string) (string, string, bool) {
	if candidate, ok := catalog.ProviderByID(profileID); ok && slices.Contains(candidate.Profiles, profileID) {
		return candidate.Countries[0], candidate.ID, true
	}
	for _, candidate := range catalog.Providers {
		if slices.Contains(candidate.Profiles, profileID) {
			return candidate.Countries[0], candidate.ID, true
		}
	}

	return "", "", false
}

func (definition profileDefinition) resolve(id string) Profile {
	base := genericBase()
	base.Profile = id
	base.WAN.VLAN = definition.WAN.VLAN
	base.WAN.DHCP = definition.WAN.DHCP
	base.WAN.DHCPOptions = definition.WAN.DHCPOptions
	base.WAN.StaticAddress = definition.WAN.StaticAddress
	base.WAN.NATDestinations = definition.WAN.NATDestinations
	if definition.WAN.Interface != "" {
		base.WAN.Interface = definition.WAN.Interface
	}
	base.Proxy.SourceRanges = definition.SourceRanges

	return Profile{ID: id, Name: definition.Name, Note: definition.Note, Config: base}
}

// Profiles returns a sorted list of all available provider profiles, including
// the "Custom" profile.
func Profiles() []Profile {
	known := embedded().Profiles
	result := make([]Profile, 0, 1+len(known))
	result = append(result, Profile{ID: "custom", Name: "Custom", Config: Default()})
	result = append(result, known...)
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })

	return result
}

// ProfileByID returns the provider profile corresponding to the specified ID.
// If the ID is "custom" or "legacy", it returns a profile with the name "Custom".
// If the ID corresponds to a known profile, it returns that profile.
// If the ID is unknown, it returns false.
func ProfileByID(id string) (Profile, bool) {
	if id == "custom" || id == "legacy" {
		return Profile{ID: id, Name: "Custom"}, true
	}

	return embedded().Profile(id)
}

// FromProfile returns a configuration based on the specified provider profile ID.
// If the ID is "custom" or "legacy", it returns the provided current configuration.
// If the ID corresponds to a known profile, it returns the configuration for that profile,
// preserving the telemetry setting from the current configuration.
// If the ID is unknown, it returns an error.
func FromProfile(id string, current Config) (Config, error) {
	if id == "custom" || id == "legacy" {
		current.Profile = id

		return current, nil
	}
	if value, found := embedded().Profile(id); found {
		value.Config.Telemetry = current.Telemetry

		return value.Config, nil
	}

	return Config{}, fmt.Errorf("unknown provider profile %q", id)
}

// InferLegacyProfile recognizes a provider only when every provider-specific
// value matches. Hardware-dependent interface names and user-selectable proxy
// settings intentionally do not participate in the match.
func InferLegacyProfile(value Config) (string, bool) {
	match := ""
	for _, candidate := range embedded().Profiles {
		if value.WAN.VLAN == candidate.Config.WAN.VLAN &&
			value.WAN.DHCP == candidate.Config.WAN.DHCP &&
			value.WAN.StaticAddress == candidate.Config.WAN.StaticAddress &&
			slices.Equal(value.WAN.DHCPOptions, candidate.Config.WAN.DHCPOptions) &&
			slices.Equal(value.WAN.NATDestinations, candidate.Config.Proxy.SourceRanges) {
			if match != "" {
				return "", false
			}
			match = candidate.ID
		}
	}

	return match, match != ""
}
