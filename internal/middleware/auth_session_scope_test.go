package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// okHandler is the "reached the real handler" marker used by the session tests.
func okMarkerHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestRequireApiKeyWithSessionAcceptsCookie covers the regression that produced
// the reported bug: a browser that signed in with the dashboard password holds
// only the session cookie, so the OAuth add-account endpoints (which sit behind
// this middleware) answered 401 and the UI showed an error.
func TestRequireApiKeyWithSessionAcceptsCookie(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	valid := func(token string) bool { return token == "good-token" }
	mw := RequireApiKeyWithSession(repo, valid, "/api/oauth/")

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/cline/authorize", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: "good-token"})
	rec := httptest.NewRecorder()
	mw(okMarkerHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a valid session cookie", rec.Code)
	}
}

// TestRequireApiKeyWithSessionRejectsBadCookie makes sure the cookie is actually
// verified rather than merely present.
func TestRequireApiKeyWithSessionRejectsBadCookie(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	valid := func(token string) bool { return token == "good-token" }
	mw := RequireApiKeyWithSession(repo, valid, "/api/oauth/")

	for name, value := range map[string]string{
		"forged":  "forged.value.sig",
		"empty":   "",
		"expired": "expired-token",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/oauth/cline/authorize", nil)
		req.AddCookie(&http.Cookie{Name: "auth_token", Value: value})
		rec := httptest.NewRecorder()
		mw(okMarkerHandler()).ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s cookie: status = %d, want 401", name, rec.Code)
		}
	}
}

// TestRequireApiKeyWithSessionCookieScopedToDashboardPaths is the security
// half of the change: the cookie must authenticate the browser-facing OAuth
// routes but must NOT unlock the engine surface (`/v1`, `/chat/completions`),
// which is a machine credential contract. Without the scope the session would
// silently become a proxy credential.
func TestRequireApiKeyWithSessionCookieScopedToDashboardPaths(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	valid := func(token string) bool { return token == "good-token" }
	mw := RequireApiKeyWithSession(repo, valid, "/api/oauth/", "/api/dashboard/")

	allowed := []string{"/api/oauth/cline/authorize", "/api/dashboard/providers"}
	for _, path := range allowed {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: "auth_token", Value: "good-token"})
		rec := httptest.NewRecorder()
		mw(okMarkerHandler()).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (cookie should be accepted)", path, rec.Code)
		}
	}

	denied := []string{"/v1/models", "/chat/completions", "/messages"}
	for _, path := range denied {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: "auth_token", Value: "good-token"})
		rec := httptest.NewRecorder()
		mw(okMarkerHandler()).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 (cookie must not unlock the engine)", path, rec.Code)
		}
	}
}

// TestRequireApiKeyWithSessionStillAcceptsApiKey proves the change is additive:
// scripts and CI keep working with a key and no cookie.
func TestRequireApiKeyWithSessionStillAcceptsApiKey(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	mw := RequireApiKeyWithSession(repo, func(string) bool { return false }, "/api/oauth/")

	for _, path := range []string{"/api/oauth/cline/authorize", "/v1/models"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer valid-token")
		rec := httptest.NewRecorder()
		mw(okMarkerHandler()).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 for a valid API key", path, rec.Code)
		}
	}
}

// TestRequireApiKeyWithSessionNilVerifierIsApiKeyOnly pins the backward
// compatibility contract: passing nil must restore the old behaviour exactly.
func TestRequireApiKeyWithSessionNilVerifierIsApiKeyOnly(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	mw := RequireApiKeyWithSession(repo, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/cline/authorize", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: "anything"})
	rec := httptest.NewRecorder()
	mw(okMarkerHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no verifier is configured", rec.Code)
	}
}

// TestSessionPathAllowed covers the prefix matcher directly, including the
// empty-list "allow everything" default.
func TestSessionPathAllowed(t *testing.T) {
	if !sessionPathAllowed("/v1/models", nil) {
		t.Error("empty allow-list should permit every path")
	}
	allowed := []string{"/api/oauth/", "/api/dashboard/"}
	cases := map[string]bool{
		"/api/oauth/cline/authorize": true,
		"/api/oauth/github/exchange": true,
		"/api/dashboard/providers":   true,
		"/v1/models":                 false,
		"/chat/completions":          false,
		"/api/oauth":                 false, // no trailing slash: not a prefix match
	}
	for path, want := range cases {
		if got := sessionPathAllowed(path, allowed); got != want {
			t.Errorf("sessionPathAllowed(%q) = %v, want %v", path, got, want)
		}
	}
}
