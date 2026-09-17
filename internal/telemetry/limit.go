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

type rateBudget struct {
	fd   int
	file *os.File
}

func openRateBudget(directory, kind string) (rateBudget, bool) {
	fd, err := unix.Open(filepath.Join(directory, "telemetry-"+kind+".rate"), unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, filemode.PrivateFile)
	if err != nil {
		return rateBudget{}, false
	}
	file := os.NewFile(uintptr(fd), "telemetry-rate")
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()

		return rateBudget{}, false
	}

	return rateBudget{fd: fd, file: file}, true
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

func (b rateBudget) read() (rateRecord, bool) {
	var record rateRecord
	n, err := fmt.Fscan(io.LimitReader(b.file, rateRecordLimit), &record.minute, &record.usedMinute, &record.day, &record.usedDay)
	if err != nil && (!errors.Is(err, io.EOF) || n != 0) {
		return rateRecord{}, false
	}

	return record, true
}

func (b rateBudget) write(record rateRecord) bool {
	if _, err := b.file.Seek(0, io.SeekStart); err != nil {
		return false
	}
	if err := b.file.Truncate(0); err != nil {
		return false
	}
	_, err := fmt.Fprintf(b.file, "%d %d %d %d\n", record.minute, record.usedMinute, record.day, record.usedDay)

	return err == nil
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

func (record rateRecord) hasHeadroom(perMinute int) bool {
	return record.usedMinute >= 0 && record.usedDay >= 0 &&
		record.usedMinute < perMinute && record.usedDay < perMinute*dailyBudgetFactor
}

func (record rateRecord) consume() rateRecord {
	record.usedMinute++
	record.usedDay++

	return record
}

// Sharing the budget across processes prevents daemon restart loops and DHCP
// hook invocations from resetting it. No identifiers or payloads are persisted.
func allowPersisted(directory, kind string, perMinute int, now time.Time) bool {
	if err := os.MkdirAll(directory, filemode.PrivateDir); err != nil {
		return false
	}
	budget, ok := openRateBudget(directory, kind)
	if !ok {
		return false
	}
	defer budget.release()
	record, ok := budget.read()
	if !ok {
		return false
	}
	record, ok = record.rollOver(now)
	if !ok || !record.hasHeadroom(perMinute) {
		return false
	}

	return budget.write(record.consume())
}
