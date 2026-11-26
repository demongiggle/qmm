package update

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseVersionAndCompare(t *testing.T) {
	tests := []struct {
		name   string
		left   string
		right  string
		result int
		valid  bool
	}{
		{name: "equal with v prefix", left: "v1.2.3", right: "1.2.3", result: 0, valid: true},
		{name: "two component release tags", left: "v0.6", right: "v0.7", result: -1, valid: true},
		{name: "patch update", left: "1.2.3", right: "1.2.4", result: -1, valid: true},
		{name: "major downgrade", left: "2.0.0", right: "1.9.9", result: 1, valid: true},
		{name: "release beats prerelease", left: "1.2.3-rc.1", right: "1.2.3", result: -1, valid: true},
		{name: "numbered prereleases", left: "v1.3.4-rc.2", right: "v1.3.4-rc.10", result: -1, valid: true},
		{name: "longer prerelease wins", left: "1.2.3-rc.1.1", right: "1.2.3-rc.1", result: 1, valid: true},
		{name: "dev is not semver", left: "dev", right: "1.2.3", valid: false},
		{name: "leading zeros rejected", left: "v01.2.3", right: "1.2.3", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, leftOK := parseVersion(test.left)
			right, rightOK := parseVersion(test.right)
			if leftOK != test.valid || !rightOK {
				t.Fatalf("parse validity = %v/%v, want %v/true", leftOK, rightOK, test.valid)
			}
			if test.valid && compareVersions(left, right) != test.result {
				t.Fatalf("compareVersions = %d, want %d", compareVersions(left, right), test.result)
			}
		})
	}
}

func TestTargetPlatform(t *testing.T) {
	tests := []struct {
		name     string
		spec     string
		hostOS   string
		hostArch string
		wantOS   string
		wantArch string
		wantErr  string
	}{
		{name: "host default", hostOS: "linux", hostArch: "amd64", wantOS: "linux", wantArch: "amd64"},
		{name: "architecture override", spec: "arm64", hostOS: "linux", hostArch: "amd64", wantOS: "linux", wantArch: "arm64"},
		{name: "full platform override", spec: "linux/aarch64", hostOS: "linux", hostArch: "amd64", wantOS: "linux", wantArch: "arm64"},
		{name: "different OS rejected", spec: "darwin_arm64", hostOS: "linux", hostArch: "amd64", wantErr: "does not match"},
		{name: "unsupported architecture", spec: "386", hostOS: "linux", hostArch: "amd64", wantErr: "unsupported"},
		{name: "windows is not built", spec: "windows_amd64", hostOS: "windows", hostArch: "amd64", wantErr: "unsupported"},
		{name: "freebsd arm64 is not built", spec: "freebsd_arm64", hostOS: "freebsd", hostArch: "arm64", wantErr: "unsupported"},
		{name: "freebsd amd64", hostOS: "freebsd", hostArch: "amd64", wantOS: "freebsd", wantArch: "amd64"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotOS, gotArch, err := targetPlatform(test.spec, test.hostOS, test.hostArch)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil || gotOS != test.wantOS || gotArch != test.wantArch {
				t.Fatalf("target = %s/%s, error = %v; want %s/%s", gotOS, gotArch, err, test.wantOS, test.wantArch)
			}
		})
	}
}

func TestFindAsset(t *testing.T) {
	assets := []asset{
		{Name: "qmm_linux_amd64"},
		{Name: "qmm_darwin_arm64"},
		{Name: "qmm_linux_amd64.tar.gz"},
	}
	if got, ok := findAsset(assets, "linux", "amd64"); !ok || got.Name != "qmm_linux_amd64" {
		t.Fatalf("linux asset = %+v, %v", got, ok)
	}
	if _, ok := findAsset(assets, "windows", "amd64"); ok {
		t.Fatal("found an asset that was not present")
	}
}

func TestParseArgs(t *testing.T) {
	parsed, err := parseArgs(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.pre || parsed.arch != "" {
		t.Fatalf("defaults = %+v, want no flags", parsed)
	}
	parsed, err = parseArgs([]string{"--arch", "arm64", "--pre"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.pre || parsed.arch != "arm64" {
		t.Fatalf("parsed = %+v, want arch arm64 with pre", parsed)
	}
	if _, err := parseArgs([]string{"extra"}, io.Discard); err == nil || !strings.Contains(err.Error(), "positional") {
		t.Fatalf("positional argument error = %v", err)
	}
	var usage strings.Builder
	if _, err := parseArgs([]string{"--help"}, &usage); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(usage.String(), "qmm update") || !strings.Contains(usage.String(), "--pre") {
		t.Fatalf("usage = %q", usage.String())
	}
}

func testOptions(target string, client *http.Client, output io.Writer) Options {
	return Options{
		CurrentVersion:       "v0.6",
		GOOS:                 "linux",
		GOARCH:               "amd64",
		ExecutablePath:       target,
		ReleaseURL:           "https://api.test/latest",
		ReleasesURL:          "https://api.test/releases",
		Client:               client,
		Stdout:               output,
		Stderr:               io.Discard,
		DownloadURLValidator: func(string) error { return nil },
	}
}

func TestRunDownloadsAndReplacesExecutable(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "qmm")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	var downloads atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/latest":
			return fakeResponse(http.StatusOK, `{"tag_name":"v0.7","assets":[{"name":"qmm_linux_amd64","browser_download_url":"https://api.test/download/linux"},{"name":"qmm_linux_arm64","browser_download_url":"https://api.test/download/arm64"}]}`), nil
		case "/download/linux":
			downloads.Add(1)
			response := fakeResponse(http.StatusOK, "new binary")
			if request.Header.Get("User-Agent") != "qmm-update" {
				t.Errorf("download User-Agent = %q", request.Header.Get("User-Agent"))
			}
			return response, nil
		default:
			return fakeResponse(http.StatusNotFound, "not found"), nil
		}
	})}

	var output strings.Builder
	if err := Run(context.Background(), nil, testOptions(target, client, &output)); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new binary" || downloads.Load() != 1 {
		t.Fatalf("content = %q, downloads = %d", content, downloads.Load())
	}
	if !strings.Contains(output.String(), "from v0.6 to v0.7") || !strings.Contains(output.String(), target) {
		t.Fatalf("output = %q", output.String())
	}
	if !strings.Contains(output.String(), restartHint) {
		t.Fatalf("output %q is missing the server restart hint", output.String())
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("mode = %o, want 755", info.Mode().Perm())
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("leftover files in target directory: %v", entries)
	}
}

func TestRunReplacesSymlinkTarget(t *testing.T) {
	targetDir := t.TempDir()
	real := filepath.Join(targetDir, "qmm-v0.6")
	link := filepath.Join(targetDir, "qmm")
	if err := os.WriteFile(real, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/latest":
			return fakeResponse(http.StatusOK, `{"tag_name":"v0.7","assets":[{"name":"qmm_linux_amd64","browser_download_url":"https://api.test/download/linux"}]}`), nil
		case "/download/linux":
			return fakeResponse(http.StatusOK, "new binary"), nil
		default:
			return fakeResponse(http.StatusNotFound, "not found"), nil
		}
	})}
	if err := Run(context.Background(), nil, testOptions(link, client, io.Discard)); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new binary" {
		t.Fatalf("content through symlink = %q", content)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink was replaced: %v", err)
	}
}

func TestRunSkipsDownloadWhenCurrentVersionMatches(t *testing.T) {
	var downloads atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/latest" {
			return fakeResponse(http.StatusOK, `{"tag_name":"1.1.0","assets":[]}`), nil
		}
		downloads.Add(1)
		return fakeResponse(http.StatusNotFound, "not found"), nil
	})}
	options := testOptions("", client, nil)
	options.CurrentVersion = "v1.1.0"
	var output strings.Builder
	options.Stdout = &output
	if err := Run(context.Background(), nil, options); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 0 || !strings.Contains(output.String(), "already up to date") {
		t.Fatalf("downloads = %d, output = %q", downloads.Load(), output.String())
	}
}

func TestRunKeepsNewerLocalVersion(t *testing.T) {
	var downloads atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/latest" {
			return fakeResponse(http.StatusOK, `{"tag_name":"v1.0.0","assets":[]}`), nil
		}
		downloads.Add(1)
		return fakeResponse(http.StatusNotFound, "not found"), nil
	})}
	options := testOptions("", client, nil)
	options.CurrentVersion = "v1.2.0"
	var output strings.Builder
	options.Stdout = &output
	if err := Run(context.Background(), nil, options); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 0 || !strings.Contains(output.String(), "is newer than the latest release") {
		t.Fatalf("downloads = %d, output = %q", downloads.Load(), output.String())
	}
}

func TestRunStableIgnoresPrerelease(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "qmm")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	stable := `{"tag_name":"v1.3.3","prerelease":false,"assets":[{"name":"qmm_linux_amd64","browser_download_url":"https://api.test/download/stable"}]}`
	list := `[{"tag_name":"v1.3.4-rc.1","draft":false,"prerelease":true,"assets":[{"name":"qmm_linux_amd64","browser_download_url":"https://api.test/download/rc"}]},` + stable + `]`
	var downloads atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/latest":
			return fakeResponse(http.StatusOK, stable), nil
		case "/releases":
			return fakeResponse(http.StatusOK, list), nil
		case "/download/rc":
			downloads.Add(1)
			return fakeResponse(http.StatusOK, "rc binary"), nil
		default:
			downloads.Add(1)
			return fakeResponse(http.StatusNotFound, "not found"), nil
		}
	})}

	// Default channel tracks the stable endpoint: already up to date.
	options := testOptions(target, client, nil)
	options.CurrentVersion = "v1.3.3"
	var output strings.Builder
	options.Stdout = &output
	if err := Run(context.Background(), nil, options); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 0 || !strings.Contains(output.String(), "already up to date (v1.3.3)") {
		t.Fatalf("downloads = %d, output = %q", downloads.Load(), output.String())
	}

	// Opt-in pre-release channel upgrades to the release candidate.
	output.Reset()
	options.Stdout = &output
	if err := Run(context.Background(), []string{"--pre"}, options); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "rc binary" || downloads.Load() != 1 {
		t.Fatalf("content = %q, downloads = %d", content, downloads.Load())
	}
	if !strings.Contains(output.String(), "from v1.3.3 to v1.3.4-rc.1") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunPreSkipsDraftsAndPrefersNewestStable(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "qmm")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	list := `[
		{"tag_name":"v9.9.9","draft":true,"prerelease":false,"assets":[{"name":"qmm_linux_amd64","browser_download_url":"https://api.test/download/draft"}]},
		{"tag_name":"v1.3.4-rc.1","draft":false,"prerelease":true,"assets":[{"name":"qmm_linux_amd64","browser_download_url":"https://api.test/download/rc"}]},
		{"tag_name":"v1.3.4","draft":false,"prerelease":false,"assets":[{"name":"qmm_linux_amd64","browser_download_url":"https://api.test/download/stable"}]},
		{"tag_name":"v1.3.5-rc.1","draft":false,"prerelease":true,"assets":[{"name":"qmm_darwin_amd64","browser_download_url":"https://api.test/download/other-platform"}]}
	]`
	var downloads atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/releases":
			return fakeResponse(http.StatusOK, list), nil
		case "/download/stable":
			downloads.Add(1)
			return fakeResponse(http.StatusOK, "stable binary"), nil
		default:
			downloads.Add(1)
			return fakeResponse(http.StatusNotFound, "not found"), nil
		}
	})}

	options := testOptions(target, client, nil)
	options.CurrentVersion = "v1.3.3"
	var output strings.Builder
	options.Stdout = &output
	if err := Run(context.Background(), []string{"--pre"}, options); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "stable binary" || downloads.Load() != 1 {
		t.Fatalf("content = %q, downloads = %d", content, downloads.Load())
	}
	if !strings.Contains(output.String(), "from v1.3.3 to v1.3.4") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunPreFollowsPagination(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "qmm")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/releases":
			if request.URL.Query().Get("page") == "2" {
				return fakeResponse(http.StatusOK, `[{"tag_name":"v1.1.0","assets":[{"name":"qmm_linux_amd64","browser_download_url":"https://api.test/download/new"}]}]`), nil
			}
			response := fakeResponse(http.StatusOK, `[{"tag_name":"v1.0.0","assets":[{"name":"qmm_linux_amd64","browser_download_url":"https://api.test/download/old"}]}]`)
			response.Header.Set("Link", `<https://api.test/releases?page=2>; rel="next", <https://api.test/releases?page=3>; rel="last"`)
			return response, nil
		case "/download/new":
			return fakeResponse(http.StatusOK, "new binary"), nil
		default:
			return fakeResponse(http.StatusNotFound, "not found"), nil
		}
	})}
	options := testOptions(target, client, nil)
	options.CurrentVersion = "v1.0.0"
	if err := Run(context.Background(), []string{"--pre"}, options); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new binary" {
		t.Fatalf("content = %q, want binary from second page", content)
	}
}

func TestRunPreReportsWhenNothingSuitable(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "qmm")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusOK, `[]`), nil
	})}
	err := Run(context.Background(), []string{"--pre"}, testOptions(target, client, io.Discard))
	if err == nil || !strings.Contains(err.Error(), "no releases found") {
		t.Fatalf("error = %v, want no-releases error", err)
	}
}

func TestRunReportsMissingAssetAndBadRelease(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "qmm")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "no matching asset", body: `{"tag_name":"v0.7","assets":[{"name":"qmm_darwin_arm64","browser_download_url":"https://github.com/demongiggle/qmm/releases/download/v0.7/qmm_darwin_arm64"}]}`, wantErr: "no asset for linux/amd64"},
		{name: "unparseable tag", body: `{"tag_name":"nightly","assets":[]}`, wantErr: "unsupported version tag"},
		{name: "missing download URL", body: `{"tag_name":"v0.7","assets":[{"name":"qmm_linux_amd64"}]}`, wantErr: "no download URL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return fakeResponse(http.StatusOK, test.body), nil
			})}
			err := Run(context.Background(), nil, testOptions(target, client, io.Discard))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
			content, readErr := os.ReadFile(target)
			if readErr != nil || string(content) != "old binary" {
				t.Fatalf("executable changed on failure: %q, %v", content, readErr)
			}
		})
	}
}

func TestRunAgainstLocalHTTPServer(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "qmm")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v0.9.0","assets":[{"name":"qmm_linux_amd64","browser_download_url":%q}]}`, server.URL+"/download")
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "downloaded binary")
	})

	options := testOptions(target, server.Client(), nil)
	options.ReleaseURL = server.URL + "/latest"
	options.DownloadURLValidator = func(raw string) error {
		if !strings.HasPrefix(raw, server.URL+"/download") {
			return errors.New("unexpected download host")
		}
		return nil
	}
	var output strings.Builder
	options.Stdout = &output
	if err := Run(context.Background(), nil, options); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "downloaded binary" || !strings.Contains(output.String(), "v0.9.0") {
		t.Fatalf("content = %q, output = %q", content, output.String())
	}
}

func TestRunRejectsForeignDownloadURL(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "qmm")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	var downloads atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/latest" {
			return fakeResponse(http.StatusOK, `{"tag_name":"v0.7","assets":[{"name":"qmm_linux_amd64","browser_download_url":"http://example.com/qmm"}]}`), nil
		}
		downloads.Add(1)
		return fakeResponse(http.StatusOK, "malicious binary"), nil
	})}
	options := testOptions(target, client, io.Discard)
	options.DownloadURLValidator = nil // use the production validator
	err := Run(context.Background(), nil, options)
	if err == nil || !strings.Contains(err.Error(), "invalid download URL") {
		t.Fatalf("error = %v, want invalid download URL", err)
	}
	if downloads.Load() != 0 {
		t.Fatalf("downloaded %d times despite an untrusted URL", downloads.Load())
	}
}

func TestRunRejectsEmptyDownload(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "qmm")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/latest":
			return fakeResponse(http.StatusOK, `{"tag_name":"v0.7","assets":[{"name":"qmm_linux_amd64","browser_download_url":"https://api.test/download/empty"}]}`), nil
		default:
			return fakeResponse(http.StatusOK, ""), nil
		}
	})}
	err := Run(context.Background(), nil, testOptions(target, client, io.Discard))
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v, want empty-download error", err)
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil || string(content) != "old binary" {
		t.Fatalf("executable changed on failure: %q, %v", content, readErr)
	}
	entries, readErr := os.ReadDir(targetDir)
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("temporary file not cleaned up: %v, %v", entries, readErr)
	}
}

func TestValidateDownloadURL(t *testing.T) {
	if err := validateDownloadURL("https://github.com/demongiggle/qmm/releases/download/v1.2.3/qmm_linux_amd64"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"http://github.com/demongiggle/qmm/releases/download/v1.2.3/qmm_linux_amd64",
		"https://example.com/demongiggle/qmm/releases/download/v1.2.3/qmm_linux_amd64",
		"https://github.com/other/repo/releases/download/v1.2.3/qmm_linux_amd64",
		"https://user@github.com/demongiggle/qmm/releases/download/v1.2.3/qmm_linux_amd64",
	} {
		if err := validateDownloadURL(raw); err == nil {
			t.Fatalf("validateDownloadURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestNextPageURL(t *testing.T) {
	header := `<https://api.github.com/repositories/1/releases?page=2>; rel="next", <https://api.github.com/repositories/1/releases?page=5>; rel="last"`
	if got := nextPageURL(header); got != "https://api.github.com/repositories/1/releases?page=2" {
		t.Fatalf("nextPageURL = %q", got)
	}
	if got := nextPageURL(`<https://api.github.com/x>; rel="last"`); got != "" {
		t.Fatalf("nextPageURL without next = %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func fakeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
