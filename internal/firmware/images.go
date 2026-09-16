package firmware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kjanat/udm-iptv/internal/filemode"
)

// Runner executes an external command, streaming its output to a writer.
type Runner interface {
	Run(context.Context, io.Writer, string, ...string) error
}

const commandTimeout = 15 * time.Minute

// Commands runs commands directly, bounded by commandTimeout.
type Commands struct{ Log io.Writer }

// Run executes name with args, writing its stdout to output and stderr to c.Log.
func (c Commands) Run(ctx context.Context, output io.Writer, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout, command.Stderr = output, c.Log
	err := command.Run()
	if err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return nil
}

// Pipeline builds, publishes and selects firmware test images.
type Pipeline struct {
	Runner Runner
	Images Images
	Client *http.Client
	Log    io.Writer
}

func (p Pipeline) capture(ctx context.Context, name string, args ...string) (string, error) {
	var output bytes.Buffer
	err := p.Runner.Run(ctx, &output, name, args...)

	return strings.TrimSpace(output.String()), err
}

// Fingerprint hashes a release's identifying fields, to detect catalog drift.
func Fingerprint(model string, release Release) string {
	data := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\n", model, release.Board, release.Version, release.URL, release.SHA256)

	return fmt.Sprintf("%x", sha256.Sum256([]byte(data)))
}

// Build extracts releases' root filesystems and imports them as test images.
func (p Pipeline) Build(ctx context.Context, image, model, cache string, releases []Release) error {
	err := ValidateImage(image)
	if err != nil {
		return err
	}
	err = ValidatePair(model, releases)
	if err != nil {
		return err
	}
	if cache == "" {
		return errors.New("cache directory required")
	}
	err = os.MkdirAll(cache, filemode.SharedDir)
	if err != nil {
		return err
	}
	for _, release := range releases {
		ref := image + ":" + model + "-" + release.Version
		fingerprint := Fingerprint(model, release)
		err := p.Images.Pull(ctx, ref)
		if err == nil {
			actual, err := p.Images.Inspect(ctx, ref)
			if err != nil {
				return err
			}
			if actual.Fingerprint == fingerprint {
				if _, err := fmt.Fprintf(p.Log, "Using validated %s\n", ref); err != nil {
					return fmt.Errorf("report cached firmware image: %w", err)
				}

				continue
			}
		}
		err = p.buildImage(ctx, cache, ref, fingerprint, release)
		if err != nil {
			return err
		}
	}

	return nil
}

func (p Pipeline) buildImage(ctx context.Context, cache, ref, fingerprint string, release Release) (err error) {
	work, err := os.MkdirTemp(cache, "image-")
	if err != nil {
		return err
	}
	// Extraction creates root-owned entries; cleanup owns only this fresh directory.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		err = errors.Join(err, p.Runner.Run(cleanup, p.Log, "sudo", "rm", "-rf", "--", work))
	}()
	firmware := filepath.Join(work, "firmware.bin")
	if err := p.download(ctx, release, firmware); err != nil {
		return err
	}
	rootfs := filepath.Join(work, "rootfs.squashfs")
	if err := extractFile(firmware, rootfs); err != nil {
		return err
	}
	root := filepath.Join(work, "rootfs")
	if err := p.Runner.Run(ctx, p.Log, "sudo", "unsquashfs", "-xattrs", "-d", root, rootfs); err != nil {
		return err
	}
	archive := filepath.Join(work, "rootfs.tar")
	if err := p.Runner.Run(ctx, p.Log, "sudo", "tar", "--xattrs", "--xattrs-include=*", "--numeric-owner", "-C", root, "-cf", archive, "."); err != nil {
		return err
	}

	return p.Images.Import(ctx, archive, ref, fingerprint)
}

func (p Pipeline) download(ctx context.Context, release Release, destination string) (err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, release.URL, nil)
	if err != nil {
		return err
	}
	response, err := p.Client.Do(request)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close firmware download response: %w", closeErr))
		}
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("firmware download: HTTP %d", response.StatusCode)
	}
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, output.Close()) }()
	checksum := sha256.New()
	// Refuse oversized downloads before filling a runner disk.
	n, err := io.Copy(io.MultiWriter(output, checksum), io.LimitReader(response.Body, maximumFirmwareSize+1))
	if err != nil {
		return err
	}
	if n > maximumFirmwareSize {
		return errors.New("firmware exceeds four GiB")
	}
	if !strings.EqualFold(hex.EncodeToString(checksum.Sum(nil)), release.SHA256) {
		return errors.New("firmware SHA256 mismatch")
	}

	return nil
}

func extractFile(source, destination string) (err error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := input.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close firmware image: %w", closeErr))
		}
	}()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, output.Close()) }()

	return Extract(input, info.Size(), output)
}

// Publish rebuilds and pushes releases' images only if their catalog fingerprint changed.
func (p Pipeline) Publish(ctx context.Context, image, model string, releases []Release) error {
	if err := ValidateImage(image); err != nil {
		return err
	}
	if err := ValidatePair(model, releases); err != nil {
		return err
	}
	owner := strings.Split(image, "/")[1]
	visibility, err := p.capture(ctx, "gh", "api", "/users/"+owner+"/packages/container/unifi-os", "--jq", ".visibility")
	if err != nil {
		return err
	}
	if visibility != "public" {
		return errors.New("unifi-os must be public in GitHub Packages")
	}
	for index, release := range releases {
		source := image + ":" + model + "-" + release.Version
		tags := []string{source, image + ":" + release.Board + "-" + release.Version}
		if index == len(releases)-1 {
			tags = append(tags, image+":"+model+"-latest", image+":"+release.Board+"-latest")
			if model == "udmpro" {
				tags = append(tags, image+":latest")
			}
		}
		for _, tag := range tags {
			err := p.publishTag(ctx, source, tag)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (p Pipeline) publishTag(ctx context.Context, source, target string) error {
	expected, err := p.Images.Inspect(ctx, source)
	if err != nil {
		return err
	}
	if source != target {
		err := p.Images.Tag(ctx, source, target)
		if err != nil {
			return err
		}
	}
	if err := p.Images.Push(ctx, target); err != nil {
		return err
	}
	if err := p.Images.Pull(ctx, target); err != nil {
		return err
	}
	actual, err := p.Images.Inspect(ctx, target)
	if err != nil {
		return err
	}
	if expected.ID == "" || actual.ID != expected.ID {
		return fmt.Errorf("published image differs: %s", target)
	}

	return nil
}

// Published selects the latest two published firmware pairs per model, by
// listing image's registry tags.
func (p Pipeline) Published(ctx context.Context, image string) (Matrix, error) {
	if err := ValidateImage(image); err != nil {
		return Matrix{}, err
	}
	owner := strings.Split(image, "/")[1]
	tags, err := p.capture(ctx, "gh", "api", "/users/"+owner+"/packages/container/unifi-os/versions?per_page=100", "--paginate", "--jq", ".[].metadata.container.tags[]")
	if err != nil {
		return Matrix{}, err
	}

	return Published(tags, image)
}
