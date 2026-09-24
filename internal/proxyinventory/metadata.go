package proxyinventory

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/runtimebundle"
)

const (
	queryTimeout       = time.Second
	queryOutputLimit   = 16 << 10
	elfNoteLimit       = 4096
	elfStringLimit     = 1 << 20
	elfNoteHeaderSize  = 12
	elfNotePaddingMask = 3
)

// Feature distinguishes family-level support from a capability observed in
// this build. Unknown is never interpreted as absent or verified support.
type Feature struct {
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

func familyFeatures(program string) map[string]Feature {
	ipv6 := Feature{Status: unknownStatus, Evidence: "build capability not queried"}
	if program == config.ProxyIgmpproxy {
		ipv6 = Feature{Status: "unsupported", Evidence: "igmpproxy is IPv4-only"}
	}
	return map[string]Feature{
		"ipv4_multicast":        {Status: "supported", Evidence: "program family"},
		"ipv6_multicast":        ipv6,
		"ipv4_querier_election": {Status: unknownStatus, Evidence: "not established by executable presence or version"},
	}
}

// Inspect augments discovery using ELF notes, package ownership and documented
// information-only flags. Neither command loads a proxy configuration: improxy
// -v prints its version and exits (normally with status 1); igmpproxy -h prints
// help and its version. igmpproxy -v means verbosity and must not be used here.
func Inspect(ctx context.Context, stateDir string) Inventory {
	ctx, cancel := context.WithTimeout(ctx, 2*queryTimeout)
	defer cancel()
	inventory := Discover(stateDir)
	for index := range inventory {
		item := &inventory[index]
		if !item.Available {
			continue
		}
		if inspectELF(item) {
			inspectVersion(ctx, stateDir, item, query)
		} else {
			item.MetadataErrors = append(item.MetadataErrors, "version query unavailable: information-only flag not identified in binary")
		}
		if item.Source == "system" {
			inspectPackage(ctx, item, query)
		}
	}
	return inventory
}

type queryCommand func(context.Context, string, ...string) (string, error)

type limitedOutput struct{ data []byte }

func (output *limitedOutput) Write(data []byte) (int, error) {
	remaining := queryOutputLimit - len(output.data)
	output.data = append(output.data, data[:min(len(data), remaining)]...)
	return len(data), nil
}

func query(ctx context.Context, binary string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.WaitDelay = queryTimeout
	var output limitedOutput
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	return string(output.data), err
}

var (
	versionPattern  = regexp.MustCompile(`(?im)^(?:version:\s*|igmpproxy\s+)([0-9][^\r\n]*)$`)
	revisionPattern = regexp.MustCompile(`(?i)\b(?:git(?: revision| commit)?|commit|revision|vcs\.revision)[ :=]+([0-9a-f]{7,40})\b`)
)

func inspectVersion(ctx context.Context, stateDir string, item *Proxy, run queryCommand) {
	flag := "-h"
	if item.Name == config.ProxyImproxy {
		flag = "-v"
	}
	binary, args := item.Path, []string{flag}
	if item.Source == "offline" {
		var err error
		binary, args, err = runtimebundle.Command(stateDir, item.Name, args)
		if err != nil {
			item.MetadataErrors = append(item.MetadataErrors, err.Error())
			return
		}
	}
	output, err := run(ctx, binary, args...)
	parseVersionOutput(item, output, err)
	if revision := revisionPattern.FindStringSubmatch(output); len(revision) > 1 {
		item.Revision = revision[1]
	}
}

func parseVersionOutput(item *Proxy, output string, err error) {
	matches := versionPattern.FindStringSubmatch(output)
	var exit *exec.ExitError
	versionExit := item.Name == config.ProxyImproxy && errors.As(err, &exit) && exit.ExitCode() == 1
	if len(matches) > 1 && (err == nil || versionExit) {
		item.Version, item.VersionSource = strings.TrimSpace(matches[1]), "version-command"
		if item.Name == config.ProxyIgmpproxy {
			item.VersionSource = "help-output"
		}
	} else if err != nil {
		item.MetadataErrors = append(item.MetadataErrors, "version query: "+err.Error())
	}
}

func inspectPackage(ctx context.Context, item *Proxy, run queryCommand) {
	output, err := run(ctx, "dpkg-query", "-S", item.Path)
	if err != nil {
		return // Standalone and preserved binaries need not belong to a package.
	}
	owner, _, found := strings.Cut(strings.SplitN(output, "\n", 2)[0], ": ")
	if !found || strings.ContainsAny(owner, " ,\t\r\n") {
		return
	}
	version, err := run(ctx, "dpkg-query", "-W", "-f=${Version}", owner)
	if err == nil {
		item.Package, item.PackageVersion = owner, strings.TrimSpace(version)
	}
}

func inspectELF(item *Proxy) bool {
	binary, err := elf.Open(item.Path)
	if err != nil {
		return false
	}
	defer func() { _ = binary.Close() }()
	item.Architecture = binary.Machine.String()
	if section := binary.Section(".note.gnu.build-id"); section != nil && section.Size <= elfNoteLimit {
		data, err := io.ReadAll(io.LimitReader(section.Open(), elfNoteLimit))
		if err == nil {
			item.BuildID = parseBuildID(data, binary.ByteOrder.Uint32)
		}
	}
	// Named compiled functions establish compiled MLD code, not conformance or
	// successful IPv6 forwarding on this router. Stripped builds stay unknown.
	symbols := boundedSymbols(binary)
	for _, symbol := range symbols {
		if symbol.Name == "mcast_recv_mld" && symbol.Section != elf.SHN_UNDEF {
			item.Features["ipv6_multicast"] = Feature{Status: "compiled", Evidence: "ELF symbol mcast_recv_mld"}
		}
	}
	return hasInformationFlag(binary, item.Name)
}

func boundedSymbols(binary *elf.File) []elf.Symbol {
	for _, name := range []string{".symtab", ".strtab"} {
		section := binary.Section(name)
		if section == nil || section.Size > elfStringLimit {
			return nil
		}
	}
	symbols, _ := binary.Symbols()
	return symbols
}

func hasInformationFlag(binary *elf.File, program string) bool {
	section := binary.Section(".rodata")
	if section == nil || section.Size > elfStringLimit {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(section.Open(), elfStringLimit))
	if err != nil {
		return false
	}
	if program == config.ProxyImproxy {
		return bytes.Contains(data, []byte("-v show version"))
	}
	return bytes.Contains(data, []byte("-h   Display this help screen"))
}

func parseBuildID(data []byte, uint32Value func([]byte) uint32) string {
	if len(data) < elfNoteHeaderSize {
		return ""
	}
	nameSize, valueSize, kind := uint64(uint32Value(data)), uint64(uint32Value(data[4:])), uint32Value(data[8:])
	start := uint64(elfNoteHeaderSize) + ((nameSize + elfNotePaddingMask) &^ uint64(elfNotePaddingMask))
	if kind != 3 || nameSize != 4 || start+valueSize > uint64(len(data)) || string(data[12:16]) != "GNU\x00" {
		return ""
	}
	return hex.EncodeToString(data[start : start+valueSize])
}

// Description includes observed metadata without confusing an ELF build ID or
// packaging version with a source-control revision.
func (item Proxy) Description() string {
	parts := []string{item.Name + ": " + item.Source}
	for _, field := range [][2]string{{"version", item.Version}, {"package", item.Package}, {"package version", item.PackageVersion}, {"VCS revision", item.Revision}, {"ELF build ID", item.BuildID}, {"architecture", item.Architecture}} {
		if field[1] != "" {
			parts = append(parts, fmt.Sprintf("%s %s", field[0], field[1]))
		}
	}
	return strings.Join(parts, "; ")
}
