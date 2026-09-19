// Package updater handles app versioning, release update checks, safe self-updating, and background auto-update.
package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/log"
)

// CurrentVersion is the active 9router-go application version.
// Can be overridden at build time via -ldflags "-X 9router/proxy/internal/updater.CurrentVersion=1.8.8"
// Default fallback is read from version.json at init if not overridden.
var CurrentVersion = "1.8.17"

// DefaultUpdateBranch is the branch whose version.json the updater polls.
//
// This must be the repository's *default* branch, which in this fork is
// feat/go-dashboard — main lags far behind it. Pointing at main makes the
// updater read a stale version.json, conclude the running build is newer, and
// never show the update button: a silent failure with no error to debug.
const DefaultUpdateBranch = "feat/go-dashboard"

// DefaultUpdateURL is the primary remote version manifest URL.
//
// It must point at the fork this dashboard was built from: the updater replaces
// the running binary, so reading another project's manifest would silently
// downgrade a customised build to the upstream release. Override with UPDATE_URL.
var DefaultUpdateURL = "https://raw.githubusercontent.com/arewedaks/9router-go/" + DefaultUpdateBranch + "/version.json"

// DefaultGitHubRepo is the repository for GitHub Releases API fallback.
// Override with UPDATE_REPO.
var DefaultGitHubRepo = "arewedaks/9router-go"

// TrustedUpdateOwners are the GitHub owners whose release assets may be
// installed. A manifest or release pointing anywhere else is refused: the
// dashboard runs with the operator's credentials and must never be talked into
// replacing its own binary by a third party.
var TrustedUpdateOwners = []string{"arewedaks"}

// DefaultCheckInterval is the periodic background update check interval (6 hours).
const DefaultCheckInterval = 6 * time.Hour

var (
	cachedInfo        *UpdateInfo
	cacheMu           sync.RWMutex
	lastCheckTime     time.Time
	autoUpdateEnabled bool
	autoUpdateMu      sync.RWMutex
	updateInProgress  bool
	updateProgressMu  sync.Mutex
)

// ErrUntrustedUpdateSource is returned when a manifest or release points at a
// repository outside TrustedUpdateOwners.
var ErrUntrustedUpdateSource = fmt.Errorf("update source is not a trusted repository")

// verifyTrustedSource rejects a download URL that does not belong to one of
// TrustedUpdateOwners. The check is deliberately on the URL host and path, not
// on the manifest's own version fields, because the manifest is fetched over
// the network and is therefore attacker-controlled if DNS or TLS is subverted.
func verifyTrustedSource(downloadURL string) error {
	if downloadURL == "" {
		return nil // nothing to install yet; CheckUpdate reports HasUpdate separately
	}
	u, err := url.Parse(downloadURL)
	if err != nil {
		return fmt.Errorf("%w: unparsable download URL %q", ErrUntrustedUpdateSource, downloadURL)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("%w: download URL must be https, got %q", ErrUntrustedUpdateSource, u.Scheme)
	}

	host := strings.ToLower(u.Hostname())
	path := strings.ToLower(u.EscapedPath())

	// Only GitHub download hosts are acceptable. Release assets are served from
	// objects.githubusercontent.com, whose path has no owner segment; ownership
	// for those is established by the Releases API call in checkGitHubReleases.
	switch host {
	case "objects.githubusercontent.com", "codeload.github.com":
		return nil
	case "github.com", "raw.githubusercontent.com":
	default:
		return fmt.Errorf("%w: host %q is not a GitHub download host", ErrUntrustedUpdateSource, host)
	}

	for _, owner := range TrustedUpdateOwners {
		if strings.Contains(path, "/"+strings.ToLower(owner)+"/") {
			return nil
		}
	}
	return fmt.Errorf("%w: %q does not belong to any of %v", ErrUntrustedUpdateSource, downloadURL, TrustedUpdateOwners)
}

// repoIsTrusted reports whether an "owner/name" slug belongs to a trusted owner.
func repoIsTrusted(slug string) bool {
	parts := strings.SplitN(strings.TrimSpace(slug), "/", 2)
	if len(parts) != 2 {
		return false
	}
	for _, owner := range TrustedUpdateOwners {
		if strings.EqualFold(parts[0], owner) {
			return true
		}
	}
	return false
}

// UpdateInfo holds detailed version and asset information.
type UpdateInfo struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	HasUpdate      bool   `json:"hasUpdate"`
	DownloadURL    string `json:"downloadUrl"`
	ReleaseNotes   string `json:"releaseNotes"`
	GoVersion      string `json:"goVersion"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	CheckedAt      string `json:"checkedAt"`
	SHA256         string `json:"sha256,omitempty"`
	Source         string `json:"source,omitempty"` // "manifest" or "github_releases"
	// UntrustedSource is set when an update was advertised but refused because
	// the download URL pointed outside the trusted repositories. The dashboard
	// shows it so the refusal is visible rather than looking like "up to date".
	UntrustedSource string `json:"untrustedSource,omitempty"`
}

// UpdaterStatus represents the live background auto-update engine status.
type UpdaterStatus struct {
	CurrentVersion    string      `json:"currentVersion"`
	LatestVersion     string      `json:"latestVersion"`
	HasUpdate         bool        `json:"hasUpdate"`
	AutoUpdateEnabled bool        `json:"autoUpdateEnabled"`
	UpdateInProgress  bool        `json:"updateInProgress"`
	LastCheckTime     string      `json:"lastCheckTime,omitempty"`
	CheckInterval     string      `json:"checkInterval"`
	CachedInfo        *UpdateInfo `json:"cachedInfo,omitempty"`
}

// SetCachedInfoForTest replaces the cached result of the last version check.
// Test-only seam: it lets a handler test present a check outcome (including a
// refused one) without reaching the network or waiting for the background
// interval to fire.
func SetCachedInfoForTest(info *UpdateInfo) {
	cacheMu.Lock()
	cachedInfo = info
	lastCheckTime = time.Now()
	cacheMu.Unlock()
}

// SetAutoUpdate sets the auto-update flag in memory.
func SetAutoUpdate(enabled bool) {
	autoUpdateMu.Lock()
	defer autoUpdateMu.Unlock()
	autoUpdateEnabled = enabled
}

// IsAutoUpdateEnabled returns whether auto-update is currently active.
func IsAutoUpdateEnabled() bool {
	autoUpdateMu.RLock()
	defer autoUpdateMu.RUnlock()
	return autoUpdateEnabled
}

// GetCachedInfo returns the latest cached UpdateInfo or a default.
func GetCachedInfo() *UpdateInfo {
	cacheMu.RLock()
	defer cacheMu.RUnlock()

	if cachedInfo != nil {
		return cachedInfo
	}

	return &UpdateInfo{
		CurrentVersion: CurrentVersion,
		LatestVersion:  CurrentVersion,
		HasUpdate:      false,
		GoVersion:      runtime.Version(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
	}
}

// GetStatus returns the complete updater subsystem status.
func GetStatus() *UpdaterStatus {
	cacheMu.RLock()
	info := cachedInfo
	lastCheck := ""
	if !lastCheckTime.IsZero() {
		lastCheck = lastCheckTime.UTC().Format(time.RFC3339)
	}
	cacheMu.RUnlock()

	updateProgressMu.Lock()
	inProg := updateInProgress
	updateProgressMu.Unlock()

	latestVer := CurrentVersion
	hasUp := false
	if info != nil {
		latestVer = info.LatestVersion
		hasUp = info.HasUpdate
	}

	return &UpdaterStatus{
		CurrentVersion:    CurrentVersion,
		LatestVersion:     latestVer,
		HasUpdate:         hasUp,
		AutoUpdateEnabled: IsAutoUpdateEnabled(),
		UpdateInProgress:  inProg,
		LastCheckTime:     lastCheck,
		CheckInterval:     DefaultCheckInterval.String(),
		CachedInfo:        info,
	}
}

// CheckUpdate queries remote version sources (manifest or GitHub Releases API) and compares semver.
func CheckUpdate(ctx context.Context) (*UpdateInfo, error) {
	updateURL := os.Getenv("UPDATE_URL")
	if updateURL == "" {
		updateURL = DefaultUpdateURL
	}

	// 1. Try manifest URL first
	info, err := checkManifest(ctx, updateURL)
	if err == nil && info != nil {
		// A manifest can only carry one URL, and that URL is usually the release
		// page rather than a per-platform binary. Downloading it would fetch HTML,
		// so when it is not a direct asset, ask the Releases API for the real one.
		if info.HasUpdate && !looksLikeDirectAsset(info.DownloadURL) {
			if resolved := resolveReleaseAsset(ctx, info.DownloadURL); resolved != "" {
				info.DownloadURL = resolved
			}
		}
		if tErr := verifyTrustedSource(info.DownloadURL); tErr != nil && info.HasUpdate {
			log.Warn("updater", "refusing update from untrusted manifest",
				"url", info.DownloadURL, "error", tErr)
			info.HasUpdate = false
			info.DownloadURL = ""
			info.UntrustedSource = tErr.Error()
		}
		cacheMu.Lock()
		cachedInfo = info
		lastCheckTime = time.Now()
		cacheMu.Unlock()
		return info, nil
	}

	// 2. Fallback to GitHub Releases API
	repo := os.Getenv("UPDATE_REPO")
	if repo == "" {
		repo = DefaultGitHubRepo
	}
	// The API response's asset URLs carry no owner, so ownership can only be
	// established here. Reject an override pointing at somebody else's repo.
	if !repoIsTrusted(repo) {
		return nil, fmt.Errorf("%w: UPDATE_REPO %q is not a trusted repository", ErrUntrustedUpdateSource, repo)
	}
	ghURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	log.Debug("updater", "checking github releases fallback", "repo", repo)

	ghInfo, ghErr := checkGitHubReleases(ctx, ghURL)
	if ghErr == nil && ghInfo != nil {
		if tErr := verifyTrustedSource(ghInfo.DownloadURL); tErr != nil && ghInfo.HasUpdate {
			log.Warn("updater", "refusing update from untrusted release",
				"url", ghInfo.DownloadURL, "error", tErr)
			ghInfo.HasUpdate = false
			ghInfo.DownloadURL = ""
			ghInfo.UntrustedSource = tErr.Error()
		}
		cacheMu.Lock()
		cachedInfo = ghInfo
		lastCheckTime = time.Now()
		cacheMu.Unlock()
		return ghInfo, nil
	}

	if err != nil {
		return nil, fmt.Errorf("check update failed: manifest error (%v), github releases error (%v)", err, ghErr)
	}
	return nil, ghErr
}

func checkManifest(ctx context.Context, url string) (*UpdateInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var remote struct {
		Version       string            `json:"version"`
		LatestVersion string            `json:"latestVersion"`
		DownloadURL   string            `json:"downloadUrl"`
		DownloadURLs  map[string]string `json:"downloadUrls"`
		ReleaseNotes  string            `json:"releaseNotes"`
		SHA256        string            `json:"sha256"`
	}
	if err := json.Unmarshal(body, &remote); err != nil {
		return nil, err
	}

	versionStr := remote.LatestVersion
	if versionStr == "" {
		versionStr = remote.Version
	}
	if versionStr == "" {
		return nil, fmt.Errorf("manifest missing version field")
	}

	latestVersion := strings.TrimPrefix(versionStr, "v")
	current := strings.TrimPrefix(CurrentVersion, "v")
	hasUpdate := CompareVersions(latestVersion, current) > 0

	platformKey := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	downloadURL := ""
	if remote.DownloadURLs != nil {
		downloadURL = remote.DownloadURLs[platformKey]
		if downloadURL == "" {
			downloadURL = remote.DownloadURLs["default"]
		}
	}
	if downloadURL == "" {
		downloadURL = remote.DownloadURL
	}

	return &UpdateInfo{
		CurrentVersion: CurrentVersion,
		LatestVersion:  latestVersion,
		HasUpdate:      hasUpdate,
		DownloadURL:    downloadURL,
		ReleaseNotes:   remote.ReleaseNotes,
		GoVersion:      runtime.Version(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
		SHA256:         remote.SHA256,
		Source:         "manifest",
	}, nil
}

// looksLikeDirectAsset reports whether a URL points at a file we can download
// rather than at a human-facing release page.
func looksLikeDirectAsset(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	// github.com/<owner>/<repo>/releases/download/<tag>/<file> is a real asset.
	return strings.Contains(u.Path, "/releases/download/")
}

// resolveReleaseAsset turns a release page URL into the download URL of the
// asset built for this platform. It returns "" when the page is not a GitHub
// release or has no matching asset, in which case the caller keeps the original
// URL (and the trusted-source guard still applies to it).
func resolveReleaseAsset(ctx context.Context, releaseURL string) string {
	u, err := url.Parse(releaseURL)
	if err != nil || u.Host != "github.com" {
		return ""
	}
	// /<owner>/<repo>/releases/tag/<tag>
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[2] != "releases" {
		return ""
	}
	repo := parts[0] + "/" + parts[1]
	if !repoIsTrusted(repo) {
		return ""
	}

	tag := "latest"
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	if len(parts) >= 5 && parts[3] == "tag" {
		tag = parts[4]
		apiURL = fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repo, url.PathEscape(tag))
	}
	log.Debug("updater", "resolving release asset", "repo", repo, "tag", tag)

	info, err := checkGitHubReleases(ctx, apiURL)
	if err != nil || info == nil {
		log.Warn("updater", "could not resolve release asset", "repo", repo, "error", err)
		return ""
	}
	return info.DownloadURL
}

func checkGitHubReleases(ctx context.Context, apiURL string) (*UpdateInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "9router-go/"+CurrentVersion)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases HTTP %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Name    string `json:"name"`
		Body    string `json:"body"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}

	if err := json.UnmarshalRead(resp.Body, &release); err != nil {
		return nil, err
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(CurrentVersion, "v")
	hasUpdate := CompareVersions(latestVersion, current) > 0

	downloadURL := matchReleaseAsset(release.Assets, runtime.GOOS, runtime.GOARCH)

	return &UpdateInfo{
		CurrentVersion: CurrentVersion,
		LatestVersion:  latestVersion,
		HasUpdate:      hasUpdate,
		DownloadURL:    downloadURL,
		ReleaseNotes:   release.Body,
		GoVersion:      runtime.Version(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
		Source:         "github_releases",
	}, nil
}

func archAliases(archKey string) []string {
	archKey = strings.ToLower(archKey)
	switch archKey {
	case "amd64", "x86_64", "x64":
		return []string{"amd64", "x86_64", "x64"}
	case "arm64", "aarch64":
		return []string{"arm64", "aarch64"}
	case "386", "x86", "i386":
		return []string{"386", "x86", "i386"}
	default:
		return []string{archKey}
	}
}

// matchReleaseAsset finds the best matching asset URL for target OS and Architecture.
func matchReleaseAsset(assets []struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}, targetOS, targetArch string) string {
	osKey := strings.ToLower(targetOS)
	archNames := archAliases(targetArch)

	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		// A checksum file has the same os/arch in its name, so it must be
		// rejected first: downloading it as the binary would replace the proxy
		// with a hash string.
		if isChecksumAsset(name) {
			continue
		}
		// Check OS match
		if !strings.Contains(name, osKey) {
			continue
		}
		// Check Arch match
		for _, arch := range archNames {
			if strings.Contains(name, arch) {
				return asset.BrowserDownloadURL
			}
		}
	}

	// Fallback to first asset that is not a checksum sidecar.
	for _, asset := range assets {
		if !isChecksumAsset(strings.ToLower(asset.Name)) {
			return asset.BrowserDownloadURL
		}
	}
	return ""
}

// isChecksumAsset reports whether an asset name is a sidecar checksum rather
// than a runnable binary.
func isChecksumAsset(lowerName string) bool {
	for _, suffix := range []string{".sha256", ".sha512", ".sha1", ".md5", ".txt"} {
		if strings.HasSuffix(lowerName, suffix) {
			return true
		}
	}
	return false
}

// PerformSelfUpdate downloads, decompresses (tar.gz/zip if needed), verifies, and safely replaces the active binary.
// When expectedSHA256 is non-empty, the downloaded asset (after archive extraction) is verified against it before
// the binary is written to disk; a mismatch aborts the update and keeps the running binary intact.
func PerformSelfUpdate(downloadURL, expectedSHA256 string) error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve symlink path: %w", err)
	}
	return performSelfUpdateTo(downloadURL, expectedSHA256, execPath)
}

// performSelfUpdateTo installs the update over the given path.
//
// Split from PerformSelfUpdate so the download/verify/swap path can be tested
// against a temporary file: the alternative is a test that overwrites the
// running test binary, which would corrupt the run it is meant to check.
func performSelfUpdateTo(downloadURL, expectedSHA256, execPath string) error {
	if downloadURL == "" {
		return fmt.Errorf("missing download URL for platform %s_%s", runtime.GOOS, runtime.GOARCH)
	}

	// Last line of defence: even a handler-level mistake must not let an
	// untrusted URL overwrite the running binary.
	if err := verifyTrustedSource(downloadURL); err != nil {
		return err
	}

	updateProgressMu.Lock()
	if updateInProgress {
		updateProgressMu.Unlock()
		return fmt.Errorf("update already in progress")
	}
	updateInProgress = true
	updateProgressMu.Unlock()

	defer func() {
		updateProgressMu.Lock()
		updateInProgress = false
		updateProgressMu.Unlock()
	}()

	log.Info("updater", "downloading update asset", "url", downloadURL, "target", execPath)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download binary asset: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download asset returned status %d", resp.StatusCode)
	}

	rawBytes, err := io.ReadAll(io.LimitReader(resp.Body, 150<<20)) // 150MB limit
	if err != nil {
		return fmt.Errorf("read downloaded asset: %w", err)
	}

	// Extract executable bytes from archive or raw binary
	binaryBytes, err := extractExecutableBytes(rawBytes, downloadURL)
	if err != nil {
		return fmt.Errorf("extract executable: %w", err)
	}

	// Verify SHA256 checksum when the manifest provides one
	if expectedSHA256 != "" {
		actualSHA := ComputeSHA256(binaryBytes)
		if !strings.EqualFold(actualSHA, expectedSHA256) {
			return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expectedSHA256, actualSHA)
		}
		log.Info("updater", "SHA256 checksum verified", "sha256", actualSHA)
	}

	// Create temporary binary file in the target directory
	dir := filepath.Dir(execPath)
	tmpFile, err := os.CreateTemp(dir, ".9router-go-update-*.tmp")
	if err != nil {
		// The most common cause by far is a hardened unit that mounts the install
		// directory read-only (systemd ProtectSystem=strict) or a binary owned by
		// another user. The raw error does not say which, and the fix is a config
		// change the operator has to make, so name it here.
		if errors.Is(err, fs.ErrPermission) || strings.Contains(err.Error(), "read-only") {
			return fmt.Errorf("cannot write to %s: %w\n"+
				"the install directory is not writable by this process. "+
				"If running under systemd, add ReadWritePaths=%s to the unit; "+
				"otherwise run the update with the same user that owns the binary",
				dir, err, dir)
		}
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(binaryBytes); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write binary to temp file: %w", err)
	}
	tmpFile.Close()

	// Ensure executable permissions (0755)
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("chmod executable: %w", err)
	}

	// Replace running binary atomically
	oldPath := execPath + ".old"
	_ = os.Remove(oldPath)

	if err := os.Rename(execPath, oldPath); err != nil {
		return fmt.Errorf("backup active binary: %w", err)
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		// Rollback on failure
		_ = os.Rename(oldPath, execPath)
		return fmt.Errorf("swap binary asset: %w", err)
	}

	_ = os.Remove(oldPath)
	log.Info("updater", "self-update applied successfully!", "binary", execPath)
	return nil
}

// extractExecutableBytes handles .tar.gz, .zip, and raw binaries.
// Uses a scoring heuristic to prefer the actual executable binary over
// README, LICENSE, checksum, or other non-executable files in the archive.
func extractExecutableBytes(data []byte, filenameOrURL string) ([]byte, error) {
	lower := strings.ToLower(filenameOrURL)

	// 1. Handle .tar.gz or .tgz (or gzip magic bytes)
	if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") || isGzip(data) {
		gzReader, err := gzip.NewReader(bytes.NewReader(data))
		if err == nil {
			defer gzReader.Close()
			tarReader := tar.NewReader(gzReader)
			var best []byte
			bestScore := 0
			for {
				header, err := tarReader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return nil, fmt.Errorf("read tar entry: %w", err)
				}
				if header.Typeflag != tar.TypeReg {
					continue
				}
				extracted, err := io.ReadAll(tarReader)
				if err != nil {
					continue
				}
				score := scoreArchiveEntry(header.Name, extracted)
				if score > bestScore {
					bestScore = score
					best = extracted
				}
			}
			if bestScore >= 3 && len(best) > 1024 {
				return best, nil
			}
		}
	}

	// 2. Handle .zip (or ZIP magic bytes)
	if strings.HasSuffix(lower, ".zip") || isZip(data) {
		zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err == nil {
			var best []byte
			bestScore := 0
			for _, file := range zipReader.File {
				if file.FileInfo().IsDir() {
					continue
				}
				rc, err := file.Open()
				if err != nil {
					continue
				}
				extracted, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					continue
				}
				score := scoreArchiveEntry(file.Name, extracted)
				if score > bestScore {
					bestScore = score
					best = extracted
				}
			}
			if bestScore >= 3 && len(best) > 1024 {
				return best, nil
			}
		}
	}

	// 3. Raw executable binary — apply magic byte check
	if isELF(data) || isMachO(data) || isPE(data) {
		return data, nil
	}
	return data, nil
}

// scoreArchiveEntry returns a confidence score for an archive entry being the
// right binary. Higher = more likely to be the target executable.
func scoreArchiveEntry(name string, content []byte) int {
	score := 0
	low := strings.ToLower(name)

	// Prefer entries containing the project name over generic files
	if strings.Contains(low, "9router-go") || strings.Contains(low, "9router_go") {
		score += 10
	}

	// Match target OS
	if strings.Contains(low, runtime.GOOS) {
		score += 3
	}

	// Match target architecture
	for _, a := range archAliases(runtime.GOARCH) {
		if strings.Contains(low, strings.ToLower(a)) {
			score += 3
			break
		}
	}

	// Penalize non-executable extensions
	if strings.HasSuffix(low, ".md") || strings.HasSuffix(low, ".txt") ||
		strings.HasSuffix(low, ".sha256") || strings.HasSuffix(low, ".md5") ||
		strings.HasSuffix(low, ".yaml") || strings.HasSuffix(low, ".yml") ||
		strings.HasSuffix(low, ".json") || strings.HasSuffix(low, ".toml") {
		score -= 5
	}

	// Penalize well-known doc/asset names
	base := strings.TrimSuffix(low, ".exe")
	if base == "readme" || base == "license" || base == "changelog" ||
		base == "contributing" || base == "version" || base == "manifest" {
		score -= 10
	}

	// Bonus for executable magic bytes
	if isELF(content) || isMachO(content) || isPE(content) {
		score += 8
	}

	return score
}

func isELF(data []byte) bool {
	return len(data) > 4 && data[0] == 0x7f && data[1] == 'E' && data[2] == 'L' && data[3] == 'F'
}

func isMachO(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	// Mach-O 32-bit (MH_MAGIC / MH_CIGAM)
	if data[0] == 0xfe && data[1] == 0xed && data[2] == 0xfa && data[3] == 0xce {
		return true
	}
	if data[0] == 0xce && data[1] == 0xfa && data[2] == 0xed && data[3] == 0xfe {
		return true
	}
	// Mach-O 64-bit (MH_MAGIC_64 / MH_CIGAM_64)
	if data[0] == 0xfe && data[1] == 0xed && data[2] == 0xfa && data[3] == 0xcf {
		return true
	}
	if data[0] == 0xcf && data[1] == 0xfa && data[2] == 0xed && data[3] == 0xfe {
		return true
	}
	return false
}

func isPE(data []byte) bool {
	return len(data) > 2 && data[0] == 'M' && data[1] == 'Z'
}

func isGzip(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

func isZip(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 0x03 && data[3] == 0x04
}

// ComputeSHA256 returns hex-encoded sha256 checksum of data.
func ComputeSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// StartBackgroundCheck initiates recurring background update check and executes auto-update when enabled.
func StartBackgroundCheck(ctx context.Context, initialAutoUpdate bool) {
	SetAutoUpdate(initialAutoUpdate)

	// Check custom interval from env
	interval := DefaultCheckInterval
	if envHours := os.Getenv("AUTO_UPDATE_INTERVAL_HOURS"); envHours != "" {
		if h, err := strconv.Atoi(envHours); err == nil && h > 0 {
			interval = time.Duration(h) * time.Hour
		}
	}

	go func() {
		// Initial startup check after 5 seconds
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}

		runCheckCycle(ctx)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCheckCycle(ctx)
			}
		}
	}()
}

func runCheckCycle(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	info, err := CheckUpdate(checkCtx)
	if err != nil {
		log.Debug("updater", "periodic update check failed", "error", err)
		return
	}

	if !info.HasUpdate {
		log.Debug("updater", "9router-go is up to date", "version", info.CurrentVersion)
		return
	}

	log.Info("updater", "NEW 9ROUTER-GO VERSION AVAILABLE!",
		"current", info.CurrentVersion,
		"latest", info.LatestVersion,
		"downloadUrl", info.DownloadURL,
	)

	if IsAutoUpdateEnabled() || os.Getenv("AUTO_UPDATE") == "true" {
		log.Info("updater", "auto-update is enabled — applying update...", "version", info.LatestVersion)
		if err := PerformSelfUpdate(info.DownloadURL, info.SHA256); err != nil {
			log.Error("updater", "auto-update download/apply failed", "error", err)
		} else {
			log.Info("updater", "auto-update applied successfully! Restarting process...")
			RestartSelf()
		}
	}
}

// RestartSelf brings the updated executable into service.
//
// Under a supervisor (systemd, Docker with a restart policy, a process manager)
// respawning is the supervisor's job. Spawning a child ourselves would leave two
// processes racing for the same listening port, because the supervisor restarts
// us the moment we exit — so when a supervisor is detected we only ask to shut
// down and let it do its work.
func RestartSelf() {
	if supervisorManaged() {
		// Under a supervisor, exiting is enough: it will start the binary we
		// just swapped in. RestartSelf never spawns a replacement in this case
		// because the port would still be held by this process.
		log.Info("updater", "supervisor detected; exiting so it can restart the updated binary")
		if signalSelfShutdown() {
			time.AfterFunc(5*time.Second, func() { os.Exit(1) })
			select {}
		}
		os.Exit(0)
		return
	}

	spawnAndExit()
}

// supervisorManaged reports whether something else is responsible for keeping
// this process running.
//
// Only a real supervisor counts, because a false positive is unrecoverable:
// RestartSelf would exit without a replacement and the proxy would stay down.
// systemd's INVOCATION_ID/JOURNAL_STREAM are inherited by every descendant of a
// unit, so running this binary from a shell that systemd itself started (a
// terminal scope, a desktop session, a CI step) sets them without anyone
// watching us. Those variables must therefore be corroborated by our cgroup:
// only a .service cgroup means a unit actually owns this process.
func supervisorManaged() bool {
	if os.Getenv("CONTAINER") != "" || os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	// Docker writes /.dockerenv; the file is the only signal for images that
	// clear the CONTAINER env var.
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if os.Getenv("INVOCATION_ID") == "" && os.Getenv("JOURNAL_STREAM") == "" {
		return false
	}
	return systemdOwnsThisProcess()
}

// systemdOwnsThisProcess reports whether this process runs inside a systemd
// service unit (as opposed to a user session scope, which also exports
// INVOCATION_ID to everything it starts).
func systemdOwnsThisProcess() bool {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		// cgroup v1 puts the path after the last colon; v2 uses "0::<path>".
		idx := strings.LastIndex(line, ":")
		if idx < 0 {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(line[idx+1:]), ".service") {
			return true
		}
	}
	return false
}

// executableForRestart returns the path of the binary that should be started to
// take over from this process, or "" when it cannot be determined.
//
// os.Executable is the natural source, but it reads /proc/self/exe, which still
// points at the <binary>.old inode that a self-update has already renamed and
// removed. EvalSymlinks then fails with ENOENT even though the freshly written
// binary sits exactly where the updater put it, and the restart is skipped — the
// proxy stays down after every update that is not supervised. The path we were
// launched with (os.Args[0]) survives that rename, so it is the fallback.
func executableForRestart() string {
	// Prefer the path we were launched with. After a swap it is the only name
	// that still refers to the new binary, while os.Executable may resolve to the
	// deleted <binary>.old inode.
	if len(os.Args) > 0 && os.Args[0] != "" {
		if abs, err := filepath.Abs(os.Args[0]); err == nil {
			if _, statErr := os.Stat(abs); statErr == nil {
				return abs
			}
		}
	}

	if path, err := os.Executable(); err == nil {
		if resolved, rErr := filepath.EvalSymlinks(path); rErr == nil {
			if _, statErr := os.Stat(resolved); statErr == nil {
				return resolved
			}
		}
	}

	log.Error("updater", "cannot locate the updated binary to restart")
	return ""
}

func spawnAndExit() {
	execPath := executableForRestart()
	if execPath == "" {
		return
	}

	cmd := exec.Command(execPath, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		log.Error("updater", "spawn updated process failed", "error", err)
		return
	}

	log.Info("updater", "spawned updated process, shutting down old instance", "pid", cmd.Process.Pid)

	// Signal our own process to shut down gracefully instead of os.Exit(0):
	// main's signal handler drains in-flight SSE streams and closes the listener
	// before exiting, so the spawned process can bind the same port without
	// "address already in use". If we are not the process leader or the signal
	// path is unavailable (e.g. Windows), fall back to os.Exit(0).
	if signalSelfShutdown() {
		// Give main a bounded window to complete graceful shutdown; if it does
		// not exit in time, force-quit so the new process can take over.
		time.AfterFunc(5*time.Second, func() { os.Exit(1) })
		select {}
	}

	os.Exit(0)
}

// CompareVersions compares two semver strings (v1 > v2 -> 1, v1 < v2 -> -1, v1 == v2 -> 0).
func CompareVersions(v1, v2 string) int {
	parts1 := parseSemver(v1)
	parts2 := parseSemver(v2)

	for i := 0; i < 3; i++ {
		if parts1[i] > parts2[i] {
			return 1
		}
		if parts1[i] < parts2[i] {
			return -1
		}
	}
	return 0
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.Index(v, "-"); idx != -1 {
		v = v[:idx]
	}
	var parts [3]int
	fmt.Sscanf(v, "%d.%d.%d", &parts[0], &parts[1], &parts[2])
	return parts
}
