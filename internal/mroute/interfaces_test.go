package mroute

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPartialStartupFixtures(t *testing.T) {
	for _, name := range []string{"partial-startup.vif", "recovered-startup.vif"} {
		t.Run(name, func(t *testing.T) {
			table := readInterfaces(os.DirFS("testdata"), name)
			if table.Error != "" {
				t.Fatal(table.Error)
			}
			err := table.Check([]string{"wan", "br0", "br2"})
			if name == "partial-startup.vif" {
				if err == nil || !strings.Contains(err.Error(), "br2") {
					t.Fatalf("partial registration passed: %v", err)
				}
			} else if err != nil {
				t.Fatalf("idle, recovered registration failed: %v", err)
			}
		})
	}
}

func TestInterfaceRegistrationEvidence(t *testing.T) {
	for _, test := range []struct{ name, raw, want string }{
		{"partial", routerVIFs, "br2"},
		{"empty-table", "Interface\n", "iptv, br0, br2"},
		{"truncated", "Interface\n1", "truncated"},
		{"invalid-header", "oops\n1 br0\n", "header"},
		{"invalid-index", "Interface\noops br0\n", "index"},
		{"complete-idle", "Interface\n0 iptv 0 0\n1 br0 0 0\n2 br2 0 0\n", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := fstest.MapFS{"vif": {Data: []byte(test.raw)}}
			table := readInterfaces(files, "vif")
			if table.Raw != test.raw {
				t.Fatal("lost raw kernel evidence")
			}
			err := table.Check([]string{"iptv", "br0", "br2"})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Check = %v, want %q", err, test.want)
			}
		})
	}
	if err := readInterfaces(fstest.MapFS{}, "missing").Check([]string{"br0"}); err == nil {
		t.Fatal("unreadable table passed")
	}
}

func TestInterfaceEvidenceRejectsOversize(t *testing.T) {
	table := readInterfaces(fstest.MapFS{"vif": {Data: []byte("Interface\n" + strings.Repeat(" ", readLimit))}}, "vif")
	if table.Error == "" || len(table.Raw) != readLimit {
		t.Fatalf("oversize evidence: error=%q bytes=%d", table.Error, len(table.Raw))
	}
}
