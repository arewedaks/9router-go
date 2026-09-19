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
	mw := RequireApiKeyWithSession(repo, valid,
		"/api/oauth/", "/api/dashboard/", "/api/translator/", "/translator/", "/usage/", "/api/usage/")

	allowed := []string{
		"/api/oauth/cline/authorize",
		"/api/dashboard/providers",
		// The browser console-log viewer and usage stream cannot attach an
		// Authorization header (EventSource has no header API), so the session
		// cookie is the only credential a password-logged-in dashboard holds.
		"/api/translator/console-logs",
		"/api/translator/console-logs/stream",
		"/translator/console-logs",
		"/api/usage/stats",
	}
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
	allowed := []string{"/api/oauth/", "/api/dashboard/", "/api/translator/", "/translator/", "/usage/", "/api/usage/", "/v1/models", "/models"}
	cases := map[string]bool{
		"/api/oauth/cline/authorize":      true,
		"/api/oauth/github/exchange":      true,
		"/api/dashboard/providers":        true,
		"/api/translator/console-logs":    true,
		"/translator/console-logs/stream": true,
		"/api/usage/stats":                true,
		// The dashboard's model pickers read the catalog through the session
		// cookie; without this they got 401 and rendered an empty list.
		"/v1/models":   true,
		"/v1/models/*": true, // prefix match covers the lookup sub-route
		// RequestLogger strips a leading "/v1" before the auth middleware runs,
		// so the stripped spelling is the one that actually has to match.
		"/models":           true,
		"/chat/completions": false,
		"/api/oauth":        false, // no trailing slash: not a prefix match
	}
	for path, want := range cases {
		if got := sessionPathAllowed(path, allowed); got != want {
			t.Errorf("sessionPathAllowed(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestRequestLoggerStripsV1Prefix documents the path rewrite that made the
// session allow-list miss the model catalog: by the time the auth middleware
// runs, "/v1/models" has already become "/models". Any session path for a /v1
// route must therefore be listed in its stripped form too.
func TestRequestLoggerStripsV1Prefix(t *testing.T) {
	var seen string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	RequestLogger(inner).ServeHTTP(rec, req)

	if seen != "/models" {
		t.Fatalf("path seen by inner handler = %q, want %q", seen, "/models")
	}
}

// TestRequireApiKeyWithSessionAcceptsCookieForModelCatalog pins the reported
// regression: the combo "Add Model" picker fetches /v1/models with only the
// dashboard session cookie, so that route must accept it or the picker shows
// an empty list even though the server can list 600+ models.
func TestRequireApiKeyWithSessionAcceptsCookieForModelCatalog(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	valid := func(token string) bool { return token == "good-token" }
	// "/models" is the spelling the middleware actually observes, because
	// RequestLogger strips the /v1 prefix first.
	mw := RequireApiKeyWithSession(repo, valid, "/api/dashboard/", "/v1/models", "/models")

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: "good-token"})
	rec := httptest.NewRecorder()
	mw(okMarkerHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a valid session cookie on /models", rec.Code)
	}

	// A bad cookie must still be rejected: the route is browser-facing, not public.
	req = httptest.NewRequest(http.MethodGet, "/models", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: "forged"})
	rec = httptest.NewRecorder()
	mw(okMarkerHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a forged cookie", rec.Code)
	}

	// A chat route must stay API-key only even with a good cookie.
	req = httptest.NewRequest(http.MethodPost, "/chat/completions", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: "good-token"})
	rec = httptest.NewRecorder()
	mw(okMarkerHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a cookie on a chat route", rec.Code)
	}
}
