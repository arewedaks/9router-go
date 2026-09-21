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
func settingsBlob(requireAPIKey, allowRemote *bool) string {
	blob := `{"rtkEnabled": true`
	if requireAPIKey != nil {
		blob += fmt.Sprintf(`, "requireApiKey": %t`, *requireAPIKey)
	}
	if allowRemote != nil {
		blob += fmt.Sprintf(`, "allowRemoteNoApiKey": %t`, *allowRemote)
	}
	return blob + "}"
}

// seedSettings writes the settings row.
//
// setupTestDB only creates apiKeys, so the settings table is created here rather
// than widening the shared helper for the whole package.
func seedSettings(t *testing.T, repo *db.Repo, requireAPIKey, allowRemote *bool) {
	t.Helper()
	if _, err := repo.RawDB().Exec(
		`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
	if _, err := repo.RawDB().Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		settingsBlob(requireAPIKey, allowRemote)); err != nil {
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
	seedSettings(t, repo, &off, nil)
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
	seedSettings(t, repo, &off, &on)
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
	seedSettings(t, repo, &on, &on)
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
	seedSettings(t, repo, &off, nil)
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

	seedSettings(t, repo, nil, nil)
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
	seedSettings(t, repo, &off, nil)
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
	seedSettings(t, repo, &off, nil)

	// One middleware instance for the whole test: this is what a live router
	// holds, so a captured value would show up as a stale result below.
	mw := RequireApiKeyUnlessDisabled(repo, nil)

	if got := engineRequestFrom(t, mw, remoteAddr); got != http.StatusUnauthorized {
		t.Fatalf("remote status = %d, want 401 initially", got)
	}

	// Turning the remote flag on widens access with no restart.
	on := true
	seedSettings(t, repo, &off, &on)
	if got := engineRequestFrom(t, mw, remoteAddr); got != http.StatusOK {
		t.Fatalf("remote status = %d, want 200 after enabling allowRemoteNoApiKey; "+
			"the middleware appears to have cached the value", got)
	}

	// Turning the key requirement back on closes it again.
	seedSettings(t, repo, &on, &on)
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
	seedSettings(t, repo, &off, &on)

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

func boolPtr(b bool) *bool { return &b }
