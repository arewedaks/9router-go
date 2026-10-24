package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
)

func setupTestDB(t *testing.T) (*sql.DB, func()) {
	tmpFile, err := os.CreateTemp("", "test_router_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()

	database, err := db.OpenDatabase(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("OpenDatabase failed: %v", err)
	}

	cleanup := func() {
		database.Close()
		os.Remove(tmpFile.Name())
	}
	return database, cleanup
}

func TestSetupRoutes(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	r := chi.NewRouter()
	SetupRoutes(r, repo, nil)

	req := httptest.NewRequest("POST", "/chat/completions", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusMethodNotAllowed || w.Code == http.StatusNotFound {
		t.Errorf("expected /chat/completions route to be registered, got status %d", w.Code)
	}
}

// Alysis's OAuth endpoints must be mounted ahead of the generic {provider}
// wildcard. When /flow was missing, the request fell through to the wildcard
// handler and the dashboard showed "no OAuth flow for provider: alysis" — the
// dedicated handler existing was not enough, the route had to be registered.
func TestAlysisOAuthRoutesMounted(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	r := chi.NewRouter()
	SetupRoutes(r, db.NewRepo(database), nil)

	for _, path := range []string{"/api/oauth/alysis/flow"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s returned %d, want 200: %s", path, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), "device_code") {
			t.Errorf("%s answered without a flow kind: %s", path, w.Body.String())
		}
	}
}

func TestSetupServerRouter_PprofMounted(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	r := chi.NewRouter()
	SetupServerRouter(r, repo, nil)

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/goroutine"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected %s to return 200 OK, got %d", path, w.Code)
		}
	}
}

// The Deploy Relay UI runs in the dashboard, which authenticates with a session
// cookie and has no API key to send. The deploy handlers therefore have to be
// mounted behind that same session, next to the other proxy-pool routes; when
// they were only mounted at the root under API-key auth, the UI's POST reached
// nothing and every deploy failed with a bare 404.
func TestRelayDeployMountedOnDashboard(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	r := chi.NewRouter()
	SetupServerRouter(r, repo, nil)

	for _, path := range []string{
		"/api/dashboard/proxy-pools/cloudflare-deploy",
		"/api/dashboard/proxy-pools/vercel-deploy",
		"/api/dashboard/proxy-pools/deno-deploy",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		// Any auth/validation answer proves the route exists. 404 is the
		// regression this test is for.
		if w.Code == http.StatusNotFound {
			t.Errorf("%s is not mounted (got 404)", path)
		}
	}
}
