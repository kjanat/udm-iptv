package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

const (
	testLogPartition = 976 * mebibyte
	testPlentyFree   = 500 * mebibyte
	testScarceFree   = 12 * mebibyte
	testBetweenFree  = 70 * mebibyte
)

var errTestNoSuchPath = errors.New("no such path")

type fakeSpace struct {
	free     map[string]uint64
	rotated  int
	rotation error
}

func (space *fakeSpace) measure(path string) (spaceSample, error) {
	free, ok := space.free[path]
	if !ok {
		return spaceSample{}, errTestNoSuchPath
	}

	return spaceSample{path: path, free: free, total: testLogPartition}, nil
}

func (space *fakeSpace) rotate(context.Context) error {
	space.rotated++

	return space.rotation
}

func newTestGuard(space *fakeSpace, notices *bytes.Buffer, paths ...string) *spaceGuard {
	return &spaceGuard{paths: paths, measure: space.measure, rotate: space.rotate, notices: notices}
}

func mustWrite(t *testing.T, writer io.Writer, text string) {
	t.Helper()
	if _, err := io.WriteString(writer, text); err != nil {
		t.Fatal(err)
	}
}

func TestSpaceGuardDropsProxyChatterWhileSpaceIsLow(t *testing.T) {
	t.Parallel()
	space := &fakeSpace{free: map[string]uint64{"/var/log": testPlentyFree, "/data/udm-iptv": testPlentyFree}}
	var notices, relayed bytes.Buffer
	guard := newTestGuard(space, &notices, "/var/log", "/data/udm-iptv")
	writer := guard.filter(&relayed)
	now := time.Now()
	guard.check(context.Background(), now)
	mustWrite(t, writer, "INFO[imp_verify_multicast_addr@251]: 239.1.1.1 ok\n")
	if notices.Len() != 0 {
		t.Fatalf("a healthy filesystem produced notices %q", notices.String())
	}
	space.free["/var/log"] = testScarceFree
	guard.check(context.Background(), now)
	mustWrite(t, writer, "DEBUG[main@653]: Data arrive\nINFO[imp_verify_multicast_addr@262]: 239.1.1.2 ok\nWARNING[mcast_recv_igmp@717]: bad checksum\nERROR[x@1]: broken\n")
	want := "INFO[imp_verify_multicast_addr@251]: 239.1.1.1 ok\nWARNING[mcast_recv_igmp@717]: bad checksum\nERROR[x@1]: broken\n"
	if relayed.String() != want {
		t.Fatalf("relayed while low:\n%q\nwant:\n%q", relayed.String(), want)
	}
	if !strings.Contains(notices.String(), "low space: /var/log has 12 MiB free of 976 MiB; proxy INFO and DEBUG lines are dropped") {
		t.Fatalf("trip notice missing:\n%s", notices.String())
	}
	space.free["/var/log"] = testPlentyFree
	notices.Reset()
	relayed.Reset()
	guard.check(context.Background(), now)
	mustWrite(t, writer, "DEBUG[main@653]: Data arrive\n")
	if !strings.Contains(notices.String(), "space recovered: /var/log has 500 MiB free of 976 MiB; proxy output resumes") || relayed.String() != "DEBUG[main@653]: Data arrive\n" {
		t.Fatalf("after recovery: notices %q, relayed %q", notices.String(), relayed.String())
	}
}

func TestSpaceGuardForcesRotationOncePerHourWhileLow(t *testing.T) {
	t.Parallel()
	space := &fakeSpace{free: map[string]uint64{"/var/log": testScarceFree}}
	var notices bytes.Buffer
	guard := newTestGuard(space, &notices, "/var/log")
	start := time.Date(2026, time.October, 9, 13, 0, 0, 0, time.UTC)
	for i, step := range []struct {
		at   time.Duration
		free uint64
		want int
	}{
		{0, testScarceFree, 1},
		{logRotateInterval / 2, testScarceFree, 1},
		{logRotateInterval + spaceCheckInterval, testScarceFree, 2},
		{2 * logRotateInterval, testPlentyFree, 2},
		{3 * logRotateInterval, testPlentyFree, 2},
	} {
		space.free["/var/log"] = step.free
		guard.check(context.Background(), start.Add(step.at))
		if space.rotated != step.want {
			t.Fatalf("step %d: rotations = %d, want %d", i, space.rotated, step.want)
		}
	}
	if !strings.Contains(notices.String(), "forced the system log rotation (logrotate --force /etc/logrotate.d/syslog-ng)") {
		t.Fatalf("rotation notice missing:\n%s", notices.String())
	}
}

func TestSpaceGuardHoldsItsStateBetweenTheThresholds(t *testing.T) {
	t.Parallel()
	space := &fakeSpace{free: map[string]uint64{"/var/log": testBetweenFree}}
	var notices bytes.Buffer
	guard := newTestGuard(space, &notices, "/var/log")
	guard.check(context.Background(), time.Now())
	if guard.low.Load() {
		t.Fatal("tripped above the low threshold")
	}
	space.free["/var/log"] = testScarceFree
	guard.check(context.Background(), time.Now())
	space.free["/var/log"] = testBetweenFree
	guard.check(context.Background(), time.Now())
	if !guard.low.Load() {
		t.Fatal("recovered below the recovery threshold")
	}
	if got := strings.Count(notices.String(), "low space: /var/log"); got != 1 {
		t.Fatalf("trip reported %d times:\n%s", got, notices.String())
	}
}

func TestSpaceGuardReportsAFailedRotationAndSkipsUnmeasurablePaths(t *testing.T) {
	t.Parallel()
	space := &fakeSpace{free: map[string]uint64{"/var/log": testScarceFree}, rotation: errTestNoSuchPath}
	var notices bytes.Buffer
	guard := newTestGuard(space, &notices, "/missing", "/var/log")
	guard.check(context.Background(), time.Now())
	if !guard.low.Load() || space.rotated != 1 {
		t.Fatalf("low=%v rotated=%d", guard.low.Load(), space.rotated)
	}
	if !strings.Contains(notices.String(), "forcing the system log rotation failed: no such path") {
		t.Fatalf("failure notice missing:\n%s", notices.String())
	}
	empty := newTestGuard(&fakeSpace{free: map[string]uint64{}}, &notices, "/missing")
	empty.check(context.Background(), time.Now())
	if empty.low.Load() {
		t.Fatal("tripped without a measurable path")
	}
}

func TestLineFilterJudgesWholeLines(t *testing.T) {
	t.Parallel()
	var relayed bytes.Buffer
	guard := newTestGuard(&fakeSpace{}, &bytes.Buffer{})
	writer := guard.filter(&relayed)
	for _, chunk := range []string{"INFO[a@1]: first ", "half\nDEBUG[b@2]: who", "le\n"} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if relayed.String() != "INFO[a@1]: first half\nDEBUG[b@2]: whole\n" {
		t.Fatalf("reassembled lines: %q", relayed.String())
	}
	guard.low.Store(true)
	relayed.Reset()
	for _, chunk := range []string{"DEBUG[c@3]: dropped as ", "a whole\nWARNING[d@4]: kept as ", "a whole\n"} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if relayed.String() != "WARNING[d@4]: kept as a whole\n" {
		t.Fatalf("filtered lines: %q", relayed.String())
	}
	if guard := (*spaceGuard)(nil); guard.filter(&relayed) != &relayed {
		t.Fatal("a nil guard wraps the writer")
	}
}

func TestLineFilterJudgesAnEndlessLineAsItStands(t *testing.T) {
	t.Parallel()
	var relayed bytes.Buffer
	guard := newTestGuard(&fakeSpace{}, &bytes.Buffer{})
	writer := guard.filter(&relayed)
	guard.low.Store(true)
	long := strings.Repeat("x", pendingLineLimit+1)
	if _, err := writer.Write([]byte(long)); err != nil {
		t.Fatal(err)
	}
	if relayed.Len() != 0 {
		t.Fatalf("an endless chatter line was relayed while low: %d bytes", relayed.Len())
	}
	guard.low.Store(false)
	if _, err := writer.Write([]byte("error without newline" + long)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(relayed.String(), "error without newline") {
		t.Fatalf("an endless line was held back: %d bytes", relayed.Len())
	}
}

func TestIsProxyProblemCoversIgmpproxyWording(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]bool{
		"FATAL[main@1]: no interfaces\n":                    true,
		"WARNING[mcast_recv_igmp@717]: odd packet\n":        true,
		"INFO[imp_verify_multicast_addr@251]: 239.1.1.1\n":  false,
		"DEBUG[main@653]: Data arrive\n":                    false,
		"The source address 10.0.0.1 for group 239.1.1.1\n": false,
		"Warn: Unable to set socket option\n":               true,
		"select() failure; Errno(4): Interrupted\n":         true,
	} {
		if got := isProxyProblem([]byte(line)); got != want {
			t.Errorf("isProxyProblem(%q) = %v, want %v", line, got, want)
		}
	}
}
