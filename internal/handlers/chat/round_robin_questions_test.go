package chat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// pad4 formats an index as a 4-digit suffix, matching seedManyConns' ids.
func pad4(i int) string {
	s := strconv.Itoa(i)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

// connIndex extracts the numeric suffix from an id like "<provider>-conn-0042".
func connIndex(id string) int {
	i := strings.LastIndex(id, "-")
	if i < 0 {
		return -1
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil {
		return -1
	}
	return n
}

// Q1: a never-used account must be served before any account that has already
// served, so a newly added account does not wait for a full cycle of 273.
func TestRoundRobinPrefersNeverUsedAtScale(t *testing.T) {
	const n = 100
	const alreadyUsed = 40
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	provider := "rr-prefer-unused"
	seedManyConns(t, database, provider, n)

	// Mark the first 40 as already used, leaving 60 never used.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < alreadyUsed; i++ {
		if _, err := database.Exec(
			`UPDATE providerConnections SET lastUsedAt=?, consecutiveUseCount=1 WHERE id=?`,
			now, provider+"-conn-"+pad4(i)); err != nil {
			t.Fatalf("mark used %d: %v", i, err)
		}
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")
	if err := repo.SetProviderStrategy(provider, db.ProviderStrategy{
		AccountStrategy: "round-robin", FallbackStrategy: "round-robin", StickyRoundRobinLimit: 1,
	}); err != nil {
		t.Fatalf("strategy: %v", err)
	}

	// Every request in the first (n - alreadyUsed) must land on a never-used
	// account (index >= alreadyUsed).
	servedFresh, servedStale := 0, 0
	for i := 0; i < n-alreadyUsed; i++ {
		conn, _, err := h.GetBestConnection(provider, "", nil, "")
		if err != nil {
			t.Fatalf("req %d: %v", i, err)
		}
		if idx := connIndex(conn.ID); idx >= alreadyUsed {
			servedFresh++
		} else {
			servedStale++
		}
	}
	if servedStale != 0 {
		t.Errorf("%d already-used accounts were served while %d never-used ones remained",
			servedStale, n-alreadyUsed-servedFresh)
	}
	if servedFresh != n-alreadyUsed {
		t.Errorf("served %d never-used accounts in the first %d requests, want %d",
			servedFresh, n-alreadyUsed, n-alreadyUsed)
	}
}

// Q2: an account that is dead (upstream error) must be locked, skipped, and the
// request must succeed on another account — all within one client request.
func TestRoundRobinFallsBackWhenAccountDies(t *testing.T) {
	var deadHits, liveHits atomic.Int32

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadHits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key","type":"auth_error"}}`))
	}))
	defer dead.Close()
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		liveHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"alive"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer live.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	provider := "rr-deadacct"
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE provider='deepseek'`); err != nil {
		t.Fatalf("clear seeded: %v", err)
	}
	seedConnDB(t, database, provider, provider+"-dead", "sk-dead", dead.URL)
	seedConnDB(t, database, provider, provider+"-live", "sk-live", live.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")
	if err := repo.SetProviderStrategy(provider, db.ProviderStrategy{
		AccountStrategy: "round-robin", FallbackStrategy: "round-robin", StickyRoundRobinLimit: 1,
	}); err != nil {
		t.Fatalf("strategy: %v", err)
	}

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	rec := httptest.NewRecorder()
	err := h.handleAccountFallback(context.Background(), rec, provider, "m", "", body, false, false, "/v1/chat/completions")
	if err != nil {
		t.Fatalf("request failed instead of falling back: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if deadHits.Load() == 0 {
		t.Error("the dead account was never tried; the test did not exercise fallback")
	}
	if liveHits.Load() == 0 {
		t.Error("the live account never served the request; no fallback happened")
	}
	t.Logf("dead hits=%d live hits=%d (fallback worked)", deadHits.Load(), liveHits.Load())

	// The dead account must now be locked for the model so later requests skip it.
	locked, err := repo.IsConnectionModelLocked(provider+"-dead", "m")
	if err != nil {
		t.Fatalf("IsConnectionModelLocked: %v", err)
	}
	if !locked {
		t.Error("the dead account was not locked; it will be retried on every request")
	}
}

var _ = fmt.Sprintf
