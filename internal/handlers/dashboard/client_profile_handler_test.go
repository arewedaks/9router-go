package dashboard

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// newProfileTestHandler spins up a Handler backed by a throwaway SQLite file.
// The settings table is the only storage the profile endpoint touches, but the
// full schema is created so the repo behaves like production.
func newProfileTestHandler(t *testing.T) (*Handler, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile_test.sqlite")
	database, err := db.OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	if err := db.EnsureSchema(database); err != nil {
		database.Close()
		t.Fatalf("EnsureSchema: %v", err)
	}
	t.Cleanup(func() { database.Close(); os.Remove(path) })
	return &Handler{repo: db.NewRepo(database)}, database
}

func callProfileHandler(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/settings/antigravity-profile",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleSetAntigravityClientProfile(w, req)
	return w
}

func TestHandleSetAntigravityClientProfile_RejectsInvalidJSON(t *testing.T) {
	h, _ := newProfileTestHandler(t)
	w := callProfileHandler(h, `{not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleSetAntigravityClientProfile_RejectsUnknownProfile(t *testing.T) {
	h, _ := newProfileTestHandler(t)
	w := callProfileHandler(h, `{"profile":"safari"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown profile, got %d", w.Code)
	}
}

// A keyless install must not be rejected: the profile is a provider-wide
// setting, so no Antigravity connection needs to exist for it to be saved. The
// per-connection endpoint this replaced required one, which meant the setting
// could not be prepared before the first account was added.
func TestHandleSetAntigravityClientProfile_SavesWithoutAnyConnection(t *testing.T) {
	h, database := newProfileTestHandler(t)
	w := callProfileHandler(h, `{"profile":"cli"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	repo := db.NewRepo(database)
	s, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.AntigravityClientProfile != "cli" {
		t.Fatalf("stored profile = %q, want cli", s.AntigravityClientProfile)
	}
}

// The setting is provider-wide: saving it must be visible to every future read,
// which is the whole point of replacing the per-account value.
func TestHandleSetAntigravityClientProfile_PersistsAcrossReads(t *testing.T) {
	h, database := newProfileTestHandler(t)
	repo := db.NewRepo(database)

	if w := callProfileHandler(h, `{"profile":"cli"}`); w.Code != http.StatusOK {
		t.Fatalf("set cli: %d", w.Code)
	}
	if got, _ := repo.GetSettings(); got.AntigravityClientProfile != "cli" {
		t.Fatalf("after cli: %q", got.AntigravityClientProfile)
	}

	if w := callProfileHandler(h, `{"profile":"ide"}`); w.Code != http.StatusOK {
		t.Fatalf("set ide: %d", w.Code)
	}
	got, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.AntigravityClientProfile != "ide" {
		t.Fatalf("after switching back: %q, want ide", got.AntigravityClientProfile)
	}
}

// The legacy "harness" alias must normalize to cli on write.
func TestHandleSetAntigravityClientProfile_LegacyAliasNormalized(t *testing.T) {
	h, database := newProfileTestHandler(t)
	if w := callProfileHandler(h, `{"profile":"harness"}`); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got, _ := db.NewRepo(database).GetSettings()
	if got.AntigravityClientProfile != "cli" {
		t.Fatalf("harness should persist as cli, got %q", got.AntigravityClientProfile)
	}
}

// Saving the profile must not disturb any other setting in the same row.
func TestHandleSetAntigravityClientProfile_PreservesOtherSettings(t *testing.T) {
	h, database := newProfileTestHandler(t)
	repo := db.NewRepo(database)

	if err := repo.SetAutoUpdate(true); err != nil {
		t.Fatalf("SetAutoUpdate: %v", err)
	}
	if err := repo.SetPasswordHash("hash-value"); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}

	if w := callProfileHandler(h, `{"profile":"cli"}`); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	got, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !got.AutoUpdate {
		t.Error("autoUpdate was clobbered by the profile write")
	}
	if got.PasswordHash != "hash-value" {
		t.Error("password hash was clobbered by the profile write")
	}
}

// seedConnection inserts a provider connection row for tests that exercise
// per-account behaviour (connection probing, listing, …).
func seedConnection(t *testing.T, repo *db.Repo, id, provider, data string) {
	t.Helper()
	conn := &models.ProviderConnection{
		ID:       id,
		Provider: provider,
		AuthType: "oauth",
		IsActive: 1,
		Data:     data,
	}
	if err := repo.UpsertProviderConnection(conn); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}
