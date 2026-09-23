package firmware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

const firmwareBinary = "/data/udm-iptv/bin/udm-iptv"

type evidenceCommand struct {
	File string   `json:"file"`
	Args []string `json:"args"`
}

type evidenceResult struct {
	Command  evidenceCommand `json:"command"`
	Started  time.Time       `json:"started"`
	Finished time.Time       `json:"finished"`
	Error    string          `json:"error,omitempty"`
}

type evidenceRunner func(context.Context, []string) (stdout, stderr []byte, err error)

func firmwareEvidenceCommands(name string) []evidenceCommand {
	return []evidenceCommand{
		{"container.json", []string{"inspect", name}},
		{"console.log", []string{"logs", "--timestamps", name}},
		{"journal.jsonl", []string{"exec", name, "journalctl", "--no-pager", "--all", "--output=json"}},
		{"service.txt", []string{"exec", name, "systemctl", "show", "udm-iptv.service", "udm-iptv-restore.service", "--no-pager"}},
		{"jobs.txt", []string{"exec", name, "systemctl", "list-jobs", "--no-pager"}},
		{"status.json", []string{"exec", name, firmwareBinary, "status", "--json"}},
		{"snapshot.jsonl", []string{"exec", name, firmwareBinary, "diagnose", "--format", "jsonl"}},
		{"config.json", []string{"exec", name, "cat", "/data/udm-iptv/config.json"}},
		{"links.txt", []string{"exec", name, "ip", "-details", "-statistics", "link", "show"}},
		{"addresses.txt", []string{"exec", name, "ip", "address", "show"}},
		{"routes.txt", []string{"exec", name, "ip", "route", "show", "table", "all"}},
		{"nat.txt", []string{"exec", name, "iptables-save", "-c", "-t", "nat"}},
		{"diagnostics.tar", []string{"cp", name + ":/data/udm-iptv/diagnostics", "-"}},
		{"runtime.tar", []string{"cp", name + ":/run/udm-iptv", "-"}},
		{"dhcp.log", []string{"exec", name, "cat", "/run/udm-iptv-test/dnsmasq.log"}},
	}
}

// Keep command streams separate: docker cp emits binary archives on stdout,
// while stderr and a failing exit status still carry useful partial evidence.
// A missing source must not prevent subsequent collectors or container cleanup.
func collectFirmwareEvidence(ctx context.Context, directory, name string, run evidenceRunner) error {
	commands := firmwareEvidenceCommands(name)
	results := make([]evidenceResult, 0, len(commands))
	var failures error
	for _, command := range commands {
		result := evidenceResult{Command: command, Started: time.Now().UTC()}
		stdout, stderr, err := run(ctx, command.Args)
		result.Finished = time.Now().UTC()
		if err != nil {
			result.Error = err.Error()
			failures = errors.Join(failures, fmt.Errorf("collect %s: %w", command.File, err))
		}
		failures = errors.Join(failures,
			atomicfile.Write(filepath.Join(directory, command.File), stdout, filemode.PrivateFile),
			atomicfile.Write(filepath.Join(directory, command.File+".stderr"), stderr, filemode.PrivateFile))
		results = append(results, result)
	}
	manifest, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return errors.Join(failures, fmt.Errorf("encode evidence manifest: %w", err))
	}
	return errors.Join(failures, atomicfile.Write(filepath.Join(directory, "manifest.json"), manifest, filemode.PrivateFile))
}

func saveFirmwareCommand(directory string, result evidenceResult, output []byte) error {
	metadata, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode firmware command: %w", err)
	}
	return errors.Join(
		atomicfile.Write(filepath.Join(directory, "command.json"), metadata, filemode.PrivateFile),
		atomicfile.Write(filepath.Join(directory, "output.log"), output, filemode.PrivateFile))
}

var errEvidencePartial = errors.New("collector exited with partial output")

func TestFirmwareCommandRetainsRawOutputAndFailure(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	result := evidenceResult{Command: evidenceCommand{File: "output.log", Args: []string{"exec", "test", "apt-get", "install"}}, Error: errEvidencePartial.Error()}
	output := []byte{'a', 0, 255, '\n'}
	if err := saveFirmwareCommand(directory, result, output); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(directory, "output.log"))
	if err != nil || !bytes.Equal(actual, output) {
		t.Fatalf("raw command output changed: %v, %v", actual, err)
	}
	metadata, err := os.ReadFile(filepath.Join(directory, "command.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored evidenceResult
	if err := json.Unmarshal(metadata, &stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stored.Command.Args, result.Command.Args) || stored.Error != result.Error {
		t.Fatalf("command evidence lost arguments or failure: %+v", stored)
	}
}

func TestFirmwareEvidenceRetainsCompleteAndPartialOutput(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	journal := bytes.Repeat([]byte("journal entry with full metadata\n"), 10_000)
	archive := []byte{0, 255, 128, 10, 0}
	var called []string
	run := func(_ context.Context, args []string) ([]byte, []byte, error) {
		called = append(called, strings.Join(args, " "))
		if slices.Contains(args, "journalctl") {
			return journal, []byte("journal warning"), errEvidencePartial
		}
		if args[0] == "cp" {
			return archive, []byte("copy warning"), nil
		}
		return []byte("complete output\n"), nil, nil
	}
	err := collectFirmwareEvidence(t.Context(), directory, "test-container", run)
	if !errors.Is(err, errEvidencePartial) {
		t.Fatalf("partial collector error lost: %v", err)
	}
	for file, expected := range map[string][]byte{
		"journal.jsonl":        journal,
		"journal.jsonl.stderr": []byte("journal warning"),
		"diagnostics.tar":      archive,
		"runtime.tar":          archive,
	} {
		actual, readErr := os.ReadFile(filepath.Join(directory, file))
		if readErr != nil || !bytes.Equal(actual, expected) {
			t.Errorf("%s lost evidence: bytes=%d, error=%v", file, len(actual), readErr)
		}
	}
	assertEvidenceManifest(t, directory, called)
}

func assertEvidenceManifest(t *testing.T, directory string, called []string) {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var results []evidenceResult
	if err := json.Unmarshal(manifest, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != len(firmwareEvidenceCommands("test-container")) || len(called) != len(results) {
		t.Fatal("failed collector prevented remaining evidence collection")
	}
	var recordedError bool
	for _, result := range results {
		if result.Command.File == "journal.jsonl" {
			recordedError = result.Error == errEvidencePartial.Error()
		}
		if result.Started.IsZero() || result.Finished.Before(result.Started) {
			t.Errorf("invalid collector timestamps: %+v", result)
		}
	}
	if !recordedError {
		t.Fatal("manifest omitted the partial-output failure")
	}
}
