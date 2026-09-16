package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmbeddedProfilesParse(t *testing.T) {
	t.Parallel()
	parsed, err := ParseCatalog(embeddedCatalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bt", "init7", "kpn", "magentatv", "meo", "posttv", "solcon", "swisscom", "telenor", "tweak", "vivo", "vivogvt"} {
		if _, found := parsed.Profile(id); !found {
			t.Errorf("profile %q missing", id)
		}
	}
	kpn, _ := parsed.Profile("kpn")
	if kpn.Config.WAN.Interface != "eth8" || kpn.Config.WAN.VLANInterface != "iptv" || kpn.Config.Proxy.Program != "improxy" {
		t.Fatalf("base not applied: %#v", kpn.Config)
	}
	if !kpn.Config.Telemetry.Enabled {
		t.Fatal("base telemetry not applied")
	}
}

const validProfileDocument = `{
  "schemaVersion": 1,
  "countries": {"XX": {"name": "Example", "localName": "Example"}},
  "providers": {"acme": {"name": "ACME", "countries": ["XX"], "profiles": ["acme"]}},
  "profiles": {
    "acme": {
      "name": "ACME",
      "wan": {"vlan": 4, "dhcp": true, "natDestinations": ["10.0.0.0/8"]},
      "sourceRanges": ["224.0.0.0/4"]
    }
  }
}`

type profileEdit = func(document, profile, wan map[string]any)

func providerOf(document map[string]any) map[string]any {
	providers, _ := document["providers"].(map[string]any)
	provider, _ := providers["acme"].(map[string]any)

	return provider
}

func editedProfileDocument(t *testing.T, edit profileEdit) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(validProfileDocument), &document); err != nil {
		t.Fatal(err)
	}
	profiles, _ := document["profiles"].(map[string]any)
	profile, _ := profiles["acme"].(map[string]any)
	wan, _ := profile["wan"].(map[string]any)
	edit(document, profile, wan)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	return encoded
}

func TestCatalogNavigation(t *testing.T) {
	t.Parallel()
	catalog := DefaultCatalog()
	if got := len(catalog.ProvidersIn("NL")); got != 5 {
		t.Fatalf("NL providers = %d", got)
	}
	if got := catalog.ProfilesOf("vivo"); len(got) != 2 || got[0].ID != "vivo" || got[1].ID != "vivogvt" {
		t.Fatalf("vivo profiles = %v", got)
	}
	if country, provider, found := catalog.Locate("kpn"); !found || country != "NL" || provider != "kpn" {
		t.Fatalf("locate kpn = %s %s %v", country, provider, found)
	}
	if _, _, found := catalog.Locate("custom"); found {
		t.Fatal("custom located")
	}
}

func TestProfileSchemaAcceptsMinimalProfile(t *testing.T) {
	t.Parallel()
	if _, err := ParseCatalog([]byte(validProfileDocument)); err != nil {
		t.Fatal(err)
	}
}

func TestProfileSchemaRejects(t *testing.T) {
	t.Parallel()
	for name, edit := range profileSchemaRejections() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseCatalog(editedProfileDocument(t, edit)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func profileSchemaRejections() map[string]profileEdit {
	return map[string]profileEdit{
		"unknown top-level key": func(d, _, _ map[string]any) { d["extra"] = true },
		"schema version":        func(d, _, _ map[string]any) { d["schemaVersion"] = 2 },
		"reserved id custom":    func(d, p, _ map[string]any) { d["profiles"] = map[string]any{"custom": p} },
		"uppercase id":          func(d, p, _ map[string]any) { d["profiles"] = map[string]any{"Acme": p} },
		"missing countries":     func(d, _, _ map[string]any) { delete(d, "countries") },
		"lowercase country": func(d, _, _ map[string]any) {
			d["countries"] = map[string]any{"xx": map[string]any{"name": "E", "localName": "E"}}
		},
		"country without name":      func(d, _, _ map[string]any) { d["countries"] = map[string]any{"XX": map[string]any{"localName": "E"}} },
		"provider unknown key":      func(d, _, _ map[string]any) { providerOf(d)["logo"] = "x" },
		"provider no profiles":      func(d, _, _ map[string]any) { providerOf(d)["profiles"] = []string{} },
		"provider dangling profile": func(d, _, _ map[string]any) { providerOf(d)["profiles"] = []string{"nope"} },
		"provider dangling country": func(d, _, _ map[string]any) { providerOf(d)["countries"] = []string{"ZZ"} },
		"orphan profile": func(d, p, _ map[string]any) {
			profiles, _ := d["profiles"].(map[string]any)
			profiles["orphan"] = p
		},
		"orphan country": func(d, _, _ map[string]any) {
			countries, _ := d["countries"].(map[string]any)
			countries["ZZ"] = map[string]any{"name": "Z", "localName": "Z"}
		},
		"unknown profile key":  func(_, p, _ map[string]any) { p["igmp"] = 3 },
		"missing sourceRanges": func(_, p, _ map[string]any) { delete(p, "sourceRanges") },
		"empty sourceRanges":   func(_, p, _ map[string]any) { p["sourceRanges"] = []string{} },
		"duplicate sources":    func(_, p, _ map[string]any) { p["sourceRanges"] = []string{"10.0.0.0/8", "10.0.0.0/8"} },
		"padded name":          func(_, p, _ map[string]any) { p["name"] = " ACME" },
		"host without prefix":  func(_, _, w map[string]any) { w["natDestinations"] = []string{"10.0.0.1"} },
		"octet out of range":   func(_, _, w map[string]any) { w["natDestinations"] = []string{"256.0.0.0/8"} },
		"prefix too long":      func(_, _, w map[string]any) { w["natDestinations"] = []string{"10.0.0.0/33"} },
		"ipv6":                 func(_, _, w map[string]any) { w["natDestinations"] = []string{"::/0"} },
		"vlan too high":        func(_, _, w map[string]any) { w["vlan"] = 4095 },
		"vlan negative":        func(_, _, w map[string]any) { w["vlan"] = -1 },
		"vlan float":           func(_, _, w map[string]any) { w["vlan"] = 4.5 },
		"vlan string":          func(_, _, w map[string]any) { w["vlan"] = "4" },
		"missing dhcp":         func(_, _, w map[string]any) { delete(w, "dhcp") },
		"dhcp with static":     func(_, _, w map[string]any) { w["staticAddress"] = "10.0.0.2/24" },
		"static with options": func(_, _, w map[string]any) {
			w["dhcp"] = false
			w["dhcpOptions"] = []string{"-O", "staticroutes"}
		},
		"empty dhcpOptions":  func(_, _, w map[string]any) { w["dhcpOptions"] = []string{} },
		"shell in option":    func(_, _, w map[string]any) { w["dhcpOptions"] = []string{"$(id)"} },
		"vlan mac in wan":    func(_, _, w map[string]any) { w["vlanMAC"] = "00:11:22:33:44:55" },
		"interface with dot": func(_, _, w map[string]any) { w["interface"] = ".hidden" },
		"interface too long": func(_, _, w map[string]any) { w["interface"] = strings.Repeat("e", 16) },
	}
}

func TestParseProfilesRejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	if _, err := ParseCatalog([]byte("{")); err == nil {
		t.Fatal("accepted")
	}
}
