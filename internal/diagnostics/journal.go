package diagnostics

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	journalCursorLimit = 64 << 10
	journalOutputLimit = 4 << 20
	// failureLogLines is how many recent lines a failure report attaches.
	failureLogLines = 100
)

type boundedJournal struct {
	buf   []byte
	limit int
}

func (output *boundedJournal) Write(data []byte) (int, error) {
	if output.limit <= 0 {
		output.buf = output.buf[:0]
		return len(data), nil
	}
	if len(data) >= output.limit {
		output.buf = append(output.buf[:0], data[len(data)-output.limit:]...)
		return len(data), nil
	}
	total := len(output.buf) + len(data)
	if total > output.limit {
		output.buf = append(output.buf[total-output.limit:], data...)
		return len(data), nil
	}
	output.buf = append(output.buf, data...)
	return len(data), nil
}

func (output *boundedJournal) Bytes() []byte {
	return output.buf
}

func (output *boundedJournal) String() string {
	return string(output.buf)
}

func journalOutput(ctx context.Context, limit int, arguments ...string) ([]byte, error) {
	output := &boundedJournal{limit: limit}
	command := exec.CommandContext(ctx, "journalctl", arguments...)
	command.Stdout = output
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("read bounded journal output: %w", err)
	}
	return output.Bytes(), nil
}

func journalCursor(ctx context.Context) string {
	output, err := journalOutput(ctx, journalCursorLimit, "-n", "0", "--show-cursor", "--no-pager", "-u", "udm-iptv.service")
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(output), "\n") {
		if cursor, found := strings.CutPrefix(strings.TrimSpace(line), "-- cursor: "); found {
			return cursor
		}
	}

	return ""
}

func journalLines(ctx context.Context, cursor string, limit int) []string {
	if cursor == "" {
		return []string{"journal cursor unavailable; service logs were not collected"}
	}
	arguments := journalArguments(cursor, limit)
	output, err := journalOutput(ctx, journalOutputLimit, arguments...)
	if err != nil {
		return []string{"journal unavailable: " + err.Error()}
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	return lines
}

func journalArguments(cursor string, limit int) []string {
	return []string{"--no-pager", "-o", "cat", "-n", strconv.Itoa(limit), "-u", "udm-iptv.service", "--after-cursor", cursor}
}
