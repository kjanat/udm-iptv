package telemetry

import (
	"context"
	"io"
	"slices"
	"sync"

	"github.com/getsentry/sentry-go"

	"github.com/kjanat/udm-iptv/internal/config"
)

const (
	// Keep recent evidence without streaming normal subprocess output to Sentry.
	outputTailLimit = 64 << 10
	outputSources   = 8
)

// LineWriter retains the most recent output for an error attachment. The caller
// still writes the complete stream to its normal local output independently.
func (r *Reporter) LineWriter(_ context.Context, source string) io.Writer {
	if r == nil || r.client == nil || !r.settings.Logs || !r.settings.Errors {
		return io.Discard
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.outputTails == nil {
		r.outputTails = make(map[string]*outputTail)
	}
	if existing := r.outputTails[source]; existing != nil {
		return existing
	}
	if len(r.outputTails) >= outputSources {
		return io.Discard
	}
	writer := &outputTail{owner: r}
	r.outputTails[source] = writer
	return writer
}

type outputTail struct {
	owner  *Reporter
	mu     sync.Mutex
	buffer []byte
}

func (tail *outputTail) Write(data []byte) (int, error) {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	n := len(data)
	if !tail.owner.logsEnabled() {
		tail.buffer = nil
		return n, nil
	}
	if n >= outputTailLimit {
		tail.buffer = append(tail.buffer[:0], data[n-outputTailLimit:]...)
	} else {
		if discard := len(tail.buffer) + n - outputTailLimit; discard > 0 {
			copy(tail.buffer, tail.buffer[discard:])
			tail.buffer = tail.buffer[:len(tail.buffer)-discard]
		}
		tail.buffer = append(tail.buffer, data...)
	}
	return n, nil
}

func (tail *outputTail) bytes() []byte {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	return slices.Clone(tail.buffer)
}

func (r *Reporter) logsEnabled() bool {
	if !r.settings.Logs {
		return false
	}
	if r.configPath == "" {
		return true
	}
	value, err := config.Load(r.configPath)
	return err == nil && value.Telemetry.Enabled && value.Telemetry.Logs
}

func (r *Reporter) outputAttachments() []*sentry.Attachment {
	if !r.logsEnabled() {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var attachments []*sentry.Attachment
	for source, tail := range r.outputTails {
		if data := tail.bytes(); len(data) != 0 {
			attachments = append(attachments, &sentry.Attachment{
				Filename: "udm-iptv-" + source + "-tail.log", ContentType: "application/octet-stream", Payload: data,
			})
		}
	}
	return attachments
}
