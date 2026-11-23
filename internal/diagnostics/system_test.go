package diagnostics

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/device"
)

func TestSystemEvidenceRetainsFirmwareConfigAndServiceOutcome(t *testing.T) {
	t.Parallel()
	const rawVersion = "UDMPRO.al324.v6.0.7.abcdef0.260901.1234\n"
	hardware := device.Hardware{Board: "UDMPRO", SysID: "ea15", Firmware: "6.0.7", Discovery: strings.TrimSpace(rawVersion)}
	system := inspectSystem(fstest.MapFS{"usr/lib/version": {Data: []byte(rawVersion)}}, hardware)
	if system.Version != rawVersion || system.Kernel == "" || len(system.Errors) != 0 {
		t.Fatalf("system evidence lost: %+v", system)
	}
	generated := "upstream eth9.4\ndownstream br10\n"
	value := Snapshot{System: system, ProxyConfig: &generated, Service: serviceStatus{SystemState: "degraded", Units: []unitEvidence{{
		Name: restoreUnit,
		Properties: map[string]json.RawMessage{
			"Result":         json.RawMessage(`"exit-code"`),
			"ExecMainStatus": json.RawMessage(`23`),
			"MainPID":        json.RawMessage(`7201`),
			"Job":            json.RawMessage(`[42,"/org/freedesktop/systemd1/job/42"]`),
		},
	}}}}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(data), RenderSnapshot(value)} {
		assertDiagnosticDetails(t, text, "UDMPRO", "ea15", "6.0.7", "abcdef0", "eth9.4", "br10", "degraded", restoreUnit, "exit-code", "ExecMainStatus", "23", "7201", "/org/freedesktop/systemd1/job/42")
	}
	assertDiagnosticDetails(t, RenderSnapshot(value), generated, system.Kernel)
}

func TestUnreadableSystemEvidenceKeepsError(t *testing.T) {
	t.Parallel()
	system := inspectSystem(deniedSysfs{}, device.Hardware{})
	if system.Errors["version"] == "" {
		t.Fatal("lost firmware read error")
	}
	value := Snapshot{System: system, Service: serviceStatus{Units: []unitEvidence{{Name: restoreUnit, Errors: map[string]string{"systemd": "D-Bus access denied"}}}}}
	assertDiagnosticDetails(t, RenderSnapshot(value), "permission denied", "usr/lib/version", "D-Bus access denied", "Generated proxy configuration: unavailable")
}

func TestRecentJournalRequestsBothUnitsAndPreservesPartialEvidence(t *testing.T) {
	installJournalScript(t, `
boot=0
main=0
restore=0
for arg do
  case "$arg" in
    -b) boot=1 ;;
    udm-iptv.service) main=1 ;;
    udm-iptv-restore.service) restore=1 ;;
  esac
done
if [ "$boot$main$restore" != 111 ]; then
  printf '%s\n' 'wrong journal selection' >&2
  exit 1
fi
printf '%s\n' '{"MESSAGE":"restored 192.0.2.7","_SYSTEMD_UNIT":"udm-iptv-restore.service","_PID":"55"}'
printf '%s\n' 'journal read interrupted' >&2
exit 7
`)
	logs, err := collectRecentJournal(t.Context(), failureLogLines)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("partial journal lost command failure: %v", err)
	}
	if len(logs.Events) != 1 || logs.Events[0].Journal["_SYSTEMD_UNIT"] == nil {
		t.Fatalf("lost original journal fields: %+v", logs)
	}
	data, marshalErr := json.Marshal(logs)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, text := range []string{string(data), renderRecentJournal(&logs)} {
		assertDiagnosticDetails(t, text, "restored 192.0.2.7", restoreUnit, "55", "journal read interrupted")
	}
}

func TestOneShotReportIncludesLogsAndCollectionErrors(t *testing.T) {
	installJournalScript(t, "printf '%s\\n' '"+liveJournalRecord+"'\nprintf '%s\\n' 'journal interrupted' >&2\nexit 7\n")
	path := filepath.Join(t.TempDir(), "config.json")
	settings := config.DefaultKPN()
	settings.WAN.Interface = "test-wan"
	settings.LAN.Interfaces = []string{"br0"}
	if err := config.Save(path, settings); err != nil {
		t.Fatal(err)
	}
	value, err := (&Collector{ConfigPath: path}).ReportSnapshot(t.Context(), "normal")
	if err != nil {
		t.Fatal(err)
	}
	if value.RecentLogs == nil || len(value.RecentLogs.Events) != 1 || value.Errors["journal"] == "" {
		t.Fatal("one-shot report lost journal evidence or collection error")
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(data), RenderSnapshot(value)} {
		assertDiagnosticDetails(t, text, "joined multicast group", "journal interrupted", "exit status 7")
	}
}
