package installer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/google/go-github/v80/github"
	"github.com/klauspost/compress/snappy"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/kjanat/udm-iptv/internal/filemode"
)

const (
	upgradeClientTimeout = 30 * time.Second
	releaseListPageSize  = 30
	// releaseAssetLimit bounds a downloaded GitHub release asset.
	releaseAssetLimit = 128 << 20
	// attestationResponseLimit bounds the GitHub attestations API response body.
	attestationResponseLimit = 8 << 20
	// attestationDecodedLimit bounds a Snappy block's declared decoded length,
	// which a hostile bundle can inflate far beyond its compressed size.
	attestationDecodedLimit = 32 << 20
	// githubAPIHost is the only host that receives the release token.
	githubAPIHost = "api.github.com"
	// Version 2026-03-10 drops the inline attestation bundle and serves it from a Snappy-compressed bundle_url.
	githubAPIVersion = "2026-03-10"
	// attestationIssuer is the OIDC issuer Fulcio records for GitHub Actions identities.
	attestationIssuer = "https://token.actions.githubusercontent.com"
)

var (
	errNotInstalled         = errors.New("udm-iptv is not installed; run 'udm-iptv install' first")
	errInvalidRepository    = errors.New("invalid repository")
	errReleaseAssetsMissing = errors.New("release is missing required assets")
	errNoPublishedRelease   = errors.New("no published GitHub release")
	errNoAttestation        = errors.New("no published attestation")
	errNoTrustedAttestation = errors.New("no trusted attestation")
	errAttestationRequest   = errors.New("attestation request failed")
	errDownloadFailed       = errors.New("download failed")
	errChecksumEntryMissing = errors.New("SHA256SUMS has no entry")
	errChecksumMismatch     = errors.New("downloaded binary does not match SHA256SUMS")
)

// UpgradeOptions selects the release to install and how to authenticate GitHub.
type UpgradeOptions struct {
	Repository string
	Version    string
	TokenFile  string
	Force      bool
	Prerelease bool
}

// Upgrade downloads, verifies and activates a GitHub release, rolling back on failure.
func (application *Upgrader) Upgrade(ctx context.Context, options UpgradeOptions) error {
	if err := validateStatePath(application.StateDir); err != nil {
		return err
	}
	if !Installed(application.StateDir) {
		return errNotInstalled
	}
	candidate, err := resolveUpgrade(ctx, options)
	if err != nil {
		return err
	}
	if !options.Force && application.Version == candidate.version {
		return writef(application.Out, "udm-iptv %s is already installed. Use --force to reinstall.\n", candidate.version)
	}
	if err := application.applyRelease(ctx, candidate); err != nil {
		return err
	}

	return writef(application.Out, "Upgraded udm-iptv to %s.\n", candidate.version)
}

type upgradeCandidate struct {
	client     *http.Client
	release    *github.RepositoryRelease
	owner      string
	repository string
	version    string
	stateDir   string
}

func resolveUpgrade(ctx context.Context, options UpgradeOptions) (upgradeCandidate, error) {
	owner, repository, err := splitRepository(options.Repository)
	if err != nil {
		return upgradeCandidate{}, err
	}
	token, err := upgradeToken(options.TokenFile)
	if err != nil {
		return upgradeCandidate{}, err
	}
	client := upgradeHTTPClient(token)
	release, err := fetchRelease(ctx, github.NewClient(client), owner, repository, options.Version, options.Prerelease)
	if err != nil {
		return upgradeCandidate{}, err
	}

	return upgradeCandidate{
		client:     client,
		release:    release,
		owner:      owner,
		repository: repository,
		version:    strings.TrimPrefix(release.GetTagName(), "v"),
	}, nil
}

// repositoryPart accepts the character set GitHub allows in an owner or
// repository name, so a flag value cannot reshape the attestation URL.
var repositoryPart = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

func splitRepository(spec string) (string, string, error) {
	owner, repository, found := strings.Cut(spec, "/")
	if !found || !repositoryPart.MatchString(owner) || !repositoryPart.MatchString(repository) {
		return "", "", fmt.Errorf("%w %q", errInvalidRepository, spec)
	}

	return owner, repository, nil
}

func upgradeToken(tokenFile string) (string, error) {
	if tokenFile == "" {
		return strings.TrimSpace(os.Getenv("GITHUB_TOKEN")), nil
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", fmt.Errorf("read GitHub token file %s: %w", tokenFile, err)
	}

	return strings.TrimSpace(string(data)), nil
}

func upgradeHTTPClient(token string) *http.Client {
	client := &http.Client{Timeout: upgradeClientTimeout}
	if token != "" {
		client.Transport = &bearerTransport{token: token, base: http.DefaultTransport}
	}

	return client
}

func fetchRelease(ctx context.Context, client *github.Client, owner, repository, version string, prerelease bool) (*github.RepositoryRelease, error) {
	var release *github.RepositoryRelease
	var err error
	if version == "" || version == "latest" {
		if prerelease {
			release, err = latestPublishedRelease(ctx, client, owner, repository)
		} else {
			release, _, err = client.Repositories.GetLatestRelease(ctx, owner, repository)
		}
	} else {
		tag := strings.TrimPrefix(version, "v")
		release, _, err = client.Repositories.GetReleaseByTag(ctx, owner, repository, "v"+tag)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve release: %w", err)
	}

	return release, nil
}

func latestPublishedRelease(ctx context.Context, client *github.Client, owner, repository string) (*github.RepositoryRelease, error) {
	releases, _, err := client.Repositories.ListReleases(ctx, owner, repository, &github.ListOptions{PerPage: releaseListPageSize})
	if err != nil {
		return nil, fmt.Errorf("list GitHub releases: %w", err)
	}
	release := newestPublishedRelease(releases)
	if release == nil {
		return nil, errNoPublishedRelease
	}

	return release, nil
}

func newestPublishedRelease(releases []*github.RepositoryRelease) *github.RepositoryRelease {
	for _, release := range releases {
		if release == nil || release.GetDraft() {
			continue
		}

		return release
	}

	return nil
}

func (application *Upgrader) applyRelease(ctx context.Context, candidate upgradeCandidate) error {
	candidate.stateDir = application.StateDir
	assetName := "udm-iptv-linux-" + runtime.GOARCH
	assetURL, checksumURL := releaseAssetURLs(candidate.release, assetName)
	if assetURL == "" || checksumURL == "" {
		return fmt.Errorf("%w: %s has no %s or SHA256SUMS", errReleaseAssetsMissing, candidate.release.GetTagName(), assetName)
	}
	directory, err := os.MkdirTemp("", "udm-iptv-upgrade-*")
	if err != nil {
		return fmt.Errorf("create upgrade download directory: %w", err)
	}
	defer removeAllIgnoringError(directory)
	if err := writef(application.Out, "Downloading udm-iptv %s...\n", candidate.version); err != nil {
		return err
	}
	binaryPath, err := downloadVerifiedAsset(ctx, candidate, directory, assetName, assetURL, checksumURL)
	if err != nil {
		return err
	}
	target := filepath.Join(application.StateDir, "bin", "udm-iptv")

	return activateUpgrade(ctx, binaryPath, target, candidate.version, systemUpgradeActions(application.Restart))
}

func downloadVerifiedAsset(ctx context.Context, candidate upgradeCandidate, directory, assetName, assetURL, checksumURL string) (string, error) {
	binaryPath := filepath.Join(directory, assetName)
	checksumPath := filepath.Join(directory, "SHA256SUMS")
	if err := download(ctx, candidate.client, assetURL, binaryPath, filemode.Executable); err != nil {
		return "", err
	}
	if err := download(ctx, candidate.client, checksumURL, checksumPath, filemode.PrivateFile); err != nil {
		return "", err
	}
	if err := verifyChecksum(binaryPath, checksumPath, assetName); err != nil {
		return "", err
	}
	if err := verifyAttestation(ctx, candidate, binaryPath); err != nil {
		return "", err
	}

	return binaryPath, nil
}

func verifyAttestation(ctx context.Context, candidate upgradeCandidate, binaryPath string) error {
	digest, err := fileDigest(filepath.Split(binaryPath))
	if err != nil {
		return err
	}
	bundles, err := fetchAttestations(ctx, candidate, digest)
	if err != nil {
		return err
	}
	if len(bundles) == 0 {
		return fmt.Errorf("%w for sha256:%s from %s/%s", errNoAttestation, digest, candidate.owner, candidate.repository)
	}
	verifier, policy, err := attestationVerifier(ctx, candidate, digest)
	if err != nil {
		return err
	}
	rejected := make([]error, 0, len(bundles))
	for _, attestation := range bundles {
		if _, err := verifier.Verify(attestation, policy); err != nil {
			rejected = append(rejected, err)

			continue
		}

		return nil
	}

	return errors.Join(append([]error{fmt.Errorf("%w from %s/%s covers sha256:%s", errNoTrustedAttestation, candidate.owner, candidate.repository, digest)}, rejected...)...)
}

func attestationVerifier(ctx context.Context, candidate upgradeCandidate, digest string) (*verify.Verifier, verify.PolicyBuilder, error) {
	options := tuf.DefaultOptions().
		WithCachePath(filepath.Join(candidate.stateDir, "sigstore", "tuf")).
		WithContext(ctx)
	trustedRoot, err := root.FetchTrustedRootWithOptions(options)
	if err != nil {
		return nil, verify.PolicyBuilder{}, fmt.Errorf("fetch sigstore trusted root: %w", err)
	}
	verifier, err := verify.NewVerifier(trustedRoot,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1))
	if err != nil {
		return nil, verify.PolicyBuilder{}, fmt.Errorf("build sigstore verifier: %w", err)
	}
	identity, err := workflowIdentity(candidate)
	if err != nil {
		return nil, verify.PolicyBuilder{}, err
	}
	raw, err := hex.DecodeString(digest)
	if err != nil {
		return nil, verify.PolicyBuilder{}, fmt.Errorf("decode asset digest %s: %w", digest, err)
	}

	return verifier, verify.NewPolicy(verify.WithArtifactDigest("sha256", raw), verify.WithCertificateIdentity(identity)), nil
}

func workflowIdentity(candidate upgradeCandidate) (verify.CertificateIdentity, error) {
	slug := regexp.QuoteMeta(candidate.owner + "/" + candidate.repository)
	san, err := verify.NewSANMatcher("", `^https://github\.com/`+slug+`/\.github/workflows/.+@refs/tags/.+$`)
	if err != nil {
		return verify.CertificateIdentity{}, fmt.Errorf("build attestation identity matcher: %w", err)
	}
	issuer, err := verify.NewIssuerMatcher(attestationIssuer, "")
	if err != nil {
		return verify.CertificateIdentity{}, fmt.Errorf("build attestation issuer matcher: %w", err)
	}
	identity, err := verify.NewCertificateIdentity(san, issuer, certificate.Extensions{
		SourceRepositoryURI: "https://github.com/" + candidate.owner + "/" + candidate.repository,
	})
	if err != nil {
		return verify.CertificateIdentity{}, fmt.Errorf("build attestation identity: %w", err)
	}

	return identity, nil
}

// fileDigest reads name through a root confined to directory, so hashing the
// downloaded asset cannot follow a link out of the download directory.
func fileDigest(directory, name string) (string, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", fmt.Errorf("open download directory %s: %w", directory, err)
	}
	defer closeIgnoringError(root)
	file, err := root.Open(name)
	if err != nil {
		return "", fmt.Errorf("open downloaded asset %s: %w", name, err)
	}
	defer closeIgnoringError(file)
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash downloaded asset %s: %w", name, err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fetchAttestations(ctx context.Context, candidate upgradeCandidate, digest string) ([]*bundle.Bundle, error) {
	refs, err := fetchAttestationRefs(ctx, candidate, digest)
	if err != nil {
		return nil, err
	}
	bundles := make([]*bundle.Bundle, 0, len(refs))
	for _, ref := range refs {
		parsed, ok := candidate.resolveBundle(ctx, ref)
		if ok {
			bundles = append(bundles, parsed)
		}
	}

	return bundles, nil
}

// attestationRef is one attestation, inlined by API version 2022-11-28 and
// addressed by URL from 2026-03-10 onwards.
type attestationRef struct {
	inline json.RawMessage
	source string
}

func fetchAttestationRefs(ctx context.Context, candidate upgradeCandidate, digest string) ([]attestationRef, error) {
	endpoint := url.URL{
		Scheme: "https",
		Host:   githubAPIHost,
		Path:   "/repos/" + candidate.owner + "/" + candidate.repository + "/attestations/sha256:" + digest,
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build attestation request for sha256:%s: %w", digest, err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-Github-Api-Version", githubAPIVersion)
	response, err := candidate.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch attestations for sha256:%s: %w", digest, err)
	}
	defer closeIgnoringError(response.Body)
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w for sha256:%s: %s", errAttestationRequest, digest, response.Status)
	}

	return decodeAttestations(io.LimitReader(response.Body, attestationResponseLimit), digest)
}

func decodeAttestations(reader io.Reader, digest string) ([]attestationRef, error) {
	var payload struct {
		Attestations []struct {
			Bundle    json.RawMessage `json:"bundle"`
			BundleURL string          `json:"bundle_url"`
		} `json:"attestations"`
	}
	if err := json.NewDecoder(reader).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode attestations for sha256:%s: %w", digest, err)
	}
	refs := make([]attestationRef, 0, len(payload.Attestations))
	for _, attestation := range payload.Attestations {
		refs = append(refs, attestationRef{inline: attestation.Bundle, source: attestation.BundleURL})
	}

	return refs, nil
}

// resolveBundle treats an unreadable attestation as covering nothing, so one
// bad entry cannot hide a valid one published beside it.
func (candidate upgradeCandidate) resolveBundle(ctx context.Context, ref attestationRef) (*bundle.Bundle, bool) {
	if len(ref.inline) > 0 {
		return parseBundle(ref.inline)
	}
	if ref.source == "" {
		return nil, false
	}
	body, err := candidate.fetchBundleBody(ctx, ref.source)
	if err != nil {
		return nil, false
	}

	return parseBundle(body)
}

func (candidate upgradeCandidate) fetchBundleBody(ctx context.Context, address string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("build attestation bundle request: %w", err)
	}
	response, err := candidate.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch attestation bundle: %w", err)
	}
	defer closeIgnoringError(response.Body)
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s", errAttestationRequest, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, attestationResponseLimit))
	if err != nil {
		return nil, fmt.Errorf("read attestation bundle: %w", err)
	}

	return body, nil
}

// Blob storage serves the bundle Snappy block compressed, with no framing
// header to detect it by.
func decompressBundle(data []byte) []byte {
	if trimmed := bytes.TrimLeft(data, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '{' {
		return data
	}
	size, err := snappy.DecodedLen(data)
	if err != nil || size > attestationDecodedLimit {
		return data
	}
	decoded, err := snappy.Decode(nil, data)
	if err != nil {
		return data
	}

	return decoded
}

func parseBundle(data []byte) (*bundle.Bundle, bool) {
	parsed := &bundle.Bundle{}
	if err := parsed.UnmarshalJSON(decompressBundle(data)); err != nil {
		return nil, false
	}

	return parsed, true
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
	if clone.URL.Hostname() == githubAPIHost {
		clone.Header.Set("Authorization", "Bearer "+transport.token)
		clone.Header.Set("X-Github-Api-Version", githubAPIVersion)
	}
	response, err := transport.base.RoundTrip(clone)
	if err != nil {
		return nil, fmt.Errorf("send request to %s: %w", clone.URL.Hostname(), err)
	}

	return response, nil
}

func download(ctx context.Context, client *http.Client, url, target string, mode os.FileMode) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build download request for %s: %w", url, err)
	}
	if request.URL.Hostname() == githubAPIHost {
		request.Header.Set("Accept", "application/octet-stream")
		request.Header.Set("X-Github-Api-Version", githubAPIVersion)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer closeIgnoringError(response.Body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s: %s", errDownloadFailed, url, response.Status)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".download-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", target, err)
	}
	name := temporary.Name()
	defer removeIgnoringError(name)
	if err := temporary.Chmod(mode); err != nil {
		closeIgnoringError(temporary)

		return fmt.Errorf("set file permissions for %s: %w", target, err)
	}
	if _, err := io.Copy(temporary, io.LimitReader(response.Body, releaseAssetLimit)); err != nil {
		closeIgnoringError(temporary)

		return fmt.Errorf("write %s from %s: %w", target, url, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", target, err)
	}
	if err := os.Rename(name, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}

	return nil
}

func verifyChecksum(binaryPath, checksumPath, assetName string) error {
	file, err := os.Open(checksumPath)
	if err != nil {
		return fmt.Errorf("open SHA256SUMS at %s: %w", checksumPath, err)
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
		return fmt.Errorf("read SHA256SUMS at %s: %w", checksumPath, err)
	}
	if expected == "" {
		return fmt.Errorf("%w for %s", errChecksumEntryMissing, assetName)
	}
	input, err := os.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("open downloaded asset %s: %w", binaryPath, err)
	}
	defer closeIgnoringError(input)
	hash := sha256.New()
	if _, err := io.Copy(hash, input); err != nil {
		return fmt.Errorf("hash downloaded asset %s: %w", binaryPath, err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return errChecksumMismatch
	}

	return nil
}

// Upgrader downloads verified releases and rolls back failed activation.
type Upgrader struct {
	Version, StateDir string
	Out               io.Writer
	Restart           func(context.Context, bool) error
}
