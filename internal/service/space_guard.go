package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// The guard watches the log partition, where syslog-ng keeps daemon.log for
// UniFi OS, and the state directory on the console overlay.
const (
	systemLogDir       = "/var/log"
	logRotateConfig    = "/etc/logrotate.d/syslog-ng"
	spaceCheckInterval = 30 * time.Second
	// Free space under either low value trips the guard; above both
	// recover values it clears. The gap keeps the state steady at the limit.
	spaceLowFraction     = 0.05
	spaceLowBytes        = 32 * mebibyte
	spaceRecoverFraction = 0.10
	spaceRecoverBytes    = 64 * mebibyte
	// logRotateInterval bounds how often a tripped guard forces rotation.
	logRotateInterval = time.Hour
	logRotateTimeout  = 2 * time.Minute
	// pendingLineLimit caps a line held back while waiting for its newline.
	pendingLineLimit = 4096
	mebibyte         = 1 << 20
)

var errBlockSize = errors.New("the filesystem reports no block size")

type spaceSample struct {
	path        string
	free, total uint64
}

func (sample spaceSample) fraction() float64 {
	if sample.total == 0 {
		return 1
	}

	return float64(sample.free) / float64(sample.total)
}

func (sample spaceSample) low() bool {
	return sample.free < spaceLowBytes || sample.fraction() < spaceLowFraction
}

func (sample spaceSample) recovered() bool {
	return sample.free >= spaceRecoverBytes && sample.fraction() >= spaceRecoverFraction
}

func (sample spaceSample) String() string {
	return fmt.Sprintf("%s has %d MiB free of %d MiB", sample.path, sample.free/mebibyte, sample.total/mebibyte)
}

// spaceGuard drops the proxy's INFO and DEBUG lines while a watched
// filesystem runs short of space and forces the system's log rotation at most
// once an hour for as long as it stays short. Each change of state is
// reported once.
type spaceGuard struct {
	paths   []string
	measure func(path string) (spaceSample, error)
	rotate  func(ctx context.Context) error
	notices io.Writer
	low     atomic.Bool
	rotated time.Time
}

func newSpaceGuard(notices io.Writer, paths ...string) *spaceGuard {
	return &spaceGuard{paths: paths, measure: measureSpace, rotate: forceLogRotation, notices: notices}
}

func measureSpace(path string) (spaceSample, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return spaceSample{}, fmt.Errorf("measure free space on %s: %w", path, err)
	}
	if stat.Bsize <= 0 {
		return spaceSample{}, fmt.Errorf("measure free space on %s: %w (%d)", path, errBlockSize, stat.Bsize)
	}
	block := uint64(stat.Bsize)

	return spaceSample{path: path, free: stat.Bavail * block, total: stat.Blocks * block}, nil
}

// forceLogRotation runs the daily UniFi OS rotation of the syslog-ng files.
// daemon.log is one of them.
func forceLogRotation(ctx context.Context) error {
	if _, err := os.Stat(logRotateConfig); err != nil {
		return fmt.Errorf("find the system log rotation: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, logRotateTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "logrotate", "--force", logRotateConfig).CombinedOutput()
	if err != nil {
		return fmt.Errorf("logrotate --force %s: %w: %s", logRotateConfig, err, strings.TrimSpace(string(output)))
	}

	return nil
}

// run checks the watched filesystems until ctx ends.
func (guard *spaceGuard) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		guard.check(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// check measures every watched path, moves the guard between its states and
// forces rotation while space stays short. A path that cannot be measured is
// left out.
func (guard *spaceGuard) check(ctx context.Context, now time.Time) {
	samples := guard.samples()
	if len(samples) == 0 {
		return
	}
	guard.transition(samples)
	guard.maybeRotate(ctx, now)
}

// transition moves the guard between its states and reports each change once.
func (guard *spaceGuard) transition(samples []spaceSample) {
	tightest := tightestOf(samples)
	switch {
	case !guard.low.Load() && anyLow(samples):
		guard.low.Store(true)
		guard.noticef("low space: %s; proxy INFO and DEBUG lines are dropped until it recovers", tightest)
	case guard.low.Load() && allRecovered(samples):
		guard.low.Store(false)
		guard.noticef("space recovered: %s; proxy output resumes", tightest)
	}
}

// maybeRotate forces the system log rotation once an hour while space stays short.
func (guard *spaceGuard) maybeRotate(ctx context.Context, now time.Time) {
	if !guard.low.Load() || guard.rotate == nil || now.Sub(guard.rotated) < logRotateInterval {
		return
	}
	guard.rotated = now
	if err := guard.rotate(ctx); err != nil {
		guard.noticef("low space: forcing the system log rotation failed: %v", err)

		return
	}
	guard.noticef("low space: forced the system log rotation (logrotate --force %s)", logRotateConfig)
}

func tightestOf(samples []spaceSample) spaceSample {
	tightest := samples[0]
	for _, sample := range samples[1:] {
		if sample.fraction() < tightest.fraction() {
			tightest = sample
		}
	}

	return tightest
}

func (guard *spaceGuard) samples() []spaceSample {
	samples := make([]spaceSample, 0, len(guard.paths))
	for _, path := range guard.paths {
		sample, err := guard.measure(path)
		if err != nil {
			continue
		}
		samples = append(samples, sample)
	}

	return samples
}

func anyLow(samples []spaceSample) bool {
	for _, sample := range samples {
		if sample.low() {
			return true
		}
	}

	return false
}

func allRecovered(samples []spaceSample) bool {
	for _, sample := range samples {
		if !sample.recovered() {
			return false
		}
	}

	return true
}

func (guard *spaceGuard) noticef(format string, arguments ...any) {
	_, _ = fmt.Fprintf(guard.notices, format+"\n", arguments...)
}

// filter returns a writer that passes lines through while space is fine and
// keeps only the proxy's warnings and errors while it is short. A line is
// judged whole, so a chunk that ends mid-line waits for its newline.
func (guard *spaceGuard) filter(next io.Writer) io.Writer {
	if guard == nil {
		return next
	}

	return &lineFilter{next: next, keep: guard.keep}
}

func (guard *spaceGuard) keep(line []byte) bool {
	return !guard.low.Load() || isProxyProblem(line)
}

// isProxyProblem recognises improxy's FATAL, ERROR and WARNING prefixes and
// any line that names an error, warning or failure in words, which covers
// igmpproxy.
func isProxyProblem(line []byte) bool {
	for _, prefix := range []string{"FATAL[", "ERROR[", "WARNING["} {
		if bytes.HasPrefix(line, []byte(prefix)) {
			return true
		}
	}
	lower := bytes.ToLower(line)
	for _, word := range []string{"error", "warn", "fail", "fatal"} {
		if bytes.Contains(lower, []byte(word)) {
			return true
		}
	}

	return false
}

// lineFilter hands complete lines to keep and forwards the accepted ones.
type lineFilter struct {
	next    io.Writer
	keep    func(line []byte) bool
	mu      sync.Mutex
	pending []byte
}

func (filter *lineFilter) Write(data []byte) (int, error) {
	filter.mu.Lock()
	defer filter.mu.Unlock()
	filter.pending = append(filter.pending, data...)
	for {
		end := bytes.IndexByte(filter.pending, '\n')
		if end < 0 && len(filter.pending) <= pendingLineLimit {
			break
		}
		if end < 0 {
			end = len(filter.pending) - 1
		}
		line := filter.pending[:end+1]
		keep := filter.keep(line)
		filter.pending = filter.pending[end+1:]
		if keep {
			if _, err := filter.next.Write(line); err != nil {
				return len(data), fmt.Errorf("relay proxy output: %w", err)
			}
		}
	}
	if len(filter.pending) == 0 {
		filter.pending = nil
	}

	return len(data), nil
}
