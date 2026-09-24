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
	"golang.org/x/mod/semver"

	"github.com/kjanat/udm-iptv/internal/filemode"
	"github.com/kjanat/udm-iptv/internal/proxycheck"
	"github.com/kjanat/udm-iptv/internal/telemetry"
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
	errDowngrade            = errors.New("downgrade refused")
)

// UpgradeOptions selects the release to install and how to authenticate GitHub.
type UpgradeOptions struct {
	Repository string
	Version    string
	TokenFile  string
	Force      bool
	Prerelease bool
	DryRun     bool
}

// Upgrade downloads, verifies and activates a GitHub release, rolling back on failure.
func (application *Upgrader) Upgrade(ctx context.Context, options UpgradeOptions) error {
	if options.DryRun {
		return application.preview(ctx, options)
	}
	candidate, plan, err := application.prepare(ctx, options)
	if err != nil {
		return err
	}
	if plan.note != "" {
		if err := writef(application.Out, "%s\n", plan.note); err != nil {
			return err
		}
	}
	if !plan.proceed {
		return nil
	}
	if err := proxycheck.Check(ctx); err != nil {
		return fmt.Errorf("check multicast proxy before upgrade: %w", err)
	}
	if plan.viaPackage {
		err = application.applyPackageRelease(ctx, candidate, options.Force)
	} else {
		err = application.applyStandaloneRelease(ctx, candidate)
	}
	if err != nil {
		return err
	}

	return writef(application.Out, "Upgraded udm-iptv to %s.\n", candidate.version)
}

func (application *Upgrader) preview(ctx context.Context, options UpgradeOptions) error {
	channel := channelFor(application.Version, options.Prerelease)
	if err := writef(application.Out, "Dry run: nothing is downloaded or installed.\nRunning udm-iptv %s on the %s channel.\n", application.Version, channel); err != nil {
		return err
	}
	candidate, plan, err := application.prepare(ctx, options)
	if err != nil {
		return err
	}

	return application.describe(candidate, plan)
}

func (application *Upgrader) describe(candidate upgradeCandidate, plan upgradePlan) error {
	if err := writef(application.Out, "Release found: udm-iptv %s.\n", candidate.version); err != nil {
		return err
	}
	if plan.note != "" {
		if err := writef(application.Out, "%s\n", plan.note); err != nil {
			return err
		}
	}
	if !plan.proceed {
		return nil
	}
	assets, err := candidateAssets(candidate, plan.viaPackage)
	if err != nil {
		return err
	}
	if plan.viaPackage {
		return writef(application.Out, "Would download %s and install it with apt-get.\n", assets.name)
	}

	return writef(application.Out, "Would download %s, replace %s and restart udm-iptv.service.\n", assets.name, installedTarget(application.StateDir))
}

func (application *Upgrader) prepare(ctx context.Context, options UpgradeOptions) (upgradeCandidate, upgradePlan, error) {
	if err := validateStatePath(application.StateDir); err != nil {
		return upgradeCandidate{}, upgradePlan{}, err
	}
	installed := application.installed
	if installed == nil {
		installed = Installed
	}
	if !installed(application.StateDir) {
		return upgradeCandidate{}, upgradePlan{}, errNotInstalled
	}
	resolve := application.resolve
	if resolve == nil {
		resolve = resolveUpgrade
	}
	candidate, err := resolve(ctx, options, channelFor(application.Version, options.Prerelease))
	if err != nil {
		return upgradeCandidate{}, upgradePlan{}, err
	}
	record, err := application.packageCommands().record(ctx)
	if err != nil {
		return upgradeCandidate{}, upgradePlan{}, err
	}
	plan, err := planUpgrade(application.Version, candidate.version, record, options.Force)
	if err != nil {
		return upgradeCandidate{}, upgradePlan{}, err
	}

	return candidate, plan, nil
}

type releaseChannel int

const (
	stableChannel releaseChannel = iota
	prereleaseChannel
)

func (channel releaseChannel) String() string {
	if channel == prereleaseChannel {
		return "prerelease"
	}

	return "stable"
}

func channelFor(running string, prerelease bool) releaseChannel {
	if prerelease || semver.Prerelease("v"+running) != "" {
		return prereleaseChannel
	}

	return stableChannel
}

func olderThan(candidate, running string) bool {
	candidate, running = "v"+candidate, "v"+running

	return semver.IsValid(candidate) && semver.IsValid(running) && semver.Compare(candidate, running) < 0
}

// upgradePlan is the decision an upgrade takes before touching anything.
type upgradePlan struct {
	proceed    bool
	viaPackage bool
	note       string
}

// planUpgrade compares the candidate with what runs and, on a dpkg-tracked
// installation, with what dpkg recorded. An executable swapped in behind
// dpkg's back leaves the record behind, and the package path repairs it
// without being forced.
func planUpgrade(running, candidate string, record PackageRecord, force bool) (upgradePlan, error) {
	if !force && olderThan(candidate, running) {
		return upgradePlan{}, fmt.Errorf("%w: udm-iptv %s is running, which is newer than %s; use --force to install it anyway", errDowngrade, running, candidate)
	}
	if !record.Owned() {
		if !force && running == candidate {
			return upgradePlan{note: "udm-iptv " + candidate + " is already installed. Use --force to reinstall."}, nil
		}

		return upgradePlan{proceed: true}, nil
	}
	recorded := record.ReleaseVersion()
	if !force && running == candidate && recorded == candidate {
		return upgradePlan{note: "udm-iptv " + candidate + " is already installed. Use --force to reinstall."}, nil
	}
	plan := upgradePlan{proceed: true, viaPackage: true}
	if recorded != running {
		plan.note = "dpkg recorded udm-iptv " + recorded + " while " + running + " is running; the package is reinstalled to bring the two in line."
	}

	return plan, nil
}

// applyStandaloneRelease replaces the executable this program installed.
func (application *Upgrader) applyStandaloneRelease(ctx context.Context, candidate upgradeCandidate) (result error) {
	release, err := AcquireLock(application.StateDir)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, release()) }()

	return application.applyRelease(ctx, candidate)
}

// applyPackageRelease hands a dpkg-tracked installation its new package, so
// the package database, maintainer scripts and shipped files advance with
// the executable. apt runs postinst, which installs under the operation
// lock, so this path holds none of its own.
func (application *Upgrader) applyPackageRelease(ctx context.Context, candidate upgradeCandidate, force bool) error {
	candidate.stateDir = application.StateDir
	assets, err := candidateAssets(candidate, true)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "udm-iptv-upgrade-*")
	if err != nil {
		return fmt.Errorf("create upgrade download directory: %w", err)
	}
	defer removeAllIgnoringError(directory)
	if err := os.Chmod(directory, filemode.SharedDir); err != nil {
		return fmt.Errorf("open upgrade download directory to apt: %w", err)
	}
	if err := writef(application.Out, "Downloading the udm-iptv %s package...\n", candidate.version); err != nil {
		return err
	}
	packagePath, err := application.fetchAsset()(ctx, candidate, directory, assets.name, assets.url, assets.checksumURL, filemode.SharedFile)
	if err != nil {
		return err
	}
	if err := writeString(application.Out, "Installing the package with apt-get...\n"); err != nil {
		return err
	}

	return application.packageCommands().install(ctx, packagePath, force, application.Out, application.Err)
}

func packageAssetName() string {
	return "udm-iptv-" + runtime.GOARCH + ".deb"
}

func standaloneAssetName() string {
	return "udm-iptv-linux-" + runtime.GOARCH
}

func installedTarget(stateDir string) string {
	return filepath.Join(stateDir, "bin", "udm-iptv")
}

type releaseAssets struct {
	name, url, checksumURL string
}

func candidateAssets(candidate upgradeCandidate, viaPackage bool) (releaseAssets, error) {
	name := standaloneAssetName()
	if viaPackage {
		name = packageAssetName()
	}
	assetURL, checksumURL := releaseAssetURLs(candidate.release, name)
	if assetURL == "" || checksumURL == "" {
		return releaseAssets{}, fmt.Errorf("%w: %s has no %s or SHA256SUMS", errReleaseAssetsMissing, candidate.release.GetTagName(), name)
	}

	return releaseAssets{name: name, url: assetURL, checksumURL: checksumURL}, nil
}

func (application *Upgrader) packageCommands() packageCommands {
	if application.packages.record == nil {
		return systemPackageCommands()
	}

	return application.packages
}

func (application *Upgrader) fetchAsset() assetFetcher {
	if application.fetch == nil {
		return downloadVerifiedAsset
	}

	return application.fetch
}

// assetFetcher downloads and verifies one release asset into directory.
type assetFetcher func(ctx context.Context, candidate upgradeCandidate, directory, assetName, assetURL, checksumURL string, mode os.FileMode) (string, error)

type upgradeCandidate struct {
	client     *http.Client
	release    *github.RepositoryRelease
	owner      string
	repository string
	version    string
	stateDir   string
}

func resolveUpgrade(ctx context.Context, options UpgradeOptions, channel releaseChannel) (upgradeCandidate, error) {
	owner, repository, err := splitRepository(options.Repository)
	if err != nil {
		return upgradeCandidate{}, err
	}
	token, err := upgradeToken(options.TokenFile)
	if err != nil {
		return upgradeCandidate{}, err
	}
	client := upgradeHTTPClient(token)
	release, err := fetchRelease(ctx, github.NewClient(client), owner, repository, options.Version, channel)
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
	transport := telemetry.HTTPTransport(http.DefaultTransport)
	if token != "" {
		transport = telemetry.HTTPTransport(&bearerTransport{token: token, base: http.DefaultTransport})
	}

	return &http.Client{Timeout: upgradeClientTimeout, Transport: transport}
}

func fetchRelease(ctx context.Context, client *github.Client, owner, repository, version string, channel releaseChannel) (*github.RepositoryRelease, error) {
	var release *github.RepositoryRelease
	var err error
	if version == "" || version == "latest" {
		if channel == prereleaseChannel {
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
	assets, err := candidateAssets(candidate, false)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "udm-iptv-upgrade-*")
	if err != nil {
		return fmt.Errorf("create upgrade download directory: %w", err)
	}
	defer removeAllIgnoringError(directory)
	if err := writef(application.Out, "Downloading udm-iptv %s...\n", candidate.version); err != nil {
		return err
	}
	binaryPath, err := application.fetchAsset()(ctx, candidate, directory, assets.name, assets.url, assets.checksumURL, filemode.Executable)
	if err != nil {
		return err
	}

	return activateUpgrade(ctx, binaryPath, installedTarget(application.StateDir), candidate.version, systemUpgradeActions(application.Restart))
}

func downloadVerifiedAsset(ctx context.Context, candidate upgradeCandidate, directory, assetName, assetURL, checksumURL string, mode os.FileMode) (string, error) {
	binaryPath := filepath.Join(directory, assetName)
	checksumPath := filepath.Join(directory, "SHA256SUMS")
	if err := download(ctx, candidate.client, assetURL, binaryPath, mode); err != nil {
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

// releaseWorkflow is the workflow that attests release assets.
const releaseWorkflow = ".github/workflows/release.yml"

// workflowIdentity binds the accepted signer to the release workflow run
// for the candidate's own tag, so a correctly signed asset from another
// release does not verify for this one.
func workflowIdentity(candidate upgradeCandidate) (verify.CertificateIdentity, error) {
	slug := regexp.QuoteMeta(candidate.owner + "/" + candidate.repository)
	tag := regexp.QuoteMeta(candidate.release.GetTagName())
	san, err := verify.NewSANMatcher("", `^https://github\.com/`+slug+`/`+regexp.QuoteMeta(releaseWorkflow)+`@refs/tags/`+tag+`$`)
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
	if clone.URL.Scheme == "https" && clone.URL.Hostname() == githubAPIHost {
		clone.Header.Set("Authorization", "Bearer "+transport.token)
		clone.Header.Set("X-Github-Api-Version", githubAPIVersion)
	}
	response, err := transport.base.RoundTrip(clone)
	if err != nil {
		return nil, fmt.Errorf("send request to %s: %w", clone.URL.Hostname(), err)
	}

	return response, nil
}

// GitHub redirects asset downloads to a URL whose query string is a
// short-lived signature, so error messages name the URL without it.
func displayURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.RawQuery, parsed.Fragment = "", ""

	return parsed.String()
}

func download(ctx context.Context, client *http.Client, address, target string, mode os.FileMode) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return fmt.Errorf("build download request for %s: %w", displayURL(address), err)
	}
	if request.URL.Hostname() == githubAPIHost {
		request.Header.Set("Accept", "application/octet-stream")
		request.Header.Set("X-Github-Api-Version", githubAPIVersion)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", displayURL(address), err)
	}
	defer closeIgnoringError(response.Body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s: %s", errDownloadFailed, displayURL(address), response.Status)
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

		return fmt.Errorf("write %s from %s: %w", target, displayURL(address), err)
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
	Out, Err          io.Writer
	Restart           func(context.Context, bool) error
	packages          packageCommands
	fetch             assetFetcher
	installed         func(string) bool
	resolve           func(context.Context, UpgradeOptions, releaseChannel) (upgradeCandidate, error)
}
