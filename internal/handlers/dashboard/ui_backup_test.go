package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The backup UI lives in Settings alongside the password controls, and both
// directions go through /api/dashboard/database. These tests assert against the
// bytes the server serves, so a broken embed is caught as well as a bad edit.

func TestUISettingsHasBackupControls(t *testing.T) {
	ui := readEmbeddedUI(t)

	controls := []struct {
		what string
		need string
	}{
		{"download password field", `id="backup-pw"`},
		{"download button", `id="backup-btn"`},
		{"download message slot", `id="backup-msg"`},
		{"restore file input", `id="restore-file"`},
		{"restore password field", `id="restore-pw"`},
		{"restore button", `id="restore-btn"`},
		{"restore message slot", `id="restore-msg"`},
		{"file preview slot", `id="restore-preview"`},
	}
	for _, c := range controls {
		if !strings.Contains(ui, c.need) {
			t.Errorf("Settings is missing the %s (%s)", c.what, c.need)
		}
	}

	// The controls must be inside the Settings pane, not stranded elsewhere.
	// The pane now ends at </main>: the Health & Setup tab that used to follow it
	// was removed, so it can no longer be used as the closing marker.
	settingsIdx := strings.Index(ui, `id="tab-settings"`)
	mainEnd := strings.Index(ui[settingsIdx:], "</main>")
	if settingsIdx < 0 || mainEnd < 0 {
		t.Fatalf("could not locate the settings pane")
	}
	pane := ui[settingsIdx : settingsIdx+mainEnd]
	for _, need := range []string{`id="backup-pw"`, `id="restore-file"`, `id="restore-btn"`} {
		if !strings.Contains(pane, need) {
			t.Errorf("backup control %s is not inside the Settings pane", need)
		}
	}
}

func TestUIBackupWarnsAboutDestructiveRestore(t *testing.T) {
	ui := readEmbeddedUI(t)

	// Restore replaces rather than merges, and the file holds live tokens. Both
	// facts have to be visible before the operator clicks, so the wording is
	// part of the contract, not decoration.
	if !strings.Contains(ui, "full replace") {
		t.Error("the restore card does not say the restore is a full replace")
	}
	if !strings.Contains(ui, "Usage history is kept") {
		t.Error("the restore card does not say what is preserved")
	}
	if !strings.Contains(ui, "provider tokens and password hash") {
		t.Error("the download card does not warn that the file holds credentials")
	}
}

func TestUIBackupUsesTheDatabaseEndpoint(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `"/api/dashboard/database"`) {
		t.Fatal("the UI never calls /api/dashboard/database")
	}
	// The password is sent as the header the API expects, not smuggled in the
	// URL where it would land in history and logs.
	if !strings.Contains(ui, `"x-9r-password"`) {
		t.Error("the UI does not send the password in the x-9r-password header")
	}
	// The download must go through a Blob: a plain navigation or link would put
	// the password in the query string.
	if !strings.Contains(ui, "createObjectURL") {
		t.Error("the download does not use a Blob, so the password may end up in a URL")
	}
	if !strings.Contains(ui, "function downloadBackup()") {
		t.Error("downloadBackup() is missing")
	}
	if !strings.Contains(ui, "function restoreBackup()") {
		t.Error("restoreBackup() is missing")
	}
	if !strings.Contains(ui, "function onRestoreFileChosen()") {
		t.Error("onRestoreFileChosen() is missing; the operator cannot see what they are about to restore")
	}
}

// TestUIBackupConfirmsBeforeRestoring checks the guard rails: a confirm() before
// the destructive call, and a reload afterwards so no stale view survives.
func TestUIBackupConfirmsBeforeRestoring(t *testing.T) {
	ui := readEmbeddedUI(t)

	idx := strings.Index(ui, "function restoreBackup()")
	if idx < 0 {
		t.Fatal("restoreBackup() not found")
	}
	end := strings.Index(ui[idx:], "\n  function ")
	if end < 0 {
		end = len(ui) - idx
	}
	body := ui[idx : idx+end]

	if !strings.Contains(body, "confirm(") {
		t.Error("restoreBackup() does not confirm before replacing the configuration")
	}
	if !strings.Contains(body, "location.reload()") {
		t.Error("restoreBackup() does not reload, so the UI keeps showing the replaced configuration")
	}
}

// TestBackupEndpointsAreRouted checks the UI and API agree on the paths. A typo
// on either side would only show up when an operator tries to restore.
func TestBackupEndpointsAreRouted(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	setDashboardPassword(t, repo, "pw")
	seedBackupConnection(t, repo, "c1", "antigravity")

	// GET /api/dashboard/database is the export the UI calls.
	req := httptest.NewRequest("GET", "/api/dashboard/database", nil)
	req.Header.Set(backupPasswordHeader, "pw")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/dashboard/database = %d, want 200", w.Code)
	}

	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("export body is not JSON: %v", err)
	}
	// The UI reads these keys to build its preview.
	for _, key := range []string{"providerConnections", "apiKeys", "combos", "exportedAt", "settings"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("export payload has no %q key, which the preview reads", key)
		}
	}

	// POST /api/dashboard/database is the restore path. The UI adds the
	// confirmation password to the parsed document before posting it, so the
	// test does the same.
	doc["password"] = "pw"
	payload, _ := json.Marshal(doc)
	req = httptest.NewRequest("POST", "/api/dashboard/database", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/dashboard/database = %d, want 200 (%s)", w.Code, w.Body.String())
	}
}

// TestUIBackupWarnsAboutUnusablePasswordHash: a backup whose stored password is
// not a valid hash restores cleanly but leaves nobody able to sign in, so the
// UI has to catch it before the operator commits. The server refuses it too;
// this check just avoids the wasted round trip and makes the reason visible.
func TestUIBackupWarnsAboutUnusablePasswordHash(t *testing.T) {
	ui := readEmbeddedUI(t)

	idx := strings.Index(ui, "function onRestoreFileChosen()")
	if idx < 0 {
		t.Fatal("onRestoreFileChosen() not found")
	}
	end := strings.Index(ui[idx:], "\n  function ")
	if end < 0 {
		end = len(ui) - idx
	}
	body := ui[idx : idx+end]

	if !strings.Contains(body, "settings.password") {
		t.Error("the file preview does not inspect the stored password hash")
	}
	if !strings.Contains(body, "lock you out") {
		t.Error("the preview does not explain why an unusable hash matters")
	}
	// A bcrypt shape check, not a bare truthiness test.
	if !strings.Contains(body, `$2`) {
		t.Error("the preview does not check for a bcrypt hash shape")
	}
}
