package chat

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
)

// TestAccountFallback_SecondAccountUsedWhenFirstIsRateLimited is the direct
// answer to "if account 1 is rate limited, does it try account 2?".
//
// Two accounts point at two different upstream servers. The first answers 429
// (rate limited); the second answers 200. The request must succeed, and the
// second account must be the one that served it.
func TestAccountFallback_SecondAccountUsedWhenFirstIsRateLimited(t *testing.T) {
	var firstHits, secondHits atomic.Int32

	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer limited.Close()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"chatcmpl-2","object":"chat.completion","created":0,"model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"served by account 2"},"finish_reason":"stop"}]}`))
	}))
	defer healthy.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	// Drop the helper's pre-seeded connections so only ours exist.
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1','conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}

	seed := func(connID, apiKey, baseURL string, priority int) {
		data, _ := json.Marshal(map[string]any{"apiKey": apiKey, "baseUrl": baseURL})
		// authType must NOT be oauth or the connection is probed for a project id.
		if _, err := database.Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
			 VALUES (?, 'deepseek', 'apikey', 'acct', ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			connID, priority, string(data)); err != nil {
			t.Fatalf("seed %s: %v", connID, err)
		}
	}
	seed("acct-1", "sk-account-1", limited.URL, 1)
	seed("acct-2", "sk-account-2", healthy.URL, 2)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	rec := httptest.NewRecorder()
	err := h.handleAccountFallback(context.Background(), rec, "deepseek", "deepseek-chat", "", body, false, false, "/v1/chat/completions")

	if err != nil {
		t.Fatalf("expected the second account to serve the request, got error: %v", err)
	}
	if got := firstHits.Load(); got != 1 {
		t.Errorf("account 1 hits = %d, want 1", got)
	}
	if got := secondHits.Load(); got != 1 {
		t.Errorf("account 2 hits = %d, want 1 (fallback must reach the second account)", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !contains(rec.Body.String(), "served by account 2") {
		t.Errorf("response did not come from account 2: %s", rec.Body.String()[:min(200, rec.Body.Len())])
	}

	// The limited account must be locked so the NEXT request skips it.
	locked, lerr := repo.IsConnectionModelLocked("acct-1", "deepseek-chat")
	if lerr != nil {
		t.Fatalf("IsConnectionModelLocked: %v", lerr)
	}
	if !locked {
		t.Error("account 1 should be locked for this model after 429")
	}
}

// TestAccountFallback_AllAccountsExhausted checks the negative case: when every
// account is limited the request fails rather than looping forever.
func TestAccountFallback_AllAccountsExhausted(t *testing.T) {
	var hits atomic.Int32
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer limited.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1','conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}

	for i, id := range []string{"acct-1", "acct-2"} {
		data, _ := json.Marshal(map[string]any{"apiKey": "sk-" + id, "baseUrl": limited.URL})
		if _, err := database.Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
			 VALUES (?, 'deepseek', 'apikey', 'acct', ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			id, i+1, string(data)); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	rec := httptest.NewRecorder()
	err := h.handleAccountFallback(context.Background(), rec, "deepseek", "deepseek-chat", "", body, false, false, "/v1/chat/completions")

	if err == nil {
		t.Error("expected an error when every account is rate limited")
	}
	// Both accounts should have been tried exactly once each.
	if got := hits.Load(); got != 2 {
		t.Errorf("upstream hits = %d, want 2 (both accounts tried)", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 ||
		indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
