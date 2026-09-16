package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// newSessionTestRepo builds a repo with the apiKeys table so the API-key
// fallback in RequireDashboardSession can be exercised.
func newSessionTestRepo(t *testing.T) *db.Repo {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	return db.NewRepo(database)
}

// okHandler records that the request reached the guarded handler.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireDashboardSessionRedirectsBrowser(t *testing.T) {
	repo := newSessionTestRepo(t)
	reached := false
	mw := RequireDashboardSession(DashboardSessionConfig{
		Repo:          repo,
		VerifySession: func(string) bool { return false },
	})

	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	mw(okHandler(&reached)).ServeHTTP(w, req)

	if reached {
		t.Fatal("an unauthenticated browser must not reach the dashboard")
	}
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 redirect", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Fatalf("Location = %q, want /login", loc)
	}
}

func TestRequireDashboardSessionReturns401ForAPI(t *testing.T) {
	repo := newSessionTestRepo(t)
	reached := false
	mw := RequireDashboardSession(DashboardSessionConfig{
		Repo:          repo,
		VerifySession: func(string) bool { return false },
	})

	req := httptest.NewRequest("GET", "/api/dashboard/stats", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	mw(okHandler(&reached)).ServeHTTP(w, req)

	if reached {
		t.Fatal("an unauthenticated API call must not reach the handler")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	// The body must be machine-readable, not an HTML login page.
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("401 body is not JSON: %v (%s)", err, w.Body.String())
	}
}

func TestRequireDashboardSessionAcceptsValidCookie(t *testing.T) {
	repo := newSessionTestRepo(t)
	reached := false
	mw := RequireDashboardSession(DashboardSessionConfig{
		Repo:          repo,
		VerifySession: func(tok string) bool { return tok == "good" },
	})

	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: "good"})
	w := httptest.NewRecorder()
	mw(okHandler(&reached)).ServeHTTP(w, req)

	if !reached {
		t.Fatal("a valid session cookie must grant access")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestRequireDashboardSessionAcceptsAPIKey(t *testing.T) {
	repo := newSessionTestRepo(t)
	if _, err := repo.CreateApiKeyWithDetails("k1", "sk-live-key", "ci"); err != nil {
		t.Fatalf("CreateApiKeyWithDetails: %v", err)
	}
	reached := false
	mw := RequireDashboardSession(DashboardSessionConfig{
		Repo:          repo,
		VerifySession: func(string) bool { return false },
	})

	req := httptest.NewRequest("GET", "/api/dashboard/stats", nil)
	req.Header.Set("Authorization", "Bearer sk-live-key")
	w := httptest.NewRecorder()
	mw(okHandler(&reached)).ServeHTTP(w, req)

	if !reached {
		t.Fatal("a valid API key must still grant access (scripts/CI compatibility)")
	}
}

func TestRequireDashboardSessionRejectsBadAPIKey(t *testing.T) {
	repo := newSessionTestRepo(t)
	reached := false
	mw := RequireDashboardSession(DashboardSessionConfig{
		Repo:          repo,
		VerifySession: func(string) bool { return false },
	})

	req := httptest.NewRequest("GET", "/api/dashboard/stats", nil)
	req.Header.Set("Authorization", "Bearer sk-does-not-exist")
	w := httptest.NewRecorder()
	mw(okHandler(&reached)).ServeHTTP(w, req)

	if reached {
		t.Fatal("an unknown API key must not grant access")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRequireDashboardSessionHonoursLoginDisabled(t *testing.T) {
	repo := newSessionTestRepo(t)
	reached := false
	mw := RequireDashboardSession(DashboardSessionConfig{
		Repo:          repo,
		VerifySession: func(string) bool { return false },
		LoginDisabled: func() bool { return true },
	})

	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	mw(okHandler(&reached)).ServeHTTP(w, req)

	if !reached {
		t.Fatal("login disabled must let requests through unauthenticated")
	}
}
