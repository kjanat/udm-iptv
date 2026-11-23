package diagnostics

import (
	"bytes"
	"errors"
	"testing"
)

var errDiskFailure = errors.New("disk failure")

type countingOutput struct {
	bytes.Buffer

	syncs   int
	failure error
}

func (output *countingOutput) Sync() error {
	output.syncs++
	return output.failure
}

func TestJournalBatchSyncsOncePerOutput(t *testing.T) {
	json, text := &countingOutput{}, &countingOutput{}
	writer := diagnosticWriter{json: json, text: text}
	for range 10_000 {
		err := writer.write(Event{Type: "log", Log: "example"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if json.syncs != 0 || text.syncs != 0 {
		t.Fatal("per-line sync")
	}
	err := writer.write(Event{Type: "completed", Message: "Done"})
	if err != nil {
		t.Fatal(err)
	}
	if json.syncs != 1 || text.syncs != 1 {
		t.Fatal("missing batch sync")
	}
	if bytes.Count(json.Bytes(), []byte("\n")) != 10_001 {
		t.Fatal("lost JSON events")
	}
	text.failure = errDiskFailure
	err = writer.flush()
	if !errors.Is(err, text.failure) {
		t.Fatal("sync failure discarded")
	}
}
