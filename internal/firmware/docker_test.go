package firmware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func TestDockerStreamFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Registry-Auth") != "test-auth" {
			t.Error("registry authentication missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":"registry rejected operation","errorDetail":{"message":"registry rejected operation"}}`)
	}))
	defer server.Close()
	engine, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.45"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	})
	docker := &Docker{engine: engine, auth: "test-auth"}
	for _, operation := range []func() error{
		func() error { return docker.Pull(t.Context(), testImage+":latest") },
		func() error { return docker.Push(t.Context(), testImage+":latest") },
	} {
		err := operation()
		if err == nil || !strings.Contains(err.Error(), "registry rejected") {
			t.Fatalf("stream error lost: %v", err)
		}
	}
}

func TestImportResult(t *testing.T) {
	for _, test := range []struct {
		input string
		valid bool
	}{
		{`{"status":"Importing"}` + "\n" + `{"status":"sha256:done"}`, true},
		{`{"error":"broken import"}`, false},
		{`{"status":`, false},
	} {
		err := readImportResult(strings.NewReader(test.input))
		if (err == nil) != test.valid {
			t.Fatalf("import result: %v", err)
		}
	}
}
