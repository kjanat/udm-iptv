package service

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n, _ := b.buffer.Write(p)

	return n, nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.String()
}

func TestTerminalCarriesOutputLineByLineUntilTheChildExits(t *testing.T) {
	t.Parallel()
	var out lockedBuffer
	command := exec.CommandContext(t.Context(), "sh", "-c", "printf 'one\\n'; printf 'two\\n'; exit 3")
	output, err := attachTerminal(command, &out)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := output.started(); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := command.Wait(); err == nil || !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("wait returned %v", err)
	}
	<-output.done
	if got := out.String(); got != "one\ntwo\n" {
		t.Fatalf("output %q", got)
	}
	if err := output.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalFinishesReadingBeforeFlushing(t *testing.T) {
	t.Parallel()
	var out lockedBuffer
	command := exec.CommandContext(t.Context(), "sh", "-c", "printf 'trailing evidence'")
	output, err := attachTerminal(command, &out)
	if err != nil {
		t.Fatal(err)
	}
	var flushed string
	output.flush = func() { flushed = out.String() }
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := output.started(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := output.finish(); err != nil {
		t.Fatal(err)
	}
	if flushed != "trailing evidence" {
		t.Fatalf("flush overtook output: %q", flushed)
	}
}

func TestTerminalKeepsNewlinesRaw(t *testing.T) {
	t.Parallel()
	term, err := openTerminal()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = term.master.Close() }()
	if _, err := term.slave.WriteString("a\nb\n"); err != nil {
		t.Fatal(err)
	}
	if err := term.slave.Close(); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	n, err := term.master.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buffer[:n]); strings.Contains(got, "\r") || got != "a\nb\n" {
		t.Fatalf("read %q", got)
	}
}
