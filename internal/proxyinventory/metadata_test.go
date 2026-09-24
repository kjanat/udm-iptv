package proxyinventory

import (
	"context"
	"encoding/binary"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestVersionQueriesUseProgramSpecificFlags(t *testing.T) {
	exitOne := exec.CommandContext(t.Context(), "sh", "-c", "exit 1").Run()
	for _, test := range []struct {
		name, flag, output, version, source string
		err                                 error
	}{
		{"improxy", "-v", "version: 0.3\nrevision: abcdef1234567\n", "0.3", "version-command", exitOne},
		{"igmpproxy", "-h", "Usage: igmpproxy [-h] [-v]\n\nigmpproxy 0.4\n", "0.4", "help-output", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := Proxy{Name: test.name, Path: "/usr/sbin/" + test.name, Source: "system"}
			inspectVersion(t.Context(), "", &item, func(_ context.Context, command string, args ...string) (string, error) {
				if command != item.Path || !reflect.DeepEqual(args, []string{test.flag}) {
					t.Fatalf("unsafe query: %s %v", command, args)
				}
				return test.output, test.err
			})
			if item.Version != test.version || item.VersionSource != test.source {
				t.Fatalf("version metadata = %+v", item)
			}
			if test.name == "improxy" && item.Revision != "abcdef1234567" {
				t.Fatal("explicit revision missing")
			}
			if test.name == "igmpproxy" && item.Revision != "" {
				t.Fatal("invented revision")
			}
		})
	}
}

func TestELFBuildIDIsNotARevision(t *testing.T) {
	data := make([]byte, 20)
	binary.LittleEndian.PutUint32(data, 4)
	binary.LittleEndian.PutUint32(data[4:], 4)
	binary.LittleEndian.PutUint32(data[8:], 3)
	copy(data[12:], "GNU\x00")
	copy(data[16:], []byte{1, 2, 3, 4})
	if got := parseBuildID(data, binary.LittleEndian.Uint32); got != "01020304" {
		t.Fatalf("build ID = %q", got)
	}
	for index := range data {
		if got := parseBuildID(data[:index], binary.LittleEndian.Uint32); got != "" {
			t.Fatal("accepted truncated ELF note")
		}
	}
	item := Proxy{Name: "improxy", BuildID: "01020304", Features: familyFeatures("improxy")}
	if item.Revision != "" || item.Features["ipv4_querier_election"].Status != "unknown" {
		t.Fatal("build identity invented supported behavior")
	}
}

func TestPackageVersionStaysSeparateFromProgramVersion(t *testing.T) {
	item := Proxy{Name: "improxy", Path: "/usr/sbin/improxy", Version: "0.3"}
	inspectPackage(t.Context(), &item, func(_ context.Context, command string, args ...string) (string, error) {
		if command != "dpkg-query" {
			t.Fatal(command)
		}
		if args[0] == "-S" {
			return "unifi-base:arm64: /usr/sbin/improxy\n", nil
		}
		return "5.1.33", nil
	})
	if item.Version != "0.3" || item.PackageVersion != "5.1.33" || item.Package != "unifi-base:arm64" {
		t.Fatalf("conflated package and binary versions: %+v", item)
	}
}

func TestQueryOutputIsBounded(t *testing.T) {
	var output limitedOutput
	data := strings.Repeat("x", queryOutputLimit*2)
	n, err := output.Write([]byte(data))
	if err != nil || n != len(data) || len(output.data) != queryOutputLimit {
		t.Fatal("unbounded query output")
	}
}
