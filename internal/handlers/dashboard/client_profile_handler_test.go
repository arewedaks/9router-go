package dashboard

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	json "encoding/json/v2"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// newProfileTestHandler spins up a Handler backed by a throwaway SQLite file
// containing only the providerConnections table.
func newProfileTestHandler(t *testing.T) (*Handler, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile_test.sqlite")
	database, err := db.OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	_, err = database.Exec(`CREATE TABLE providerConnections (
		id TEXT PRIMARY KEY,
		provider TEXT NOT NULL,
		authType TEXT NOT NULL,
		name TEXT,
		email TEXT,
		priority INTEGER,
		isActive INTEGER DEFAULT 1,
		data TEXT NOT NULL,
		lastUsedAt TEXT,
		consecutiveUseCount INTEGER DEFAULT 0,
		createdAt TEXT NOT NULL,
		updatedAt TEXT
	);`)
	if err != nil {
		database.Close()
		os.Remove(path)
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { database.Close(); os.Remove(path) })
	return &Handler{repo: db.NewRepo(database)}, database
}

// callProfileHandler invokes the handler with the chi {id} param populated.
func callProfileHandler(h *Handler, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/connections/"+id+"/client-profile",
		strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.HandleSetClientProfile(w, req)
	return w
}

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

func TestHandleSetClientProfile_RejectsEmptyID(t *testing.T) {
	h, _ := newProfileTestHandler(t)
	w := callProfileHandler(h, "", `{"profile":"cli"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleSetClientProfile_RejectsInvalidJSON(t *testing.T) {
	h, _ := newProfileTestHandler(t)
	w := callProfileHandler(h, "c1", `{not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleSetClientProfile_RejectsUnknownProfile(t *testing.T) {
	h, _ := newProfileTestHandler(t)
	w := callProfileHandler(h, "c1", `{"profile":"safari"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown profile, got %d", w.Code)
	}
}

func TestHandleSetClientProfile_NotFound(t *testing.T) {
	h, _ := newProfileTestHandler(t)
	w := callProfileHandler(h, "missing", `{"profile":"cli"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleSetClientProfile_RejectsNonAntigravity(t *testing.T) {
	h, database := newProfileTestHandler(t)
	seedConnection(t, db.NewRepo(database), "c-other", "openai", `{"apiKey":"x"}`)
	w := callProfileHandler(h, "c-other", `{"profile":"cli"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-antigravity, got %d", w.Code)
	}
}

// Setting cli must persist providerSpecificData.clientProfile and preserve
// every pre-existing key in the data blob.
func TestHandleSetClientProfile_PersistsAndPreservesData(t *testing.T) {
	h, database := newProfileTestHandler(t)
	repo := db.NewRepo(database)
	seedConnection(t, repo, "c-ag", "antigravity",
		`{"accessToken":"tok","projectId":"proj-1","providerSpecificData":{"projectId":"proj-1"}}`)

	w := callProfileHandler(h, "c-ag", `{"profile":"cli"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	conn, err := repo.GetProviderConnectionByID("c-ag")
	if err != nil || conn == nil {
		t.Fatalf("reload connection: %v (conn=%v)", err, conn)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		t.Fatalf("stored data not JSON: %v", err)
	}
	if data["accessToken"] != "tok" || data["projectId"] != "proj-1" {
		t.Fatalf("existing keys not preserved: %v", data)
	}
	if got := providers.ClientProfileFromData(data); got != providers.AntigravityProfileCLI {
		t.Fatalf("stored profile = %q, want cli", got)
	}
}

// The legacy "harness" alias must normalize to cli on write.
func TestHandleSetClientProfile_LegacyAliasNormalized(t *testing.T) {
	h, database := newProfileTestHandler(t)
	repo := db.NewRepo(database)
	seedConnection(t, repo, "c-ag", "antigravity", `{"accessToken":"tok"}`)

	w := callProfileHandler(h, "c-ag", `{"profile":"harness"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	conn, _ := repo.GetProviderConnectionByID("c-ag")
	var data map[string]any
	_ = json.Unmarshal([]byte(conn.Data), &data)
	if got := providers.ClientProfileFromData(data); got != providers.AntigravityProfileCLI {
		t.Fatalf("harness should persist as cli, got %q", got)
	}
}

// Switching back to ide must overwrite the stored value.
func TestHandleSetClientProfile_SwitchBackToIDE(t *testing.T) {
	h, database := newProfileTestHandler(t)
	repo := db.NewRepo(database)
	seedConnection(t, repo, "c-ag", "antigravity",
		`{"accessToken":"tok","providerSpecificData":{"clientProfile":"cli"}}`)

	w := callProfileHandler(h, "c-ag", `{"profile":"ide"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	conn, _ := repo.GetProviderConnectionByID("c-ag")
	var data map[string]any
	_ = json.Unmarshal([]byte(conn.Data), &data)
	if got := providers.ClientProfileFromData(data); got != providers.AntigravityProfileIDE {
		t.Fatalf("expected ide after switch, got %q", got)
	}
}

// The agy alias must also be accepted as an Antigravity provider.
func TestHandleSetClientProfile_AcceptsAgyAlias(t *testing.T) {
	h, database := newProfileTestHandler(t)
	repo := db.NewRepo(database)
	seedConnection(t, repo, "c-agy", "agy", `{"accessToken":"tok"}`)
	w := callProfileHandler(h, "c-agy", `{"profile":"cli"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for agy alias, got %d: %s", w.Code, w.Body.String())
	}
}
