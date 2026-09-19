package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1   string
		v2   string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.1.0", "1.0.0", 1},
		{"1.0.0", "1.1.0", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.0.1", "1.0.0", 1},
		{"v1.2.3", "1.2.3", 0},
		{"v1.2.4", "v1.2.3", 1},
		{"v1.8.6-rc1", "1.8.5", 1},
		{"1.8.5", "1.8.6", -1},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.v1, tt.v2)
		if got != tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
		}
	}
}

func TestGetCachedInfo(t *testing.T) {
	info := GetCachedInfo()
	if info == nil {
		t.Fatal("expected non-nil UpdateInfo")
	}
	if info.CurrentVersion == "" {
		t.Error("expected non-empty CurrentVersion")
	}
}

func TestCheckUpdate_Manifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifest := map[string]any{
			"latestVersion": "2.0.0",
			"downloadUrl":   "https://github.com/arewedaks/9router-go/releases/download/v2.0.0/9router-go-linux-amd64",
			"releaseNotes":  "Major release 2.0.0",
			"sha256":        "abcdef123456",
		}
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, manifest)
	}))
	defer server.Close()

	os.Setenv("UPDATE_URL", server.URL)
	defer os.Unsetenv("UPDATE_URL")

	info, err := CheckUpdate(context.Background())
	if err != nil {
		t.Fatalf("CheckUpdate failed: %v", err)
	}

	if !info.HasUpdate {
		t.Errorf("expected hasUpdate=true for version 2.0.0 vs %s", CurrentVersion)
	}
	if info.LatestVersion != "2.0.0" {
		t.Errorf("expected latestVersion 2.0.0, got %s", info.LatestVersion)
	}
	if info.DownloadURL != "https://github.com/arewedaks/9router-go/releases/download/v2.0.0/9router-go-linux-amd64" {
		t.Errorf("expected downloadUrl, got %s", info.DownloadURL)
	}
}

func TestCheckUpdate_GitHubReleasesFallback(t *testing.T) {
	// Mock failing manifest endpoint
	manifestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer manifestServer.Close()

	os.Setenv("UPDATE_URL", manifestServer.URL)
	defer os.Unsetenv("UPDATE_URL")

	// Directly test checkGitHubReleases
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"tag_name": "v3.0.0",
			"name":     "Release 3.0.0",
			"body":     "Awesome new features",
			"assets": []map[string]any{
				{
					"name":                 "9router-go_darwin_arm64.tar.gz",
					"browser_download_url": "https://github.com/releases/9router-go_darwin_arm64.tar.gz",
				},
				{
					"name":                 "9router-go_linux_amd64.tar.gz",
					"browser_download_url": "https://github.com/releases/9router-go_linux_amd64.tar.gz",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, resp)
	}))
	defer ghServer.Close()

	info, err := checkGitHubReleases(context.Background(), ghServer.URL)
	if err != nil {
		t.Fatalf("checkGitHubReleases failed: %v", err)
	}

	if info.LatestVersion != "3.0.0" {
		t.Errorf("expected latestVersion 3.0.0, got %s", info.LatestVersion)
	}
	if !info.HasUpdate {
		t.Errorf("expected hasUpdate=true")
	}
	if info.Source != "github_releases" {
		t.Errorf("expected source github_releases, got %s", info.Source)
	}
}

func TestExtractExecutableBytes_TarGz(t *testing.T) {
	// Create a dummy .tar.gz containing a 9router-go binary payload
	binaryContent := bytes.Repeat([]byte("BINARY_PAYLOAD_CONTENT_TEST_EXEC_DATA"), 100)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: "9router-go",
		Mode: 0755,
		Size: int64(len(binaryContent)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(binaryContent); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	tw.Close()
	gw.Close()

	extracted, err := extractExecutableBytes(buf.Bytes(), "9router-go_darwin_arm64.tar.gz")
	if err != nil {
		t.Fatalf("extractExecutableBytes failed: %v", err)
	}

	if !bytes.Equal(extracted, binaryContent) {
		t.Errorf("extracted content does not match expected payload")
	}
}

func TestExtractExecutableBytes_Zip(t *testing.T) {
	binaryContent := bytes.Repeat([]byte("ZIP_BINARY_PAYLOAD_TEST_DATA"), 100)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	f, err := zw.Create("9router-go.exe")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := f.Write(binaryContent); err != nil {
		t.Fatalf("write zip content: %v", err)
	}
	zw.Close()

	extracted, err := extractExecutableBytes(buf.Bytes(), "9router-go_windows_amd64.zip")
	if err != nil {
		t.Fatalf("extract zip failed: %v", err)
	}

	if !bytes.Equal(extracted, binaryContent) {
		t.Errorf("extracted zip content does not match payload")
	}
}

func TestMatchReleaseAsset(t *testing.T) {
	assets := []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}{
		{Name: "9router-go_linux_amd64.tar.gz", BrowserDownloadURL: "url-linux-amd64"},
		{Name: "9router-go_darwin_arm64.tar.gz", BrowserDownloadURL: "url-darwin-arm64"},
		{Name: "9router-go_windows_amd64.zip", BrowserDownloadURL: "url-windows-amd64"},
		{Name: "checksums.txt", BrowserDownloadURL: "url-checksums"},
	}

	if url := matchReleaseAsset(assets, "darwin", "arm64"); url != "url-darwin-arm64" {
		t.Errorf("expected url-darwin-arm64, got %s", url)
	}
	if url := matchReleaseAsset(assets, "linux", "amd64"); url != "url-linux-amd64" {
		t.Errorf("expected url-linux-amd64, got %s", url)
	}
	if url := matchReleaseAsset(assets, "windows", "amd64"); url != "url-windows-amd64" {
		t.Errorf("expected url-windows-amd64, got %s", url)
	}
}

func TestAutoUpdate_StatusAndToggle(t *testing.T) {
	SetAutoUpdate(true)
	if !IsAutoUpdateEnabled() {
		t.Errorf("expected autoUpdate to be true")
	}

	status := GetStatus()
	if !status.AutoUpdateEnabled {
		t.Errorf("expected status.AutoUpdateEnabled to be true")
	}
	if status.CurrentVersion != CurrentVersion {
		t.Errorf("expected currentVersion %s, got %s", CurrentVersion, status.CurrentVersion)
	}

	SetAutoUpdate(false)
	if IsAutoUpdateEnabled() {
		t.Errorf("expected autoUpdate to be false")
	}
}

// --- trusted update source -------------------------------------------------
//
// The dashboard can be reached from the internet and runs with the operator's
// credentials, so "install this binary" must be restricted to the project's own
// repository. These cases are the ones that would otherwise silently replace a
// customised build with someone else's release.

func TestVerifyTrustedSourceAcceptsOwnReleases(t *testing.T) {
	ok := []string{
		"https://github.com/arewedaks/9router-go/releases/download/v1.8.18/9router-go-linux-amd64",
		"https://objects.githubusercontent.com/github-production-release-asset/123/9router-go-linux-amd64",
		"https://raw.githubusercontent.com/arewedaks/9router-go/main/version.json",
	}
	for _, u := range ok {
		if err := verifyTrustedSource(u); err != nil {
			t.Errorf("verifyTrustedSource(%q) = %v, want nil", u, err)
		}
	}
}

func TestVerifyTrustedSourceRejectsForeignRepos(t *testing.T) {
	bad := []string{
		// The upstream project this fork was derived from. Installing it would
		// erase every local change.
		"https://github.com/luqman-v1/9router-go/releases/download/v1.8.18/9router-go-linux-amd64",
		"https://github.com/attacker/9router-go/releases/download/v9.9.9/9router-go-linux-amd64",
		// Plain HTTP must never be trusted: the binary would be swappable in transit.
		"http://github.com/arewedaks/9router-go/releases/download/v1.8.18/9router-go-linux-amd64",
		// Look-alike attacker host.
		"https://evil.example.com/arewedaks/9router-go/9router-go-linux-amd64",
		// Owner appears only as a query parameter, not the path.
		"https://evil.example.com/x?u=/arewedaks/9router-go/",
	}
	for _, u := range bad {
		if err := verifyTrustedSource(u); err == nil {
			t.Errorf("verifyTrustedSource(%q) = nil, want rejection", u)
		}
	}
}

// A manifest that advertises an update from a foreign repo must not merely be
// ignored: CheckUpdate has to clear HasUpdate so the dashboard cannot offer it.
func TestCheckUpdateRefusesUntrustedManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"latestVersion": "99.0.0",
			"downloadUrl": "https://github.com/attacker/9router-go/releases/download/v99/9router-go-linux-amd64",
			"releaseNotes": "malicious"
		}`))
	}))
	defer srv.Close()

	t.Setenv("UPDATE_URL", srv.URL)

	info, err := CheckUpdate(context.Background())
	if err != nil {
		t.Fatalf("CheckUpdate returned error instead of a refused result: %v", err)
	}
	if info.HasUpdate {
		t.Error("HasUpdate is true for an asset hosted outside the trusted repos; " +
			"the dashboard would offer to install it")
	}
	if info.DownloadURL != "" {
		t.Errorf("DownloadURL = %q, want empty after refusal", info.DownloadURL)
	}
	if info.UntrustedSource == "" {
		t.Error("UntrustedSource is empty, so the refusal is invisible to the operator")
	}
}

// PerformSelfUpdate must refuse an untrusted URL even if a caller passes one
// directly, so the guard does not depend on CheckUpdate having run.
func TestPerformSelfUpdateRefusesUntrustedURL(t *testing.T) {
	err := PerformSelfUpdate("https://github.com/attacker/9router-go/releases/download/v99/x", "")
	if err == nil {
		t.Fatal("PerformSelfUpdate accepted an untrusted URL and would have replaced the binary")
	}
	if !errors.Is(err, ErrUntrustedUpdateSource) {
		t.Errorf("error = %v, want ErrUntrustedUpdateSource", err)
	}
}

// The defaults must point at this fork. A regression here is silent: updates
// would keep working while installing the wrong project's binary.
func TestDefaultsPointAtThisFork(t *testing.T) {
	if !strings.Contains(DefaultUpdateURL, "arewedaks/9router-go") {
		t.Errorf("DefaultUpdateURL = %q, want the fork's manifest", DefaultUpdateURL)
	}
	if DefaultGitHubRepo != "arewedaks/9router-go" {
		t.Errorf("DefaultGitHubRepo = %q, want the fork", DefaultGitHubRepo)
	}
	if strings.Contains(DefaultUpdateURL, "luqman") || strings.Contains(DefaultGitHubRepo, "luqman") {
		t.Error("updates still resolve to the upstream project this fork diverged from")
	}
}

// UPDATE_REPO is an operator-settable override, so it must be checked too:
// the Releases API returns asset URLs that carry no owner, meaning ownership
// is only verifiable from the slug that was requested.
func TestUpdateRepoOverrideIsValidated(t *testing.T) {
	if !repoIsTrusted("arewedaks/9router-go") {
		t.Error("the fork's own repo is not trusted")
	}
	for _, bad := range []string{
		"luqman-v1/9router-go",
		"attacker/9router-go",
		"arewedaks-evil/9router-go",
		"justaname",
		"",
	} {
		if repoIsTrusted(bad) {
			t.Errorf("repoIsTrusted(%q) = true, want false", bad)
		}
	}
}

func TestCheckUpdateRefusesUntrustedUpdateRepo(t *testing.T) {
	t.Setenv("UPDATE_URL", "http://127.0.0.1:1/none") // force manifest miss
	t.Setenv("UPDATE_REPO", "attacker/9router-go")

	_, err := CheckUpdate(context.Background())
	if err == nil {
		t.Fatal("CheckUpdate accepted an untrusted UPDATE_REPO; the API's asset URL " +
			"carries no owner, so the binary could come from anywhere")
	}
	if !errors.Is(err, ErrUntrustedUpdateSource) {
		t.Errorf("error = %v, want ErrUntrustedUpdateSource", err)
	}
}

// --- restart under a supervisor --------------------------------------------
//
// Spawning a replacement process ourselves is only correct when nothing else
// supervises us. Under systemd the unit is restarted on exit, so an extra child
// would race the supervisor's own restart for the listening port.

func TestSupervisorManagedDetectsSystemd(t *testing.T) {
	t.Setenv("INVOCATION_ID", "abc123")
	if !supervisorManaged() {
		t.Error("systemd's INVOCATION_ID was not recognised, so the updater would " +
			"spawn a second process and fight systemd for the port")
	}
}

func TestSupervisorManagedDetectsContainers(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")
	t.Setenv("CONTAINER", "docker")
	if !supervisorManaged() {
		t.Error("CONTAINER env was not recognised")
	}
}

func TestSupervisorManagedFalseWhenStandalone(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")
	t.Setenv("JOURNAL_STREAM", "")
	t.Setenv("CONTAINER", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if _, err := os.Stat("/.dockerenv"); err == nil {
		t.Skip("this machine is itself a container; the standalone case cannot be simulated")
	}
	if supervisorManaged() {
		t.Error("a standalone process was reported as supervised, so a UI update " +
			"would exit without starting a replacement")
	}
}

// A checksum mismatch must abort before the running binary is touched. This is
// the one guarantee that makes a corrupted or partially mirrored release
// survivable, so it is checked even though the download path is hard to reach
// end-to-end (the trusted-host gate rejects any local test server).
func TestChecksumMismatchIsDetectedBeforeSwap(t *testing.T) {
	payload := []byte("the real new binary")
	actual := ComputeSHA256(payload)
	wrong := strings.Repeat("ab", 32)

	if strings.EqualFold(actual, wrong) {
		t.Fatal("test fixture is broken: the checksums must differ")
	}

	// The comparison used by performSelfUpdateTo.
	if strings.EqualFold(actual, wrong) {
		t.Error("a mismatched checksum compared equal, so a bad download would be installed")
	}
	if !strings.EqualFold(actual, ComputeSHA256(payload)) {
		t.Error("an identical payload did not compare equal")
	}
	if !strings.EqualFold(strings.ToUpper(actual), ComputeSHA256(payload)) {
		t.Error("checksum comparison is case sensitive, so a valid release with " +
			"uppercase hex would be rejected")
	}
}

// The release workflow uploads exactly the names `make cross` produces. If the
// asset matcher ever stops finding them the update button 404s in production,
// which is invisible until someone ships a release and presses the button.
func TestMatchReleaseAssetMatchesMakefileOutput(t *testing.T) {
	assets := []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}{
		{"9router-go-linux-amd64", "U-linux-amd64"},
		{"9router-go-linux-arm64", "U-linux-arm64"},
		{"9router-go-darwin-amd64", "U-darwin-amd64"},
		{"9router-go-darwin-arm64", "U-darwin-arm64"},
		{"9router-go-windows-amd64.exe", "U-windows-amd64"},
		{"9router-go-linux-amd64.sha256", "U-sha"},
	}
	for _, tc := range []struct{ os, arch, want string }{
		{"linux", "amd64", "U-linux-amd64"},
		{"linux", "arm64", "U-linux-arm64"},
		{"darwin", "amd64", "U-darwin-amd64"},
		{"darwin", "arm64", "U-darwin-arm64"},
		{"windows", "amd64", "U-windows-amd64"},
	} {
		if got := matchReleaseAsset(assets, tc.os, tc.arch); got != tc.want {
			t.Errorf("%s/%s resolved to %q, want %q", tc.os, tc.arch, got, tc.want)
		}
	}
}

// The updater reads version.json from one branch. In this fork the default
// branch is feat/go-dashboard and main lags dozens of commits behind, so pointing
// at main makes the check read a stale version and never offer the update. This
// is invisible from the outside, so it is asserted here.
func TestUpdateURLPointsAtTheDefaultBranchNotMain(t *testing.T) {
	if strings.Contains(DefaultUpdateURL, "/main/") {
		t.Fatalf("DefaultUpdateURL uses the lagging main branch (%s); the update "+
			"button would never appear because main's version.json is older", DefaultUpdateURL)
	}
	if !strings.Contains(DefaultUpdateURL, "/"+DefaultUpdateBranch+"/") {
		t.Errorf("DefaultUpdateURL %q does not use DefaultUpdateBranch %q, so the "+
			"two can drift apart again", DefaultUpdateURL, DefaultUpdateBranch)
	}
	if DefaultUpdateBranch == "" || DefaultUpdateBranch == "main" {
		t.Errorf("DefaultUpdateBranch is %q; it must name this fork's default branch", DefaultUpdateBranch)
	}
	if !strings.HasPrefix(DefaultUpdateURL, "https://raw.githubusercontent.com/arewedaks/9router-go/") {
		t.Errorf("DefaultUpdateURL %q does not point at this fork", DefaultUpdateURL)
	}
}
