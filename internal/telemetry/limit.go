package telemetry

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// Sharing the budget across processes prevents daemon restart loops and DHCP
// hook invocations from resetting it. No identifiers or payloads are persisted.
func allowPersisted(directory, kind string, perMinute int, now time.Time) bool {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return false
	}
	fd, err := unix.Open(filepath.Join(directory, "telemetry-"+kind+".rate"), unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(fd), "telemetry-rate")
	defer func() { _ = file.Close() }()
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return false
	}
	defer func() { _ = unix.Flock(fd, unix.LOCK_UN) }()
	var minute, day int64
	var usedMinute, usedDay int
	n, err := fmt.Fscan(io.LimitReader(file, 128), &minute, &usedMinute, &day, &usedDay)
	if err != nil && (err != io.EOF || n != 0) {
		return false
	}
	currentMinute, currentDay := now.Unix()/60, now.Unix()/86400
	if minute > currentMinute || day > currentDay {
		return false
	}
	if minute != currentMinute {
		minute, usedMinute = currentMinute, 0
	}
	if day != currentDay {
		day, usedDay = currentDay, 0
	}
	if usedMinute < 0 || usedDay < 0 || usedMinute >= perMinute || usedDay >= perMinute*120 {
		return false
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false
	}
	if err := file.Truncate(0); err != nil {
		return false
	}
	_, err = fmt.Fprintf(file, "%d %d %d %d\n", minute, usedMinute+1, day, usedDay+1)
	return err == nil
}
