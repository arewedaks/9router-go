package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// seedRequireAPIKey writes the settings row with requireApiKey set, or absent
// when value is nil. Only the keys under test are written so the row stays a
// realistic stand-in for a migrated VansRouter blob.
//
// setupTestDB only creates apiKeys, so the settings table is created here rather
// than widening the shared helper for the whole package.
func seedRequireAPIKey(t *testing.T, repo *db.Repo, value *bool) {
	t.Helper()
	if _, err := repo.RawDB().Exec(
		`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
	blob := `{"rtkEnabled": true}`
	if value != nil {
		if *value {
			blob = `{"rtkEnabled": true, "requireApiKey": true}`
		} else {
			blob = `{"rtkEnabled": true, "requireApiKey": false}`
		}
	}
	if _, err := repo.RawDB().Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		blob); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
}

// engineRequest is a bare /v1 call with no credential at all — the shape a
// client without a key would send.
func engineRequest(t *testing.T, mw func(http.Handler) http.Handler) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	mw(okMarkerHandler()).ServeHTTP(rec, req)
	return rec.Code
}

// With requireApiKey set to false, an unauthenticated /v1 request must reach the
// handler. This is the whole point of the flag: 9router-go and VansRouter share
// one settings row, so the value flipped in either dashboard has to control this
// build's gate too.
func TestRequireAPIKeyOffLetsEngineThrough(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	off := false
	seedRequireAPIKey(t, repo, &off)

	mw := RequireApiKeyUnlessDisabled(repo, nil)
	if got := engineRequest(t, mw); got != http.StatusOK {
		t.Fatalf("status = %d, want 200 with requireApiKey=false", got)
	}
}

// With the flag on, or absent, the gate must still refuse. Absent is the case
// that matters for migration: a VansRouter database that never wrote the key
// must not silently become an open proxy.
func TestRequireAPIKeyOnOrAbsentBlocksEngine(t *testing.T) {
	for name, value := range map[string]*bool{
		"on":     boolPtr(true),
		"absent": nil,
	} {
		t.Run(name, func(t *testing.T) {
			database, cleanup := setupTestDB(t)
			defer cleanup()
			repo := db.NewRepo(database)
			seedRequireAPIKey(t, repo, value)

			mw := RequireApiKeyUnlessDisabled(repo, nil)
			if got := engineRequest(t, mw); got != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 when requireApiKey is %s", got, name)
			}
		})
	}
}

// The flag is read per request, not captured when the router is built. Flipping
// it in the database must change behaviour on the next request, with no restart.
func TestRequireAPIKeyIsReadPerRequest(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	on := true
	seedRequireAPIKey(t, repo, &on)

	// One middleware instance for the whole test: this is what a live router
	// holds, so a captured value would show up as a stale result below.
	mw := RequireApiKeyUnlessDisabled(repo, nil)

	if got := engineRequest(t, mw); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 while the flag is on", got)
	}

	off := false
	seedRequireAPIKey(t, repo, &off)
	if got := engineRequest(t, mw); got != http.StatusOK {
		t.Fatalf("status = %d, want 200 after flipping the flag off; the "+
			"middleware appears to have cached the value", got)
	}

	seedRequireAPIKey(t, repo, &on)
	if got := engineRequest(t, mw); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 after flipping the flag back on", got)
	}
}

// A settings read failure must fail closed. A database error is not a reason to
// serve the proxy unauthenticated.
func TestRequireAPIKeyFailsClosedOnRepoError(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	off := false
	seedRequireAPIKey(t, repo, &off)

	// Confirm the open path first, so the assertion below cannot pass merely
	// because the gate never opens.
	mw := RequireApiKeyUnlessDisabled(repo, nil)
	if got := engineRequest(t, mw); got != http.StatusOK {
		t.Fatalf("precondition failed: status = %d, want 200", got)
	}

	// Drop the table the read depends on, then ask again.
	if _, err := repo.RawDB().Exec(`DROP TABLE settings`); err != nil {
		t.Fatalf("drop settings table: %v", err)
	}
	if got := engineRequest(t, mw); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when the settings read fails", got)
	}
}

func boolPtr(b bool) *bool { return &b }
