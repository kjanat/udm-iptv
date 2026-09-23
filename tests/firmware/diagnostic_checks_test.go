package firmware_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/diagnostics"
)

type diagnosticExpectation struct {
	board, firmware, rawVersion, kernel, proxyConfig string
}

var errIncompleteFirmwareDiagnostics = errors.New("incomplete firmware diagnostics")

func checkFirmwareSnapshot(snapshot diagnostics.Snapshot, expected diagnosticExpectation) error {
	return errors.Join(checkFirmwareIdentity(snapshot, expected), checkRunningUnit(snapshot), checkSelectedNetwork(snapshot), checkFirmwareTables(snapshot))
}

func checkFirmwareIdentity(snapshot diagnostics.Snapshot, expected diagnosticExpectation) error {
	values := []struct{ name, got, want string }{
		{"firmware", snapshot.System.Firmware, expected.firmware},
		{"raw firmware", snapshot.System.Version, expected.rawVersion},
		{"kernel", snapshot.System.Kernel, expected.kernel},
	}
	// Extracted images may lack the hardware-generated board files. Assert
	// exact board identity only when a real source exists in this container.
	if expected.board != "" {
		values = append(values, struct{ name, got, want string }{"board", snapshot.System.Board, expected.board})
	}
	for _, value := range values {
		if value.want == "" || value.got != value.want {
			return fmt.Errorf("%w: %s = %q, want %q", errIncompleteFirmwareDiagnostics, value.name, value.got, value.want)
		}
	}
	if snapshot.Timestamp.IsZero() || snapshot.Version == "" {
		return fmt.Errorf("%w: timestamp or executable version missing", errIncompleteFirmwareDiagnostics)
	}
	if snapshot.ProxyConfig == nil || *snapshot.ProxyConfig != expected.proxyConfig || expected.proxyConfig == "" {
		return fmt.Errorf("%w: generated proxy configuration differs from the actual file", errIncompleteFirmwareDiagnostics)
	}
	return nil
}

func checkRunningUnit(snapshot diagnostics.Snapshot) error {
	service := snapshot.Service
	if service.LoadState != "loaded" || service.ActiveState != "active" || service.SubState != "running" || service.UnitFile != "enabled" || service.Proxy != "improxy" || service.ProxyPID <= 0 {
		return fmt.Errorf("%w: running service summary = %+v", errIncompleteFirmwareDiagnostics, service)
	}
	for _, unit := range service.Units {
		if unit.Name != "udm-iptv.service" {
			continue
		}
		return checkRunningUnitProperties(unit.Properties, service.Restarts)
	}
	return fmt.Errorf("%w: own service unit properties missing", errIncompleteFirmwareDiagnostics)
}

func checkRunningUnitProperties(properties map[string]json.RawMessage, restarts uint64) error {
	for name, expected := range map[string]string{
		"Id": `"udm-iptv.service"`, "LoadState": `"loaded"`, "ActiveState": `"active"`, "SubState": `"running"`,
		"Result": `"success"`, "ExecMainStatus": "0", "NRestarts": strconv.FormatUint(restarts, 10),
	} {
		if actual := string(properties[name]); actual != expected {
			return fmt.Errorf("%w: %s = %s, want %s", errIncompleteFirmwareDiagnostics, name, actual, expected)
		}
	}
	var pid uint64
	if err := json.Unmarshal(properties["MainPID"], &pid); err != nil {
		return fmt.Errorf("%w: decode running unit MainPID: %w", errIncompleteFirmwareDiagnostics, err)
	}
	if pid == 0 {
		return fmt.Errorf("%w: running unit MainPID is zero", errIncompleteFirmwareDiagnostics)
	}
	return nil
}

func checkSelectedNetwork(snapshot diagnostics.Snapshot) error {
	if snapshot.Network.Target == "" || len(snapshot.Network.Addresses) == 0 || snapshot.Config.Proxy != "improxy" || len(snapshot.Config.NATDestinations) == 0 || !slices.Contains(snapshot.Config.LANInterfaces, "br0") {
		return fmt.Errorf("%w: selected configuration or active network missing", errIncompleteFirmwareDiagnostics)
	}
	return nil
}

func checkFirmwareTables(snapshot diagnostics.Snapshot) error {
	if snapshot.Multicast == nil || snapshot.Memberships == nil || snapshot.NAT == nil || snapshot.NATEvidence == nil {
		return fmt.Errorf("%w: multicast, membership or NAT tables unavailable", errIncompleteFirmwareDiagnostics)
	}
	for _, destination := range snapshot.Config.NATDestinations {
		if err := checkNATDestination(snapshot, destination); err != nil {
			return err
		}
	}
	return nil
}

func checkNATDestination(snapshot diagnostics.Snapshot, destination string) error {
	managed := false
	for _, rule := range *snapshot.NAT {
		managed = managed || rule.Managed && rule.Destination == destination
	}
	if !managed || !slices.ContainsFunc(*snapshot.NATEvidence, func(entry diagnostics.NATEvidence) bool { return entry.Destination == destination }) {
		return fmt.Errorf("%w: NAT rule/evidence missing for %s", errIncompleteFirmwareDiagnostics, destination)
	}
	return nil
}

func checkFailureReport(report string) error {
	for _, detail := range []string{
		"=== udm-iptv failure diagnostics ===", "Unit udm-iptv.service:", `Result="exit-code"`, "ExecMainStatus=1", "MainPID=",
		"--- recent service logs (current boot;", "status=1/FAILURE",
	} {
		if !strings.Contains(report, detail) {
			return fmt.Errorf("%w: forced /bin/false report lacks %q", errIncompleteFirmwareDiagnostics, detail)
		}
	}
	return nil
}

func checkCaptureText(report string, expected diagnosticExpectation) error {
	for _, detail := range []string{
		"Firmware: " + expected.firmware, "Kernel: " + expected.kernel,
		"Unit udm-iptv.service:", `Result="success"`, "ExecMainStatus=0", "MainPID=",
		"Generated proxy configuration:\n" + expected.proxyConfig,
		"Active NAT rules:", "Multicast routes:",
	} {
		if !strings.Contains(report, detail) {
			return fmt.Errorf("%w: text capture lacks %q", errIncompleteFirmwareDiagnostics, detail)
		}
	}
	return nil
}

func diagnosticFixture(t *testing.T) (diagnostics.Snapshot, diagnosticExpectation) {
	t.Helper()
	const data = `{
"timestamp":"2026-09-23T10:00:00Z","version":"5.0.0",
"system":{"board":"UDMPRO","firmware":"6.0.7","rawVersion":"UDMPRO.al324.v6.0.7.abcdef0.260901.1200\n","kernel":"Linux 6.8.0 test aarch64"},
"proxyConfig":"upstream iptv\ndownstream br0\n",
"config":{"proxy":"improxy","natDestinations":["213.75.0.0/16"],"lanInterfaces":["br0"]},
"service":{"loadState":"loaded","activeState":"active","subState":"running","unitFileState":"enabled","proxy":"improxy","proxyPID":124,"restarts":0,
"units":[{"name":"udm-iptv.service","properties":{"Id":"udm-iptv.service","LoadState":"loaded","ActiveState":"active","SubState":"running","Result":"success","MainPID":123,"ExecMainStatus":0,"NRestarts":0}}]},
"network":{"target":"iptv","addresses":["198.51.100.2/24"]},"multicast":{"routes":0,"packets":0,"bytes":0},"memberships":[],
"natRules":[{"destination":"213.75.0.0/16","managed":true}],"natEvidence":[{"destination":"213.75.0.0/16"}]}`
	var snapshot diagnostics.Snapshot
	if err := json.Unmarshal([]byte(data), &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot, diagnosticExpectation{"UDMPRO", "6.0.7", "UDMPRO.al324.v6.0.7.abcdef0.260901.1200\n", "Linux 6.8.0 test aarch64", "upstream iptv\ndownstream br0\n"}
}

func TestFirmwareSnapshotRequiresActualEvidence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*diagnostics.Snapshot)
	}{
		{"board", func(value *diagnostics.Snapshot) { value.System.Board = "" }},
		{"firmware", func(value *diagnostics.Snapshot) { value.System.Firmware = "5.1.33" }},
		{"kernel", func(value *diagnostics.Snapshot) { value.System.Kernel = "" }},
		{"proxy", func(value *diagnostics.Snapshot) { value.ProxyConfig = nil }},
		{"unit", func(value *diagnostics.Snapshot) { value.Service.Units = nil }},
		{"exit", func(value *diagnostics.Snapshot) { delete(value.Service.Units[0].Properties, "ExecMainStatus") }},
		{"pid", func(value *diagnostics.Snapshot) { value.Service.Units[0].Properties["MainPID"] = json.RawMessage("0") }},
		{"nat", func(value *diagnostics.Snapshot) { value.NAT = nil }},
		{"nat evidence", func(value *diagnostics.Snapshot) { value.NATEvidence = nil }},
		{"multicast", func(value *diagnostics.Snapshot) { value.Multicast = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, expected := diagnosticFixture(t)
			if err := checkFirmwareSnapshot(value, expected); err != nil {
				t.Fatal(err)
			}
			test.change(&value)
			if err := checkFirmwareSnapshot(value, expected); !errors.Is(err, errIncompleteFirmwareDiagnostics) {
				t.Fatalf("missing %s was accepted: %v", test.name, err)
			}
		})
	}
	value, expected := diagnosticFixture(t)
	value.System.Board, expected.board = "", ""
	if err := checkFirmwareSnapshot(value, expected); err != nil {
		t.Fatalf("absent hardware-generated board rejected: %v", err)
	}
}

func TestFirmwareFailureReportRequiresConcreteFailure(t *testing.T) {
	t.Parallel()
	const report = "=== udm-iptv failure diagnostics ===\nUnit udm-iptv.service:\nResult=\"exit-code\"\nExecMainStatus=1\nMainPID=0\n--- recent service logs (current boot; last 100 records) ---\nMain process exited, code=exited, status=1/FAILURE\n"
	if err := checkFailureReport(report); err != nil {
		t.Fatal(err)
	}
	if err := checkFailureReport(strings.ReplaceAll(report, "ExecMainStatus=1", "ExecMainStatus=0")); !errors.Is(err, errIncompleteFirmwareDiagnostics) {
		t.Fatalf("incorrect exit status accepted: %v", err)
	}
}
