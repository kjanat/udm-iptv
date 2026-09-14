package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/go-github/v80/github"
	"github.com/spf13/cobra"
)

type upgradeOptions struct {
	Repository string
	Version    string
	TokenFile  string
	Force      bool
}

func (application *Application) upgradeCommand() *cobra.Command {
	options := upgradeOptions{Repository: "kjanat/udm-iptv"}
	command := &cobra.Command{
		Use: "upgrade", Short: "Install the latest udm-iptv release", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			return application.upgrade(command.Context(), options)
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.Repository, "repository", options.Repository, "GitHub repository")
	flags.StringVar(&options.Version, "version", "latest", "release version or latest")
	flags.StringVar(&options.TokenFile, "token-file", "", "file containing a GitHub token for private repositories")
	flags.BoolVar(&options.Force, "force", false, "reinstall even when the selected version is already installed")
	return command
}

func (application *Application) upgrade(ctx context.Context, options upgradeOptions) error {
	if !installed(application.StateDir) {
		return errors.New("udm-iptv is not installed; run 'udm-iptv install' first")
	}
	owner, repository, found := strings.Cut(options.Repository, "/")
	if !found || owner == "" || repository == "" || strings.Contains(repository, "/") {
		return fmt.Errorf("invalid repository %q", options.Repository)
	}
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if options.TokenFile != "" {
		data, err := os.ReadFile(options.TokenFile)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(data))
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if token != "" {
		httpClient.Transport = &bearerTransport{token: token, base: http.DefaultTransport}
	}
	client := github.NewClient(httpClient)
	var release *github.RepositoryRelease
	var err error
	if options.Version == "" || options.Version == "latest" {
		release, _, err = client.Repositories.GetLatestRelease(ctx, owner, repository)
	} else {
		tag := options.Version
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		release, _, err = client.Repositories.GetReleaseByTag(ctx, owner, repository, tag)
	}
	if err != nil {
		return fmt.Errorf("resolve release: %w", err)
	}
	version := strings.TrimPrefix(release.GetTagName(), "v")
	if !options.Force && application.Version == version {
		return writef(application.Out, "udm-iptv %s is already installed. Use --force to reinstall.\n", version)
	}
	assetName := "udm-iptv-linux-" + runtime.GOARCH
	assetURL, checksumURL := releaseAssetURLs(release, assetName)
	if assetURL == "" || checksumURL == "" {
		return fmt.Errorf("release %s does not contain %s and SHA256SUMS", release.GetTagName(), assetName)
	}
	directory, err := os.MkdirTemp("", "udm-iptv-upgrade-*")
	if err != nil {
		return err
	}
	defer removeAllIgnoringError(directory)
	binaryPath := filepath.Join(directory, assetName)
	checksumPath := filepath.Join(directory, "SHA256SUMS")
	if err := writef(application.Out, "Downloading udm-iptv %s...\n", version); err != nil {
		return err
	}
	if err := download(ctx, httpClient, assetURL, binaryPath, 0o755); err != nil {
		return err
	}
	if err := download(ctx, httpClient, checksumURL, checksumPath, 0o600); err != nil {
		return err
	}
	if err := verifyChecksum(binaryPath, checksumPath, assetName); err != nil {
		return err
	}
	target := filepath.Join(application.StateDir, "bin", "udm-iptv")
	backup := filepath.Join(application.StateDir, "bin", ".udm-iptv.previous")
	if err := copyExecutable(target, backup); err != nil {
		return fmt.Errorf("back up current executable: %w", err)
	}
	defer removeIgnoringError(backup)
	if err := copyExecutable(binaryPath, target); err != nil {
		return err
	}
	if err := application.restart(ctx, true); err != nil {
		if rollbackErr := copyExecutable(backup, target); rollbackErr != nil {
			return fmt.Errorf("installed %s but failed to restart cleanly (%v), and rollback failed: %w", version, err, rollbackErr)
		}
		if rollbackErr := application.restart(ctx, true); rollbackErr != nil {
			return fmt.Errorf("installed %s but failed to restart cleanly (%v); restored the old binary but its restart failed: %w", version, err, rollbackErr)
		}
		return fmt.Errorf("installed %s but failed its health check and was rolled back: %w", version, err)
	}
	return writef(application.Out, "Upgraded udm-iptv to %s.\n", version)
}

func releaseAssetURLs(release *github.RepositoryRelease, binaryName string) (string, string) {
	binaryURL, checksumURL := "", ""
	for _, asset := range release.Assets {
		switch asset.GetName() {
		case binaryName:
			binaryURL = asset.GetURL()
		case "SHA256SUMS":
			checksumURL = asset.GetURL()
		}
	}
	return binaryURL, checksumURL
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport *bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	if clone.URL.Hostname() == "api.github.com" {
		clone.Header.Set("Authorization", "Bearer "+transport.token)
		clone.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	return transport.base.RoundTrip(clone)
}

func download(ctx context.Context, client *http.Client, url, target string, mode os.FileMode) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if request.URL.Hostname() == "api.github.com" {
		request.Header.Set("Accept", "application/octet-stream")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer closeIgnoringError(response.Body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, response.Status)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".download-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer removeIgnoringError(name)
	if err := temporary.Chmod(mode); err != nil {
		closeIgnoringError(temporary)
		return err
	}
	if _, err := io.Copy(temporary, io.LimitReader(response.Body, 128<<20)); err != nil {
		closeIgnoringError(temporary)
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, target)
}

func verifyChecksum(binaryPath, checksumPath, assetName string) error {
	file, err := os.Open(checksumPath)
	if err != nil {
		return err
	}
	defer closeIgnoringError(file)
	expected := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == assetName {
			expected = fields[0]
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if expected == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s", assetName)
	}
	input, err := os.Open(binaryPath)
	if err != nil {
		return err
	}
	defer closeIgnoringError(input)
	hash := sha256.New()
	if _, err := io.Copy(hash, input); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return errors.New("downloaded binary does not match SHA256SUMS")
	}
	return nil
}
