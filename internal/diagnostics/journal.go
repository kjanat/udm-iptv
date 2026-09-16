package diagnostics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type boundedJournal struct {
	bytes.Buffer
	limit int
}

func (output *boundedJournal) Write(data []byte) (int, error) {
	remaining := output.limit - output.Len()
	if len(data) > remaining {
		n, _ := output.Buffer.Write(data[:remaining])
		return n, io.ErrShortBuffer
	}
	return output.Buffer.Write(data)
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
	output, err := journalOutput(ctx, 64<<10, "-n", "0", "--show-cursor", "--no-pager", "-u", "udm-iptv.service")
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
	output, err := journalOutput(ctx, 4<<20, arguments...)
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
