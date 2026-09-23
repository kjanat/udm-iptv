package telemetry

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/getsentry/sentry-go"
	sentryslog "github.com/getsentry/sentry-go/slog"
)

// LineWriter retains whitespace and splits oversized lines into numbered
// fragments. line.end distinguishes a newline from an unterminated final line.
func (r *Reporter) LineWriter(ctx context.Context, source string) io.Writer {
	if r == nil || r.client == nil || !r.settings.Logs {
		return io.Discard
	}
	ctx = sentry.SetHubOnContext(ctx, r.hub)
	writer := &lineWriter{owner: r, logger: slog.New(sentryslog.Option{}.NewSentryHandler(ctx)).With("source", source)}
	r.mu.Lock()
	r.lineWriters = append(r.lineWriters, writer)
	r.mu.Unlock()
	return writer
}

type lineWriter struct {
	mu     sync.Mutex
	owner  *Reporter
	logger *slog.Logger
	buffer []byte
	line   uint64
	part   uint64
}

func (w *lineWriter) emit(data []byte, end bool) {
	text, encoding := string(data), "utf8"
	if len(data) == 0 {
		// Sentry's Logger drops an empty body before BeforeSendLog runs.
		text = "\n"
	}
	if !utf8.Valid(data) {
		text, encoding = base64.StdEncoding.EncodeToString(data), "base64"
	}
	w.logger.Info(text, "line.number", w.line, "line.part", w.part, "line.end", end, "line.encoding", encoding, "line.empty", len(data) == 0)
	if end {
		w.line++
		w.part = 0
	} else {
		w.part++
	}
}

func (w *lineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := len(data)
	for len(data) > 0 {
		count := min(lineLimit-len(w.buffer), len(data))
		w.buffer = append(w.buffer, data[:count]...)
		data = data[count:]
		w.emitBufferedLines()
	}
	return written, nil
}

func (w *lineWriter) emitBufferedLines() {
	for {
		index := bytes.IndexByte(w.buffer, '\n')
		if index < 0 {
			break
		}
		w.emit(w.buffer[:index], true)
		w.buffer = w.buffer[index+1:]
	}
	if len(w.buffer) == lineLimit {
		count := len(w.buffer)
		// Keep a split UTF-8 rune for the next fragment.
		for count > 0 && !utf8.RuneStart(w.buffer[count-1]) {
			count--
		}
		if count == 0 {
			count = len(w.buffer)
		} else if !utf8.FullRune(w.buffer[count-1:]) {
			count--
		}
		w.emit(w.buffer[:count], false)
		w.buffer = append(w.buffer[:0], w.buffer[count:]...)
	}
}

func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buffer) != 0 {
		w.emit(w.buffer, false)
		w.buffer = w.buffer[:0]
	}
}

// FlushLines records an unterminated child-output line once its producer exits.
func FlushLines(writer io.Writer) {
	if stream, ok := writer.(*lineWriter); ok {
		stream.Flush()
		if stream.owner != nil {
			stream.owner.mu.Lock()
			stream.owner.lineWriters = slices.DeleteFunc(stream.owner.lineWriters, func(candidate *lineWriter) bool { return candidate == stream })
			stream.owner.mu.Unlock()
		}
	}
}
