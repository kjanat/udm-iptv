package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"golang.org/x/sys/unix"

	"github.com/kjanat/udm-iptv/internal/device"
)

type systemInfo struct {
	Errors    map[string]string `json:"errors,omitempty"`
	Board     string            `json:"board"`
	SysID     string            `json:"sysID"`
	Firmware  string            `json:"firmware"`
	Discovery string            `json:"discovery"`
	Version   string            `json:"rawVersion"`
	Kernel    string            `json:"kernel"`
}

// unitEvidence keeps the selected systemd properties in their original JSON
// shapes, including Job's (id, object path) tuple and absent service-only fields
// on targets. Missing properties are not invented as zero or success.
type unitEvidence struct {
	Name       string                     `json:"name"`
	Properties map[string]json.RawMessage `json:"properties"`
	Errors     map[string]string          `json:"errors,omitempty"`
}

func inspectSystem(root fs.FS, hardware device.Hardware) systemInfo {
	value := systemInfo{Board: hardware.Board, SysID: hardware.SysID, Firmware: hardware.Firmware, Discovery: hardware.Discovery}
	var kernel unix.Utsname
	if err := unix.Uname(&kernel); err == nil {
		value.Kernel = strings.Join([]string{unix.ByteSliceToString(kernel.Sysname[:]), unix.ByteSliceToString(kernel.Release[:]), unix.ByteSliceToString(kernel.Version[:]), unix.ByteSliceToString(kernel.Machine[:])}, " ")
	} else {
		recordCollectionError(&value.Errors, "kernel", err)
	}
	version, err := fs.ReadFile(root, "usr/lib/version")
	recordCollectionError(&value.Errors, "version", err)
	value.Version = string(version)
	return value
}

func collectSystemdEvidence(ctx context.Context, connection *systemd.Conn, status *serviceStatus) {
	state, err := connection.SystemStateContext(ctx)
	recordCollectionError(&status.Errors, "systemState", err)
	if err == nil {
		status.SystemState, _ = state.Value.Value().(string)
	}
	for _, name := range []string{serviceUnit, restoreUnit, "network.target", "network-online.target"} {
		unit := unitEvidence{Name: name, Properties: make(map[string]json.RawMessage)}
		properties, err := connection.GetAllPropertiesContext(ctx, name)
		recordCollectionError(&unit.Errors, "systemd", err)
		if err == nil {
			for _, key := range []string{"Id", "LoadState", "UnitFileState", "ActiveState", "SubState", "Result", "Job", "MainPID", "ExecMainStatus", "NRestarts"} {
				if value, exists := properties[key]; exists {
					data, err := json.Marshal(value)
					recordCollectionError(&unit.Errors, key, err)
					if err == nil {
						unit.Properties[key] = data
					}
				}
			}
		}
		status.Units = append(status.Units, unit)
	}
}

func (r reportRenderer) systemEvidence(value Snapshot) string {
	var output strings.Builder
	fmt.Fprintf(&output, "Board: %s, system ID: %s\nFirmware: %s\nFirmware discovery: %s\nRaw firmware version: %s\nKernel: %s\nSystem state: %s\n",
		fallbackText(value.System.Board), fallbackText(value.System.SysID), fallbackText(value.System.Firmware), fallbackText(value.System.Discovery), fallbackText(value.System.Version), fallbackText(value.System.Kernel), r.systemState(value.Service))
	output.WriteString(r.collectionErrors("system", value.System.Errors))
	for _, unit := range value.Service.Units {
		fmt.Fprintf(&output, "Unit %s:\n", unit.Name)
		for _, key := range slices.Sorted(maps.Keys(unit.Properties)) {
			fmt.Fprintf(&output, "  %s=%s\n", key, unit.Properties[key])
		}
		output.WriteString(r.collectionErrors(unit.Name, unit.Errors))
	}
	if value.ProxyConfig == nil {
		output.WriteString(r.field("Generated proxy configuration", r.text(ReportWarning, "unavailable")))
	} else {
		fmt.Fprintf(&output, "Generated proxy configuration:\n%s\n", *value.ProxyConfig)
	}
	return output.String()
}

func (r reportRenderer) systemState(value serviceStatus) string {
	role := ReportWarning
	if value.SystemState == "running" && value.Errors["systemState"] == "" && value.Errors["systemd"] == "" {
		role = ReportGood
	}
	return r.text(role, fallbackText(value.SystemState))
}
