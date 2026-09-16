package oauth

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// setupRefreshableProvider registers a fake OAuth provider config + refresher
// pointing at the given token URL, and returns a cleanup that restores the
// previous registry state. Tests using it must not run in parallel.
func setupRefreshableProvider(t *testing.T, provider, tokenURL string) {
	t.Helper()

	prevCfg, hadCfg := providers.KnownOAuthConfigs[provider]
	providers.KnownOAuthConfigs[provider] = providers.OAuthClientConfig{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		TokenURL:     tokenURL,
	}

	prevRefresher := Get(provider)
	// No per-provider refresher, so runRefresher takes the standard OAuth2 path
	// against the httptest token endpoint.
	registryMu.Lock()
	delete(registry, provider)
	registryMu.Unlock()

	t.Cleanup(func() {
		if hadCfg {
			providers.KnownOAuthConfigs[provider] = prevCfg
		} else {
			delete(providers.KnownOAuthConfigs, provider)
		}
		if prevRefresher != nil {
			Register(provider, prevRefresher)
		}
	})
}

// insertOAuthConnection seeds one providerConnections row and returns the repo.
func insertOAuthConnection(t *testing.T, id, provider, data string) *db.Repo {
	t.Helper()
	database := newStoreTestDB(t)
	_, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, ?, 'oauth', ?, 1, 1, ?, ?, ?)`,
		id, provider, id, data, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	return db.NewRepo(database)
}

// TestRefreshStoredConnection_SkipsValidToken proves a token that is not yet
// expired is returned untouched and NO network call is made. Without this,
// every probe and chat request would refresh needlessly and burn the provider's
// refresh quota (and risk refresh_token_reused).
func TestRefreshStoredConnection_SkipsValidToken(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`{"access_token":"should-not-be-used","expires_in":3600}`))
	}))
	defer srv.Close()

	setupRefreshableProvider(t, "skipvalid", srv.URL)

	future := time.Now().Add(30 * time.Minute).Format(time.RFC3339)
	data := `{"accessToken":"still-good","refreshToken":"r1","expiresAt":"` + future + `","projectId":"proj-1"}`
	repo := insertOAuthConnection(t, "conn-valid", "skipvalid", data)

	token, projectID, refreshed, err := RefreshStoredConnection(context.Background(), repo, srv.Client(), "conn-valid", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refreshed {
		t.Fatal("must not refresh a still-valid token")
	}
	if token != "still-good" || projectID != "proj-1" {
		t.Fatalf("returned %q/%q, want still-good/proj-1", token, projectID)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("token endpoint was called %d times; expected 0", n)
	}
}

// TestRefreshStoredConnection_RefreshesExpiredToken proves the happy path: an
// expired token is exchanged and the fresh values are persisted.
func TestRefreshStoredConnection_RefreshesExpiredToken(t *testing.T) {
	var gotRefreshToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotRefreshToken = r.FormValue("refresh_token")
		w.Write([]byte(`{"access_token":"fresh-token","expires_in":3600}`))
	}))
	defer srv.Close()

	setupRefreshableProvider(t, "expired", srv.URL)

	past := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	data := `{"accessToken":"stale","refreshToken":"r-orig","expiresAt":"` + past + `","projectId":"proj-2"}`
	repo := insertOAuthConnection(t, "conn-expired", "expired", data)

	token, projectID, refreshed, err := RefreshStoredConnection(context.Background(), repo, srv.Client(), "conn-expired", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !refreshed {
		t.Fatal("expired token should have been refreshed")
	}
	if token != "fresh-token" {
		t.Fatalf("returned token %q, want fresh-token", token)
	}
	if projectID != "proj-2" {
		t.Fatalf("project id should be preserved: got %q", projectID)
	}
	if gotRefreshToken != "r-orig" {
		t.Fatalf("refresh endpoint received refresh_token %q, want r-orig", gotRefreshToken)
	}

	// The new token must be persisted, or the next request refreshes again.
	var stored string
	if err := repo.RawDB().QueryRow("SELECT data FROM providerConnections WHERE id = ?", "conn-expired").Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stored), &parsed); err != nil {
		t.Fatalf("parse stored: %v", err)
	}
	if parsed["accessToken"] != "fresh-token" {
		t.Fatalf("fresh token not persisted: %#v", parsed["accessToken"])
	}
}

// TestRefreshStoredConnection_NoRefreshTokenIsNoop proves API-key connections
// (and OAuth rows without a refresh token) are left alone rather than erroring —
// the dashboard probe must still be able to run against them.
func TestRefreshStoredConnection_NoRefreshTokenIsNoop(t *testing.T) {
	repo := insertOAuthConnection(t, "conn-apikey", "someapikey", `{"apiKey":"sk-abc"}`)

	token, _, refreshed, err := RefreshStoredConnection(context.Background(), repo, nil, "conn-apikey", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refreshed {
		t.Fatal("a connection without a refresh token must not be refreshed")
	}
	_ = token
}

// TestRefreshStoredConnection_ConcurrentCallersRefreshOnceFromEmptyState is the
// regression test for the bug this whole change exists to prevent: N goroutines
// hammering the same expired connection must produce exactly ONE refresh, not N.
//
// It is run with -race in CI; a broken lock shows up either as >1 hit (both
// callers saw the stale expiry) or as a race report.
func TestRefreshStoredConnection_ConcurrentCallersRefreshOnce(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(15 * time.Millisecond) // widen the window so losers would race
		w.Write([]byte(`{"access_token":"fresh-concurrent","expires_in":3600}`))
	}))
	defer srv.Close()

	setupRefreshableProvider(t, "concurrent", srv.URL)

	past := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	data := `{"accessToken":"stale","refreshToken":"r-conc","expiresAt":"` + past + `"}`
	repo := insertOAuthConnection(t, "conn-conc", "concurrent", data)

	const callers = 12
	var wg sync.WaitGroup
	var refreshedCount int32
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, refreshed, err := RefreshStoredConnection(context.Background(), repo, srv.Client(), "conn-conc", false)
			if err != nil {
				t.Errorf("caller error: %v", err)
				return
			}
			if refreshed {
				atomic.AddInt32(&refreshedCount, 1)
			}
		}()
	}
	wg.Wait()

	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("token endpoint called %d times for %d concurrent callers; want exactly 1", n, callers)
	}
	// Exactly one caller performed the refresh; the rest observed the fresh token
	// (thanks to the in-lock re-check).
	if n := atomic.LoadInt32(&refreshedCount); n != 1 {
		t.Fatalf("%d callers reported performing a refresh; want exactly 1", n)
	}
}
