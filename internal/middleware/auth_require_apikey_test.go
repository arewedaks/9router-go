package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// settingsBlob builds a minimal settings row. Only the keys under test are
// written, so the row stays a realistic stand-in for a migrated VansRouter blob
// rather than a fully-populated one.
func settingsBlob(requireAPIKey, allowRemote, requireLogin *bool) string {
	blob := `{"rtkEnabled": true`
	if requireAPIKey != nil {
		blob += fmt.Sprintf(`, "requireApiKey": %t`, *requireAPIKey)
	}
	if allowRemote != nil {
		blob += fmt.Sprintf(`, "allowRemoteNoApiKey": %t`, *allowRemote)
	}
	if requireLogin != nil {
		blob += fmt.Sprintf(`, "requireLogin": %t`, *requireLogin)
	}
	return blob + "}"
}

// seedSettings writes the settings row.
//
// setupTestDB only creates apiKeys, so the settings table is created here rather
// than widening the shared helper for the whole package.
func seedSettings(t *testing.T, repo *db.Repo, requireAPIKey, allowRemote, requireLogin *bool) {
	t.Helper()
	if _, err := repo.RawDB().Exec(
		`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
	if _, err := repo.RawDB().Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		settingsBlob(requireAPIKey, allowRemote, requireLogin)); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
}

// engineRequestFrom sends a bare /v1 call with no credential at all, from the
// given peer address, and returns the status.
func engineRequestFrom(t *testing.T, mw func(http.Handler) http.Handler, remoteAddr string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	rec := httptest.NewRecorder()
	mw(okMarkerHandler()).ServeHTTP(rec, req)
	return rec.Code
}

const (
	loopbackAddr = "127.0.0.1:54321"
	remoteAddr   = "203.0.113.9:54321"
)

// With requireApiKey false but the remote flag unset, an operator turning the
// key off to test locally must not also publish the proxy. Only loopback passes.
func TestRequireAPIKeyOffIsLoopbackOnlyByDefault(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	off := false
	seedSettings(t, repo, &off, nil, nil)
	mw := RequireApiKeyUnlessDisabled(repo, nil)

	if got := engineRequestFrom(t, mw, loopbackAddr); got != http.StatusOK {
		t.Fatalf("loopback status = %d, want 200 with requireApiKey=false", got)
	}
	if got := engineRequestFrom(t, mw, remoteAddr); got != http.StatusUnauthorized {
		t.Fatalf("remote status = %d, want 401 while allowRemoteNoApiKey is unset; "+
			"switching the key off alone must not expose the proxy", got)
	}
}

// With both flags off, an unauthenticated remote request must reach the handler.
// This is the deliberate, fully-open configuration.
func TestAllowRemoteNoApiKeyOpensTheEngine(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	off := false
	on := true
	seedSettings(t, repo, &off, &on, nil)
	mw := RequireApiKeyUnlessDisabled(repo, nil)

	if got := engineRequestFrom(t, mw, remoteAddr); got != http.StatusOK {
		t.Fatalf("remote status = %d, want 200 with both flags off", got)
	}
}

// allowRemoteNoApiKey must not widen access on its own. While a key is required
// the flag is inert, so a leftover value from VansRouter cannot punch a hole.
func TestAllowRemoteIsInertWhileKeyRequired(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	on := true
	seedSettings(t, repo, &on, &on, nil)
	mw := RequireApiKeyUnlessDisabled(repo, nil)

	for _, addr := range []string{loopbackAddr, remoteAddr} {
		if got := engineRequestFrom(t, mw, addr); got != http.StatusUnauthorized {
			t.Fatalf("status from %s = %d, want 401 while requireApiKey is on", addr, got)
		}
	}
}

// A client must not be able to claim loopback by forging the forwarding headers.
// If X-Forwarded-For were believed here, anyone could set it to 127.0.0.1 and
// skip authentication entirely.
func TestLoopbackCannotBeSpoofedWithForwardingHeaders(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	off := false
	seedSettings(t, repo, &off, nil, nil)
	mw := RequireApiKeyUnlessDisabled(repo, nil)

	for _, h := range []struct{ name, value string }{
		{"X-Forwarded-For", "127.0.0.1"},
		{"X-Real-IP", "127.0.0.1"},
		{"X-Forwarded-For", "127.0.0.1, 203.0.113.9"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set(h.name, h.value)
		rec := httptest.NewRecorder()
		mw(okMarkerHandler()).ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %q was believed; status = %d, want 401",
				h.name, h.value, rec.Code)
		}
	}
}

// Absent flags keep the strictest behaviour, which is what protects a VansRouter
// database that never wrote these keys.
func TestAbsentFlagsRequireKey(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	seedSettings(t, repo, nil, nil, nil)
	mw := RequireApiKeyUnlessDisabled(repo, nil)

	for _, addr := range []string{loopbackAddr, remoteAddr} {
		if got := engineRequestFrom(t, mw, addr); got != http.StatusUnauthorized {
			t.Fatalf("status from %s = %d, want 401 when both flags are absent", addr, got)
		}
	}
}

// IPv6 loopback and a bare address (no port) must be recognised, and the
// parsing must not panic on an empty peer.
func TestLoopbackAddressParsing(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	off := false
	seedSettings(t, repo, &off, nil, nil)
	mw := RequireApiKeyUnlessDisabled(repo, nil)

	for _, tc := range []struct {
		addr string
		want int
	}{
		{"[::1]:80", http.StatusOK},
		{"127.0.0.1", http.StatusOK},
		{"::1", http.StatusOK},
		{"[::1]", http.StatusOK},
		{"[fe80::1%eth0]:80", http.StatusUnauthorized},
		{"0.0.0.0:80", http.StatusUnauthorized},
		{"", http.StatusUnauthorized},
	} {
		if got := engineRequestFrom(t, mw, tc.addr); got != tc.want {
			t.Errorf("addr %q: status = %d, want %d", tc.addr, got, tc.want)
		}
	}
}

// The flags are read per request, not captured when the router is built.
// Flipping them in the database must change behaviour on the next request.
func TestFlagsAreReadPerRequest(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	off := false
	seedSettings(t, repo, &off, nil, nil)

	// One middleware instance for the whole test: this is what a live router
	// holds, so a captured value would show up as a stale result below.
	mw := RequireApiKeyUnlessDisabled(repo, nil)

	if got := engineRequestFrom(t, mw, remoteAddr); got != http.StatusUnauthorized {
		t.Fatalf("remote status = %d, want 401 initially", got)
	}

	// Turning the remote flag on widens access with no restart.
	on := true
	seedSettings(t, repo, &off, &on, nil)
	if got := engineRequestFrom(t, mw, remoteAddr); got != http.StatusOK {
		t.Fatalf("remote status = %d, want 200 after enabling allowRemoteNoApiKey; "+
			"the middleware appears to have cached the value", got)
	}

	// Turning the key requirement back on closes it again.
	seedSettings(t, repo, &on, &on, nil)
	if got := engineRequestFrom(t, mw, remoteAddr); got != http.StatusUnauthorized {
		t.Fatalf("remote status = %d, want 401 after re-enabling requireApiKey", got)
	}
}

// A settings read failure must fail closed. A database error is not a reason to
// serve the proxy unauthenticated.
func TestFlagsFailClosedOnRepoError(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	off := false
	on := true
	seedSettings(t, repo, &off, &on, nil)

	// Confirm the open path first, so the assertion below cannot pass merely
	// because the gate never opens.
	mw := RequireApiKeyUnlessDisabled(repo, nil)
	if got := engineRequestFrom(t, mw, loopbackAddr); got != http.StatusOK {
		t.Fatalf("precondition failed: status = %d, want 200", got)
	}

	if _, err := repo.RawDB().Exec(`DROP TABLE settings`); err != nil {
		t.Fatalf("drop settings table: %v", err)
	}
	if got := engineRequestFrom(t, mw, loopbackAddr); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when the settings read fails", got)
	}
}

// requestFrom sends a bare GET to path with no credential at all, from the
// given peer address, and returns the status.
func requestFrom(t *testing.T, mw func(http.Handler) http.Handler, path, remoteAddr string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	rec := httptest.NewRecorder()
	mw(okMarkerHandler()).ServeHTTP(rec, req)
	return rec.Code
}

// With login disabled the dashboard is served to anyone who can reach it, so
// the browser-facing endpoints the dashboard calls must not demand a credential
// the page was never asked to obtain. Reported as: the Console Log resource
// card showed "Could not read resource metrics: HTTP 401" while the rest of the
// dashboard worked, because requireApiKey was on and the browser held no
// session cookie (login disabled means none is ever issued).
func TestLoginDisabledOpensDashboardPaths(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	on := true
	seedSettings(t, repo, &on, nil, new(false))
	mw := RequireApiKeyUnlessDisabled(repo, func(string) bool { return false },
		"/api/usage/", "/api/system/", "/api/translator/")

	// A remote caller, so the loopback rule cannot be what let it through.
	for _, path := range []string{"/api/system/metrics", "/api/usage/stats"} {
		if got := requestFrom(t, mw, path, remoteAddr); got != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 with login disabled", path, got)
		}
	}
}

// Login disabled must not open the engine surface. Only the declared
// browser-facing paths are affected, so a key is still required for /v1.
func TestLoginDisabledKeepsEngineGuarded(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	on := true
	seedSettings(t, repo, &on, nil, new(false))
	mw := RequireApiKeyUnlessDisabled(repo, func(string) bool { return false },
		"/api/usage/", "/api/system/", "/api/translator/")

	for _, addr := range []string{loopbackAddr, remoteAddr} {
		if got := engineRequestFrom(t, mw, addr); got != http.StatusUnauthorized {
			t.Errorf("status from %s = %d, want 401; login being disabled must not "+
				"turn the model proxy into an open endpoint", addr, got)
		}
	}
}

// With login required, the dashboard paths keep their cookie-or-key gate. This
// is the pre-existing behaviour the bypass must not weaken.
func TestLoginRequiredKeepsDashboardPathsGuarded(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	on := true
	seedSettings(t, repo, &on, nil, nil)
	mw := RequireApiKeyUnlessDisabled(repo, func(string) bool { return false },
		"/api/usage/", "/api/system/", "/api/translator/")

	if got := requestFrom(t, mw, "/api/system/metrics", remoteAddr); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 while login is required and no cookie is sent", got)
	}
}

// A caller that declares no browser-facing paths gets no bypass: an empty list
// means "every path" in sessionPathAllowed, which would open the engine.
func TestLoginDisabledWithoutPathListStillGuards(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	on := true
	seedSettings(t, repo, &on, nil, new(false))
	mw := RequireApiKeyUnlessDisabled(repo, func(string) bool { return false })

	if got := engineRequestFrom(t, mw, loopbackAddr); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when no session paths are declared", got)
	}
}
