package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/kjanat/udm-iptv/internal/filemode"
)

const (
	warningInterval = 15 * time.Minute
	warningKeys     = 64
	warningFileSize = 32 << 10
)

// Routine successes add no remote signal. Failures, lease acquisition and
// deconfiguration still report; breadcrumbs retain the local operation trail.
func routineOperation(operation string) bool {
	switch operation {
	case "dhcp.renew", "dhcp.leasefail", "dhcp.nak", "service.health":
		return true
	default:
		return false
	}
}

func finishOperationSpan(span *sentry.Span, operation string, err error) {
	if span == nil {
		return
	}
	span.Status = spanStatus(err)
	if routineOperation(operation) && err == nil {
		span.Sampled = sentry.SampledFalse
	}
	span.Finish()
}

// Ready records actual readiness after the service's startup settling period.
func (r *Reporter) Ready(ctx context.Context) {
	if r == nil || r.client == nil || !r.settings.Logs {
		return
	}
	sentry.NewLogger(sentry.SetHubOnContext(ctx, r.hub)).Info().Emit("service ready")
}

type warningRecord struct {
	Sent    time.Time `json:"sent"`
	Pending uint64    `json:"pending"`
}

// warningOccurrences sends the first warning promptly and coalesces repeats.
// Persist hashes and counts, not message text, so short DHCP hook processes
// share the same window. The next occurrence after the window reports its count.
func (r *Reporter) warningOccurrences(message string, now time.Time) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	digest := sha256.Sum256([]byte(message))
	key := hex.EncodeToString(digest[:])
	if r.stateDir == "" {
		if r.warnings == nil {
			r.warnings = make(map[string]warningRecord)
		}
		return coalesceWarning(r.warnings, key, now)
	}
	count, err := persistedWarning(r.stateDir, key, now)
	if err != nil {
		r.deliveryIssue("logs", "coalesce warning: "+err.Error())
		return 0
	}
	return count
}

func coalesceWarning(records map[string]warningRecord, key string, now time.Time) uint64 {
	record, exists := records[key]
	if !exists && len(records) >= warningKeys {
		var oldest string
		for candidate, value := range records {
			if oldest == "" || value.Sent.Before(records[oldest].Sent) {
				oldest = candidate
			}
		}
		delete(records, oldest)
	}
	record.Pending++
	var count uint64
	if !exists || now.Sub(record.Sent) >= warningInterval {
		count = record.Pending
		record.Sent, record.Pending = now, 0
	}
	records[key] = record
	return count
}

func persistedWarning(directory, key string, now time.Time) (uint64, error) {
	if err := os.MkdirAll(directory, filemode.PrivateDir); err != nil {
		return 0, fmt.Errorf("create warning directory: %w", err)
	}
	budget, err := openRateBudget(directory, "warnings")
	if err != nil {
		return 0, err
	}
	defer budget.release()
	records, err := loadWarningRecords(budget.file)
	if err != nil {
		return 0, err
	}
	count := coalesceWarning(records, key, now)
	if _, err := budget.file.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek warnings: %w", err)
	}
	if err := budget.file.Truncate(0); err != nil {
		return 0, fmt.Errorf("truncate warnings: %w", err)
	}
	if err := json.NewEncoder(budget.file).Encode(records); err != nil {
		return 0, fmt.Errorf("write warnings: %w", err)
	}
	return count, nil
}

func loadWarningRecords(file io.Reader) (map[string]warningRecord, error) {
	records := make(map[string]warningRecord)
	data, err := io.ReadAll(io.LimitReader(file, warningFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read warnings: %w", err)
	}
	if len(data) > warningFileSize {
		return nil, errStateTooLarge
	}
	if len(data) != 0 {
		if err := json.Unmarshal(data, &records); err != nil {
			return nil, fmt.Errorf("decode warnings: %w", err)
		}
	}
	if records == nil {
		records = make(map[string]warningRecord)
	}
	return records, nil
}
