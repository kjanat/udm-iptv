package firmware

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	errResponseCloseFailed = errors.New("response close failed")
	errOutputUnavailable   = errors.New("output unavailable")
)

type fakeRunner struct {
	calls        []string
	visibility   string
	fingerprints map[string]string
	mismatch     bool
	pulled       bool
}

func (r *fakeRunner) Pull(ctx context.Context, ref string) error {
	return r.Run(ctx, io.Discard, "docker", "pull", ref)
}

func (r *fakeRunner) Push(ctx context.Context, ref string) error {
	return r.Run(ctx, io.Discard, "docker", "push", ref)
}

func (r *fakeRunner) Tag(ctx context.Context, source, target string) error {
	return r.Run(ctx, io.Discard, "docker", "tag", source, target)
}

func (r *fakeRunner) Import(ctx context.Context, archive, ref, fingerprint string) error {
	return r.Run(ctx, io.Discard, "docker", "import", archive, ref, fingerprint)
}

func (r *fakeRunner) Inspect(_ context.Context, ref string) (Image, error) {
	image := Image{ID: "sha256:expected", Fingerprint: r.fingerprints[ref]}
	if r.mismatch && r.pulled {
		image.ID = "sha256:wrong"
	}

	return image, nil
}

func (r *fakeRunner) Run(_ context.Context, w io.Writer, name string, args ...string) error {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	handler, known := map[string]func(io.Writer, []string) error{
		"gh":     r.runGitHub,
		"sudo":   r.runSudo,
		"docker": r.runDocker,
	}[name]
	if !known {
		return nil
	}

	return handler(w, args)
}

func (r *fakeRunner) runGitHub(w io.Writer, _ []string) error {
	if _, err := fmt.Fprint(w, r.visibility); err != nil {
		return fmt.Errorf("write package visibility: %w", err)
	}

	return nil
}

func (r *fakeRunner) runSudo(_ io.Writer, args []string) error {
	if args[0] != "rm" {
		return nil
	}
	if err := os.RemoveAll(args[len(args)-1]); err != nil {
		return fmt.Errorf("remove %s: %w", args[len(args)-1], err)
	}

	return nil
}

func (r *fakeRunner) runDocker(w io.Writer, args []string) error {
	switch args[0] {
	case "pull":
		r.pulled = true
	case "image":
		if _, err := fmt.Fprint(w, r.inspected(args)); err != nil {
			return fmt.Errorf("write image inspection: %w", err)
		}
	}

	return nil
}

func (r *fakeRunner) inspected(args []string) string {
	if len(args) > 4 && strings.Contains(args[3], "fingerprint") {
		return r.fingerprints[args[4]]
	}
	if r.mismatch && r.pulled {
		return "sha256:wrong"
	}

	return "sha256:expected"
}

func (r *fakeRunner) count(prefix string) int {
	matched := 0
	for _, call := range r.calls {
		if strings.HasPrefix(call, prefix) {
			matched++
		}
	}

	return matched
}

func TestPublishAliasesAndRemoteVerification(t *testing.T) {
	for _, model := range models {
		t.Run(model.Name, func(t *testing.T) {
			runner := &fakeRunner{visibility: "public"}
			p := Pipeline{Runner: runner, Images: runner, Log: io.Discard}
			err := p.Publish(t.Context(), testImage, model.Name, pairFor(model.Name))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{model.Name + "-5.1.9", model.Board + "-5.1.9", model.Name + "-5.1.10", model.Board + "-5.1.10", model.Name + "-latest", model.Board + "-latest"}
			if model.Name == "udmpro" {
				want = append(want, "latest")
			}
			for _, operation := range []string{"push", "pull"} {
				var got []string
				for _, call := range runner.calls {
					if tag, ok := strings.CutPrefix(call, "docker "+operation+" "+testImage+":"); ok {
						got = append(got, tag)
					}
				}
				if !slices.Equal(got, want) {
					t.Fatalf("%s: %v != %v", operation, got, want)
				}
			}
		})
	}
}

func TestPublicationStopsOnFailure(t *testing.T) {
	for _, test := range []struct {
		name, visibility string
		mismatch         bool
		pair             []Release
	}{
		{"private", "private", false, pairFor("udmpro")},
		{"mismatch", "public", true, pairFor("udmpro")},
		{"invalid", "public", false, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{visibility: test.visibility, mismatch: test.mismatch}
			p := Pipeline{Runner: runner, Images: runner, Log: io.Discard}
			err := p.Publish(t.Context(), testImage, "udmpro", test.pair)
			if err == nil {
				t.Fatal("publication unexpectedly succeeded")
			}
			pushes := runner.count("docker push ")
			if test.mismatch && pushes != 1 || !test.mismatch && pushes != 0 {
				t.Fatalf("unexpected pushes: %d", pushes)
			}
		})
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

type failedCloseBody struct {
	io.Reader

	err error
}

func (body failedCloseBody) Close() error { return body.err }

func TestHTTPFailuresRetainCloseError(t *testing.T) {
	want := errResponseCloseFailed
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: failedCloseBody{strings.NewReader(""), want}}, nil
	})}
	_, err := Discover(t.Context(), client, "https://example.invalid/catalog", testImage, "udmpro", time.Now())
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("catalog lost an error: %v", err)
	}
	pipeline := Pipeline{Client: client}
	err = pipeline.download(t.Context(), pairFor("udmpro")[0], filepath.Join(t.TempDir(), "image.bin"))
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("download lost an error: %v", err)
	}
}

func TestCachedBuildReportsOutputFailure(t *testing.T) {
	pair := pairFor("udmpro")
	runner := &fakeRunner{fingerprints: map[string]string{testImage + ":udmpro-" + pair[0].Version: Fingerprint("udmpro", pair[0])}}
	want := errOutputUnavailable
	pipeline := Pipeline{Runner: runner, Images: runner, Log: failingExtractWriter{want}}
	err := pipeline.Build(t.Context(), testImage, "udmpro", t.TempDir(), pair)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "report cached firmware image") {
		t.Fatalf("lost output failure: %v", err)
	}
}

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func buildFixture(image []byte, checksum, cached bool) ([]Release, *fakeRunner) {
	pair := pairFor("udmpro")
	runner := &fakeRunner{fingerprints: make(map[string]string)}
	for i := range pair {
		if checksum {
			pair[i].SHA256 = fmt.Sprintf("%x", sha256.Sum256(image))
		}
		if cached {
			runner.fingerprints[testImage+":udmpro-"+pair[i].Version] = Fingerprint("udmpro", pair[i])
		}
	}

	return pair, runner
}

func TestBuildCacheAndChecksum(t *testing.T) {
	for _, test := range []struct {
		name               string
		checksum, cached   bool
		fails              bool
		downloads, imports int
	}{
		{"cache", true, true, false, 0, 0},
		{"download", true, false, false, 2, 2},
		{"bad-checksum", false, false, true, 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			image := fixture(false)
			pair, runner := buildFixture(image, test.checksum, test.cached)
			downloads := 0
			client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				downloads++

				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(image)))}, nil
			})}
			p := Pipeline{Runner: runner, Images: runner, Client: client, Log: io.Discard}
			cache := t.TempDir()
			err := p.Build(t.Context(), testImage, "udmpro", cache, pair)
			if (err != nil) != test.fails {
				t.Fatalf("build: %v", err)
			}
			if downloads != test.downloads {
				t.Fatalf("downloads=%d, want %d", downloads, test.downloads)
			}
			if imports := runner.count("docker import "); imports != test.imports {
				t.Fatalf("imports=%d, want %d", imports, test.imports)
			}
			entries, err := os.ReadDir(cache)
			if err != nil || len(entries) != 0 {
				t.Fatalf("extraction directories left behind: %v %v", entries, err)
			}
		})
	}
}
