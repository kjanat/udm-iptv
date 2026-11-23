package telemetry

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kjanat/udm-iptv/internal/filemode"
)

const (
	// rateRecordLimit bounds the read of a rate file's four-integer record.
	rateRecordLimit  = 128
	secondsPerMinute = 60
	secondsPerDay    = 86400
	// dailyBudgetFactor lets the daily cap span 120 one-minute windows,
	// covering bursts across the day without matching a strict perMinute*1440.
	dailyBudgetFactor = 120
)

var (
	errRateMinute = errors.New("telemetry per-minute budget exhausted")
	errRateDay    = errors.New("telemetry daily budget exhausted")
	errRateClock  = errors.New("telemetry budget clock moved backwards")
	errRateRecord = errors.New("telemetry budget contains negative counters")
)

type rateBudget struct {
	fd   int
	file *os.File
}

func openRateBudget(directory, kind string) (rateBudget, error) {
	fd, err := unix.Open(filepath.Join(directory, "telemetry-"+kind+".rate"), unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, filemode.PrivateFile)
	if err != nil {
		return rateBudget{}, fmt.Errorf("open telemetry budget: %w", err)
	}
	file := os.NewFile(uintptr(fd), "telemetry-rate")
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()

		return rateBudget{}, fmt.Errorf("lock telemetry budget: %w", err)
	}

	return rateBudget{fd: fd, file: file}, nil
}

func (b rateBudget) release() {
	_ = unix.Flock(b.fd, unix.LOCK_UN)
	_ = b.file.Close()
}

type rateRecord struct {
	minute     int64
	usedMinute int
	day        int64
	usedDay    int
}

func (b rateBudget) read() (rateRecord, error) {
	var record rateRecord
	n, err := fmt.Fscan(io.LimitReader(b.file, rateRecordLimit), &record.minute, &record.usedMinute, &record.day, &record.usedDay)
	if err != nil && (!errors.Is(err, io.EOF) || n != 0) {
		return rateRecord{}, fmt.Errorf("read telemetry budget: %w", err)
	}

	return record, nil
}

func (b rateBudget) write(record rateRecord) error {
	if _, err := b.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek telemetry budget: %w", err)
	}
	if err := b.file.Truncate(0); err != nil {
		return fmt.Errorf("truncate telemetry budget: %w", err)
	}
	_, err := fmt.Fprintf(b.file, "%d %d %d %d\n", record.minute, record.usedMinute, record.day, record.usedDay)
	if err != nil {
		return fmt.Errorf("write telemetry budget: %w", err)
	}

	return nil
}

func (record rateRecord) rollOver(now time.Time) (rateRecord, bool) {
	currentMinute, currentDay := now.Unix()/secondsPerMinute, now.Unix()/secondsPerDay
	if record.minute > currentMinute || record.day > currentDay {
		return rateRecord{}, false
	}
	if record.minute != currentMinute {
		record.minute, record.usedMinute = currentMinute, 0
	}
	if record.day != currentDay {
		record.day, record.usedDay = currentDay, 0
	}

	return record, true
}

func (record rateRecord) headroomReason(perMinute int) error {
	if record.usedMinute < 0 || record.usedDay < 0 {
		return errRateRecord
	}
	if record.usedMinute >= perMinute {
		return errRateMinute
	}
	if record.usedDay >= perMinute*dailyBudgetFactor {
		return errRateDay
	}

	return nil
}

func (record rateRecord) consume() rateRecord {
	record.usedMinute++
	record.usedDay++

	return record
}

// Sharing the budget across processes prevents daemon restart loops and DHCP
// hook invocations from resetting it. No identifiers or payloads are persisted.
func allowPersistedReason(directory, kind string, perMinute int, now time.Time) error {
	if err := os.MkdirAll(directory, filemode.PrivateDir); err != nil {
		return fmt.Errorf("create telemetry budget directory: %w", err)
	}
	budget, err := openRateBudget(directory, kind)
	if err != nil {
		return err
	}
	defer budget.release()
	record, err := budget.read()
	if err != nil {
		return err
	}
	var ok bool
	record, ok = record.rollOver(now)
	if !ok {
		return errRateClock
	}
	if err := record.headroomReason(perMinute); err != nil {
		return err
	}

	return budget.write(record.consume())
}
