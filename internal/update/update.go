// Package update implements qmm's self-update command. It checks GitHub
// releases for a newer binary, downloads the asset matching the host platform,
// and atomically replaces the running executable.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	latestReleaseURL   = "https://api.github.com/repos/demongiggle/qmm/releases/latest"
	releasesURL        = "https://api.github.com/repos/demongiggle/qmm/releases"
	maxReleaseMetadata = 1 << 20
	maxUpdateSize      = 128 << 20
	maxReleasePages    = 10

	// A detached qmm server keeps running the old inode after an update.
	// Tell users how to pick up the new binary without killing their shells
	// implicitly (which would defeat the point of a multiplexer).
	restartHint = "A running qmm server keeps the previous version until it is restarted."
)

// Options controls an update run. The platform and executable fields are
// injectable so the update workflow can be tested without changing the host.
type Options struct {
	CurrentVersion string
	GOOS           string
	GOARCH         string
	ExecutablePath string
	ReleaseURL     string
	// ReleasesURL is the GitHub list-releases endpoint consulted when
	// --pre is passed. It defaults to releasesURL; ReleaseURL remains the
	// stable-only endpoint used otherwise.
	ReleasesURL string
	Client      *http.Client
	Stdout      io.Writer
	Stderr      io.Writer

	// DownloadURLValidator overrides the production GitHub URL validation for
	// tests. A nil validator requires an HTTPS github.com release URL.
	DownloadURLValidator func(string) error
}

type release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

type asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type updateArgs struct {
	arch string
	pre  bool
}

// Run executes qmm update with the supplied arguments.
func Run(ctx context.Context, arguments []string, options Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.ReleaseURL == "" {
		options.ReleaseURL = latestReleaseURL
	}
	if options.ReleasesURL == "" {
		options.ReleasesURL = releasesURL
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: 30 * time.Second}
	}

	parsed, err := parseArgs(arguments, options.Stderr)
	if err != nil {
		return err
	}

	targetOS, targetArch, err := targetPlatform(parsed.arch, options.GOOS, options.GOARCH)
	if err != nil {
		return err
	}

	var latest release
	if parsed.pre {
		latest, err = fetchLatestIncludingPrerelease(ctx, options.Client, options.ReleasesURL, targetOS, targetArch)
	} else {
		latest, err = fetchLatestRelease(ctx, options.Client, options.ReleaseURL)
	}
	if err != nil {
		return err
	}
	latestVersion, ok := parseVersion(latest.TagName)
	if !ok {
		return fmt.Errorf("latest release has unsupported version tag %q", latest.TagName)
	}

	if current, ok := parseVersion(options.CurrentVersion); ok {
		switch compareVersions(current, latestVersion) {
		case 0:
			fmt.Fprintf(options.Stdout, "qmm is already up to date (%s)\n", latest.TagName)
			return nil
		case 1:
			fmt.Fprintf(options.Stdout, "qmm %s is newer than the latest release %s\n", options.CurrentVersion, latest.TagName)
			return nil
		}
	}

	targetAsset, ok := findAsset(latest.Assets, targetOS, targetArch)
	if !ok {
		return fmt.Errorf("release %s has no asset for %s/%s", latest.TagName, targetOS, targetArch)
	}
	if targetAsset.BrowserDownloadURL == "" {
		return fmt.Errorf("release asset %q has no download URL", targetAsset.Name)
	}
	validator := options.DownloadURLValidator
	if validator == nil {
		validator = validateDownloadURL
	}
	if err := validator(targetAsset.BrowserDownloadURL); err != nil {
		return fmt.Errorf("invalid download URL for asset %q: %w", targetAsset.Name, err)
	}

	executable, target, mode, err := executableTarget(options.ExecutablePath)
	if err != nil {
		return err
	}
	temporary, err := downloadAsset(ctx, options.Client, targetAsset.BrowserDownloadURL, filepath.Dir(target), mode, validator)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()

	if err := installUnix(temporary, target, mode); err != nil {
		return err
	}
	removeTemporary = false
	fmt.Fprintf(options.Stdout, "updated qmm at %s from %s to %s\n", executable, options.CurrentVersion, latest.TagName)
	fmt.Fprintln(options.Stdout, restartHint)
	return nil
}

func parseArgs(arguments []string, stderr io.Writer) (updateArgs, error) {
	var result updateArgs
	flags := flag.NewFlagSet("qmm update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&result.arch, "arch", "", "target architecture (default: current architecture; e.g. arm64)")
	flags.BoolVar(&result.pre, "pre", false, "include pre-release versions when checking for updates")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: qmm update [--arch architecture] [--pre]")
		fmt.Fprintln(stderr, "\nDownload and install the latest qmm release for the current operating system.")
		fmt.Fprintln(stderr, "Supported architectures: amd64 and arm64; FreeBSD only supports amd64.")
		fmt.Fprintln(stderr, "Without --pre only stable releases are considered; --pre also considers pre-releases.")
	}
	if err := flags.Parse(arguments); err != nil {
		return updateArgs{}, err
	}
	if flags.NArg() != 0 {
		return updateArgs{}, fmt.Errorf("qmm update does not accept positional arguments")
	}
	return result, nil
}

func targetPlatform(spec, hostOS, hostArch string) (string, string, error) {
	targetOS, targetArch := strings.ToLower(strings.TrimSpace(hostOS)), normalizeArch(hostArch)
	spec = strings.ToLower(strings.TrimSpace(spec))
	if spec != "" {
		spec = strings.NewReplacer("/", "_", "-", "_").Replace(spec)
		parts := strings.Split(spec, "_")
		switch len(parts) {
		case 1:
			targetArch = normalizeArch(parts[0])
		case 2:
			targetOS, targetArch = parts[0], normalizeArch(parts[1])
		default:
			return "", "", fmt.Errorf("invalid architecture %q; use amd64, arm64, or os_arch", spec)
		}
	}
	if targetOS != strings.ToLower(hostOS) {
		return "", "", fmt.Errorf("target operating system %q does not match the host operating system %q", targetOS, hostOS)
	}
	if !supportedTarget(targetOS, targetArch) {
		return "", "", fmt.Errorf("unsupported update target %s/%s", targetOS, targetArch)
	}
	return targetOS, targetArch, nil
}

func normalizeArch(arch string) string {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return strings.ToLower(strings.TrimSpace(arch))
	}
}

// supportedTarget mirrors the platforms produced by scripts/build-all.sh.
// qmm uses Unix sockets and PTYs, so Windows executables are not built.
func supportedTarget(goos, goarch string) bool {
	if goarch != "amd64" && goarch != "arm64" {
		return false
	}
	if goos == "freebsd" {
		return goarch == "amd64"
	}
	return goos == "linux" || goos == "darwin"
}

func fetchLatestRelease(ctx context.Context, client *http.Client, endpoint string) (release, error) {
	request, err := newReleaseRequest(ctx, http.MethodGet, endpoint)
	if err != nil {
		return release{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return release{}, fmt.Errorf("check latest release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return release{}, fmt.Errorf("check latest release: GitHub returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var result release
	if err := json.NewDecoder(io.LimitReader(response.Body, maxReleaseMetadata)).Decode(&result); err != nil {
		return release{}, fmt.Errorf("decode latest release: %w", err)
	}
	if strings.TrimSpace(result.TagName) == "" {
		return release{}, errors.New("latest release response did not contain a tag")
	}
	return result, nil
}

// fetchLatestIncludingPrerelease returns the highest-versioned release,
// stable or pre-release, that ships an asset for the target platform.
// Drafts, untagged releases, unparseable tags, and releases without a
// matching asset are skipped. Stable releases are included so --pre still
// offers a newer stable release when it outranks any pre-release.
func fetchLatestIncludingPrerelease(ctx context.Context, client *http.Client, endpoint, goos, goarch string) (release, error) {
	var best release
	var bestVersion semanticVersion
	found := false
	next := endpoint
	for pages := 0; pages < maxReleasePages && next != ""; pages++ {
		batch, link, err := fetchReleasePage(ctx, client, next)
		if err != nil {
			return release{}, err
		}
		for _, candidate := range batch {
			if candidate.Draft || strings.TrimSpace(candidate.TagName) == "" {
				continue
			}
			version, ok := parseVersion(candidate.TagName)
			if !ok {
				continue
			}
			if _, ok := findAsset(candidate.Assets, goos, goarch); !ok {
				continue
			}
			if !found || compareVersions(bestVersion, version) < 0 {
				best, bestVersion, found = candidate, version, true
			}
		}
		next = link
	}
	if !found {
		return release{}, fmt.Errorf("no releases found with an asset for %s/%s", goos, goarch)
	}
	return best, nil
}

func fetchReleasePage(ctx context.Context, client *http.Client, endpoint string) (batch []release, next string, err error) {
	request, err := newReleaseRequest(ctx, http.MethodGet, endpoint)
	if err != nil {
		return nil, "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("check releases: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, "", fmt.Errorf("check releases: GitHub returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxReleaseMetadata)).Decode(&batch); err != nil {
		return nil, "", fmt.Errorf("decode releases: %w", err)
	}
	return batch, nextPageURL(response.Header.Get("Link")), nil
}

func newReleaseRequest(ctx context.Context, method, endpoint string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create release request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "qmm-update")
	return request, nil
}

// nextPageURL extracts the rel="next" URL from a GitHub paginated Link
// header, or returns "" when there is no further page.
func nextPageURL(header string) string {
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(part, ";")
		if len(segments) != 2 {
			continue
		}
		if strings.TrimSpace(segments[1]) != `rel="next"` {
			continue
		}
		next := strings.TrimSpace(segments[0])
		next = strings.TrimPrefix(next, "<")
		next = strings.TrimSuffix(next, ">")
		return strings.TrimSpace(next)
	}
	return ""
}

func findAsset(assets []asset, goos, goarch string) (asset, bool) {
	wanted := fmt.Sprintf("qmm_%s_%s", goos, goarch)
	for _, candidate := range assets {
		if candidate.Name == wanted {
			return candidate, true
		}
	}
	return asset{}, false
}

func validateDownloadURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil {
		return errors.New("release downloads must use https://github.com")
	}
	if !strings.HasPrefix(parsed.Path, "/demongiggle/qmm/releases/download/") {
		return errors.New("URL is not a qmm GitHub release download")
	}
	return nil
}

func executableTarget(path string) (executable, target string, mode os.FileMode, err error) {
	executable = path
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return "", "", 0, fmt.Errorf("find qmm executable: %w", err)
		}
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", "", 0, fmt.Errorf("resolve qmm executable: %w", err)
	}
	target = executable
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		target = resolved
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", "", 0, fmt.Errorf("inspect qmm executable %q: %w", target, err)
	}
	if !info.Mode().IsRegular() {
		return "", "", 0, fmt.Errorf("qmm executable %q is not a regular file", target)
	}
	mode = info.Mode().Perm()
	if mode == 0 {
		mode = 0755
	}
	return executable, target, mode, nil
}

func downloadAsset(ctx context.Context, client *http.Client, rawURL, directory string, mode os.FileMode, validator func(string) error) (path string, err error) {
	if validator == nil {
		validator = validateDownloadURL
	}
	if err := validator(rawURL); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("create update download request: %w", err)
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "qmm-update")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download update: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("download update: GitHub returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}

	temporaryFile, err := os.CreateTemp(directory, ".qmm-update-*")
	if err != nil {
		return "", fmt.Errorf("create update temporary file: %w", err)
	}
	path = temporaryFile.Name()
	temporaryPath := path
	defer func() {
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err = io.Copy(temporaryFile, io.LimitReader(response.Body, maxUpdateSize+1)); err != nil {
		temporaryFile.Close()
		return "", fmt.Errorf("write update temporary file: %w", err)
	}
	if info, statErr := temporaryFile.Stat(); statErr != nil {
		temporaryFile.Close()
		return "", fmt.Errorf("inspect update temporary file: %w", statErr)
	} else if info.Size() == 0 {
		temporaryFile.Close()
		return "", errors.New("downloaded update is empty")
	} else if info.Size() > maxUpdateSize {
		temporaryFile.Close()
		return "", fmt.Errorf("downloaded update exceeds %d bytes", maxUpdateSize)
	}
	if err = temporaryFile.Chmod(mode); err != nil {
		temporaryFile.Close()
		return "", fmt.Errorf("set update permissions: %w", err)
	}
	if err = temporaryFile.Sync(); err != nil {
		temporaryFile.Close()
		return "", fmt.Errorf("sync update temporary file: %w", err)
	}
	if err = temporaryFile.Close(); err != nil {
		return "", fmt.Errorf("close update temporary file: %w", err)
	}
	return path, nil
}

// installUnix replaces the executable with an atomic rename. Renaming over a
// running binary is safe on Unix: the detached qmm server keeps its old inode
// and continues serving the existing session.
func installUnix(temporary, target string, mode os.FileMode) error {
	if err := os.Chmod(temporary, mode); err != nil {
		return fmt.Errorf("set update permissions: %w", err)
	}
	if err := os.Rename(temporary, target); err != nil {
		return fmt.Errorf("replace qmm executable: %w", err)
	}
	// The rename is already durable enough for normal use; directory sync is a
	// best-effort improvement for filesystems that support it.
	directory, err := os.Open(filepath.Dir(target))
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

type semanticVersion struct {
	major int64
	minor int64
	patch int64
	pre   []string
}

func parseVersion(raw string) (semanticVersion, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 0 && (raw[0] == 'v' || raw[0] == 'V') {
		raw = raw[1:]
	}
	raw = strings.SplitN(raw, "+", 2)[0]
	parts := strings.SplitN(raw, "-", 2)
	core := strings.Split(parts[0], ".")
	if len(core) != 2 && len(core) != 3 {
		return semanticVersion{}, false
	}
	values := make([]int64, 3)
	for i, part := range core {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semanticVersion{}, false
		}
		value, err := strconv.ParseInt(part, 10, 64)
		if err != nil || value < 0 {
			return semanticVersion{}, false
		}
		values[i] = value
	}
	result := semanticVersion{major: values[0], minor: values[1], patch: values[2]}
	if len(parts) == 2 {
		if parts[1] == "" {
			return semanticVersion{}, false
		}
		for _, identifier := range strings.Split(parts[1], ".") {
			if identifier == "" {
				return semanticVersion{}, false
			}
			result.pre = append(result.pre, identifier)
		}
	}
	return result, true
}

func compareVersions(left, right semanticVersion) int {
	for _, pair := range [][2]int64{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(left.pre) == 0 && len(right.pre) == 0 {
		return 0
	}
	if len(left.pre) == 0 {
		return 1
	}
	if len(right.pre) == 0 {
		return -1
	}
	for i := 0; i < len(left.pre) && i < len(right.pre); i++ {
		leftID, rightID := left.pre[i], right.pre[i]
		leftNumber, leftErr := strconv.ParseInt(leftID, 10, 64)
		rightNumber, rightErr := strconv.ParseInt(rightID, 10, 64)
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber < rightNumber {
				return -1
			}
			if leftNumber > rightNumber {
				return 1
			}
		case leftErr == nil:
			return -1
		case rightErr == nil:
			return 1
		case leftID < rightID:
			return -1
		case leftID > rightID:
			return 1
		}
	}
	if len(left.pre) < len(right.pre) {
		return -1
	}
	if len(left.pre) > len(right.pre) {
		return 1
	}
	return 0
}
