package firmware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Image identifies a built firmware image and the catalog fingerprint it was built from.
type Image struct{ ID, Fingerprint string }

// Images is the subset of Docker operations the firmware pipeline needs.
type Images interface {
	Pull(context.Context, string) error
	Push(context.Context, string) error
	Tag(context.Context, string, string) error
	Inspect(context.Context, string) (Image, error)
	Import(context.Context, string, string, string) error
}

// Docker manages images through the Engine SDK.
type Docker struct {
	engine *client.Client
	auth   string
}

// NewDocker connects to the local Docker Engine, authenticating against
// ghcr.io when a token is given.
func NewDocker(username, token string) (*Docker, error) {
	engine, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	docker := &Docker{engine: engine}
	if token != "" {
		credentials, err := json.Marshal(struct { //nolint:gosec // Docker registry auth JSON requires this field name.
			Username      string `json:"username"`
			Password      string `json:"password"`
			ServerAddress string `json:"serveraddress"`
		}{username, token, "ghcr.io"})
		if err != nil {
			_ = engine.Close()

			return nil, err
		}
		docker.auth = base64.URLEncoding.EncodeToString(credentials)
	}

	return docker, nil
}

// Close releases the Docker Engine connection.
func (d *Docker) Close() error { return d.engine.Close() }

// Pull downloads the arm64 image ref from its registry.
func (d *Docker) Pull(ctx context.Context, ref string) error {
	result, err := d.engine.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: d.auth, Platforms: []ocispec.Platform{{OS: "linux", Architecture: "arm64"}}})
	if err != nil {
		return err
	}

	return result.Wait(ctx)
}

// Push uploads the image ref to its registry.
func (d *Docker) Push(ctx context.Context, ref string) error {
	result, err := d.engine.ImagePush(ctx, ref, client.ImagePushOptions{RegistryAuth: d.auth})
	if err != nil {
		return err
	}

	return result.Wait(ctx)
}

// Tag applies target as an additional tag for the source image.
func (d *Docker) Tag(ctx context.Context, source, target string) error {
	_, err := d.engine.ImageTag(ctx, client.ImageTagOptions{Source: source, Target: target})

	return err
}

// Inspect returns ref's image ID and catalog fingerprint label.
func (d *Docker) Inspect(ctx context.Context, ref string) (Image, error) {
	result, err := d.engine.ImageInspect(ctx, ref)
	if err != nil {
		return Image{}, err
	}
	image := Image{ID: result.ID}
	if result.Config != nil {
		image.Fingerprint = result.Config.Labels["io.github.udm-iptv.catalog-fingerprint"]
	}

	return image, nil
}

// Import loads a filesystem archive as an arm64 image tagged ref, labeled
// with its catalog fingerprint.
func (d *Docker) Import(ctx context.Context, archive, ref, fingerprint string) (resultErr error) {
	input, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() {
		if err := input.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close firmware archive: %w", err))
		}
	}()
	result, err := d.engine.ImageImport(ctx, client.ImageImportSource{Source: input, SourceName: "-"}, ref, client.ImageImportOptions{
		Platform: ocispec.Platform{OS: "linux", Architecture: "arm64"},
		Changes:  []string{`CMD ["/bin/bash"]`, "ENV DEBIAN_FRONTEND=noninteractive", `LABEL org.opencontainers.image.description="Extracted UniFi OS filesystem for lifecycle tests"`, fmt.Sprintf(`LABEL io.github.udm-iptv.catalog-fingerprint="%s"`, fingerprint)},
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := result.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close Docker import response: %w", err))
		}
	}()

	return readImportResult(result)
}

func readImportResult(reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	for {
		var message struct {
			Error string `json:"error"`
		}
		err := decoder.Decode(&message)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}
		if message.Error != "" {
			return fmt.Errorf("import firmware image: %s", message.Error)
		}
	}
}
