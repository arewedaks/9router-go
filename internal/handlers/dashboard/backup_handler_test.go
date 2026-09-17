package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// setDashboardPassword stores a real bcrypt hash so the confirmation path is
// exercised exactly as production runs it, rather than through a stub. The cost
// is bcrypt's default, which is what the handler uses.
func setDashboardPassword(t *testing.T, repo *db.Repo, password string) {
	t.Helper()
	hash, err := bcryptHasher{}.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := repo.SetPasswordHash(hash); err != nil {
		t.Fatalf("set password hash: %v", err)
	}
}

// seedConnection inserts one connection so an export has something to carry.
func seedBackupConnection(t *testing.T, repo *db.Repo, id, provider string) {
	t.Helper()
	if err := repo.CreateProviderConnection(id, provider, "apikey", "Seed "+id, "sk-secret-"+id); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

func TestExportRequiresPasswordConfirmation(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	setDashboardPassword(t, repo, "correct-horse")
	seedBackupConnection(t, repo, "c1", "antigravity")

	// No password at all.
	req := httptest.NewRequest("GET", "/api/dashboard/database", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("without a password: status = %d, want 401", w.Code)
	}

	// Wrong password.
	req = httptest.NewRequest("GET", "/api/dashboard/database", nil)
	req.Header.Set(backupPasswordHeader, "wrong")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("with a wrong password: status = %d, want 401", w.Code)
	}

	// A failed attempt must not leak the payload.
	if strings.Contains(w.Body.String(), "sk-secret") {
		t.Error("response to a rejected export contained credential material")
	}
}

func TestExportReturnsDownloadableBackup(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	setDashboardPassword(t, repo, "correct-horse")
	seedBackupConnection(t, repo, "c1", "antigravity")

	req := httptest.NewRequest("GET", "/api/dashboard/database", nil)
	req.Header.Set(backupPasswordHeader, "correct-horse")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}

	// The browser needs a filename to save under.
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "9router-backup-") || !strings.Contains(cd, ".json") {
		t.Errorf("Content-Disposition = %q, want an attachment named 9router-backup-*.json", cd)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (the file carries live tokens)", got)
	}
	// A JSON content type matters: the UI reads this as a Blob and downloads it
	// directly, and a text/plain reply would still work but browsers may render
	// it instead of saving.
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var doc struct {
		FormatVersion       int              `json:"formatVersion"`
		ExportedAt          string           `json:"exportedAt"`
		Settings            map[string]any   `json:"settings"`
		ProviderConnections []map[string]any `json:"providerConnections"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if doc.FormatVersion == 0 || doc.ExportedAt == "" {
		t.Errorf("backup missing version/exportedAt: %+v", doc)
	}
	if len(doc.ProviderConnections) != 1 {
		t.Errorf("providerConnections = %d, want 1", len(doc.ProviderConnections))
	}
	// The password hash must travel with the backup, otherwise a restore would
	// leave the old login in place and the file would not be a true snapshot.
	if pw, _ := doc.Settings["password"].(string); pw == "" {
		t.Error("settings.password missing from the export")
	}
}

// TestExportFilenameIsFilesystemSafe guards the download name: a raw RFC3339
// stamp contains colons, which are illegal in filenames on some platforms.
func TestExportFilenameIsFilesystemSafe(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	setDashboardPassword(t, repo, "pw")
	seedBackupConnection(t, repo, "c1", "antigravity")

	req := httptest.NewRequest("GET", "/api/dashboard/database", nil)
	req.Header.Set(backupPasswordHeader, "pw")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	cd := w.Header().Get("Content-Disposition")
	name := cd
	if i := strings.Index(cd, "filename=\""); i >= 0 {
		name = strings.TrimSuffix(cd[i+len("filename=\""):], "\"")
	} else {
		t.Fatalf("Content-Disposition %q has no filename", cd)
	}
	for _, bad := range []string{":", "+", "/", "\\"} {
		if strings.Contains(name, bad) {
			t.Errorf("filename %q contains %q, which is not portable", name, bad)
		}
	}
}

// withPassword injects the confirmation password into an export payload, the way
// the UI posts it: the backup document plus a `password` field.
func withPassword(t *testing.T, payload []byte, password string) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("payload: %v", err)
	}
	m["password"] = password
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

func TestImportReplacesDatabase(t *testing.T) {
	h, repo, r := setupTestDashboard(t)
	setDashboardPassword(t, repo, "pw")
	seedBackupConnection(t, repo, "keep-me", "antigravity")

	// Export the current state.
	req := httptest.NewRequest("GET", "/api/dashboard/database", nil)
	req.Header.Set(backupPasswordHeader, "pw")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("export failed: %d", w.Code)
	}
	payload := w.Body.Bytes()

	// Wipe and add a row that must not survive the restore.
	if err := repo.DeleteProviderConnection("keep-me"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	seedBackupConnection(t, repo, "intruder", "kiro")

	req = httptest.NewRequest("POST", "/api/dashboard/database", bytes.NewReader(withPassword(t, payload, "pw")))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import failed: %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !resp.Success {
		t.Errorf("import response = %s, want success:true", w.Body.String())
	}

	conns, err := h.repo.GetAllProviderConnections()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := map[string]bool{}
	for _, c := range conns {
		ids[c.ID] = true
	}
	if !ids["keep-me"] {
		t.Error("the backed-up connection did not come back")
	}
	if ids["intruder"] {
		t.Error("a connection absent from the backup survived the import; import is merging, not replacing")
	}
}

func TestImportRequiresPassword(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	setDashboardPassword(t, repo, "pw")
	seedBackupConnection(t, repo, "c1", "antigravity")

	body := `{"formatVersion":1,"settings":{"rtkEnabled":false}}`

	// Password in the body, wrong.
	req := httptest.NewRequest("POST", "/api/dashboard/database",
		bytes.NewReader([]byte(`{"password":"nope","formatVersion":1}`)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong body password: status = %d, want 401", w.Code)
	}

	// Password in the header, correct.
	req = httptest.NewRequest("POST", "/api/dashboard/database", bytes.NewReader([]byte(body)))
	req.Header.Set(backupPasswordHeader, "pw")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("correct header password: status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
}

func TestImportRejectsGarbage(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	setDashboardPassword(t, repo, "pw")
	seedBackupConnection(t, repo, "c1", "antigravity")

	req := httptest.NewRequest("POST", "/api/dashboard/database", bytes.NewReader([]byte("not json at all")))
	req.Header.Set(backupPasswordHeader, "pw")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("garbage body: status = %d, want 400", w.Code)
	}

	// The database must be untouched by a rejected import.
	conns, err := repo.GetAllProviderConnections()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(conns) != 1 {
		t.Errorf("connections = %d after a rejected import, want 1", len(conns))
	}
}

func TestInspectBackupDescribesFileWithoutImporting(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	setDashboardPassword(t, repo, "pw")
	seedBackupConnection(t, repo, "c1", "antigravity")

	req := httptest.NewRequest("GET", "/api/dashboard/database", nil)
	req.Header.Set(backupPasswordHeader, "pw")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	payload := w.Body.Bytes()

	req = httptest.NewRequest("POST", "/api/dashboard/database/inspect", bytes.NewReader(payload))
	req.Header.Set(backupPasswordHeader, "pw")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("inspect: status = %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Valid   bool   `json:"valid"`
		App     string `json:"app"`
		Summary struct {
			ProviderConnections int `json:"providerConnections"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("inspect body: %v", err)
	}
	if !resp.Valid || resp.Summary.ProviderConnections != 1 {
		t.Errorf("inspect = %+v, want valid with 1 connection", resp)
	}

	// Inspect must not modify anything.
	conns, _ := repo.GetAllProviderConnections()
	if len(conns) != 1 {
		t.Errorf("inspect changed the database: %d connections", len(conns))
	}
}

// TestImportRestoresPassword proves the login credentials come back with the
// backup, which is the point of including the hash.
func TestImportRestoresPassword(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	setDashboardPassword(t, repo, "original-pw")
	seedBackupConnection(t, repo, "c1", "antigravity")

	req := httptest.NewRequest("GET", "/api/dashboard/database", nil)
	req.Header.Set(backupPasswordHeader, "original-pw")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	payload := w.Body.Bytes()

	// Change the password, then restore the backup.
	setDashboardPassword(t, repo, "changed-pw")

	req = httptest.NewRequest("POST", "/api/dashboard/database", bytes.NewReader(withPassword(t, payload, "changed-pw")))
	req.Header.Set(backupPasswordHeader, "changed-pw")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import: status = %d (%s)", w.Code, w.Body.String())
	}

	// The restored backup is now the live credential set, so only the original
	// password should work again.
	req = httptest.NewRequest("GET", "/api/dashboard/database", nil)
	req.Header.Set(backupPasswordHeader, "original-pw")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("original password rejected after restore: %d", w.Code)
	}

	req = httptest.NewRequest("GET", "/api/dashboard/database", nil)
	req.Header.Set(backupPasswordHeader, "changed-pw")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("the pre-restore password still works after restore: %d", w.Code)
	}
}

// TestBackupHashIsNotAUsablePassword pins a property worth stating: the export
// deliberately carries the password hash (a restore must bring the login with
// it), so it is important that the hash itself is not accepted as the password.
// If it were, anyone holding a backup file could open the dashboard.
func TestBackupHashIsNotAUsablePassword(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	setDashboardPassword(t, repo, "real-pw")
	seedBackupConnection(t, repo, "c1", "antigravity")

	req := httptest.NewRequest("GET", "/api/dashboard/database", nil)
	req.Header.Set(backupPasswordHeader, "real-pw")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("export: %d", w.Code)
	}

	var doc struct {
		Settings map[string]any `json:"settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body: %v", err)
	}
	hash, _ := doc.Settings["password"].(string)
	if hash == "" {
		t.Skip("no hash in the payload")
	}

	req = httptest.NewRequest("GET", "/api/dashboard/database", nil)
	req.Header.Set(backupPasswordHeader, hash)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("the stored hash was accepted as the password; a backup file would be enough to open the dashboard")
	}
}
