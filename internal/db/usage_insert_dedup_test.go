package db

import (
	"testing"
	"time"
)

// A completion callback that fires twice for the same request used to append
// two identical usageHistory rows, which showed up in Recent Requests as a
// duplicated hit. Upstream VansRouter guards this before INSERT; these tests
// pin the same behaviour.
//
// The timestamp is truncated to the second by InsertUsageHistory, so two calls
// in the same test land on the same value and the guard can match them.
func TestInsertUsageHistorySkipsExactDuplicate(t *testing.T) {
	r := usageTestDB(t)

	for i := 0; i < 3; i++ {
		if err := r.InsertUsageHistory("openai", "gpt-4o", "conn-1", "sk-x", "/v1/chat",
			100, 50, 0.01, "success", 150, "{}", `{"prompt_tokens":100,"completion_tokens":50}`); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	if n := r.countUsageHistory(t); n != 1 {
		t.Errorf("usageHistory rows = %d, want 1: an identical re-call must not "+
			"append a second row", n)
	}

	s, err := r.GetUsageStats("24h", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s.RecentRequests); got != 1 {
		t.Errorf("RecentRequests = %d, want 1", got)
	}
}

// The guard must be narrow. A genuine second request in the same second with
// different token counts is a different request and has to be recorded: the key
// includes the counts precisely so this case is not swallowed.
func TestInsertUsageHistoryKeepsDistinctCounts(t *testing.T) {
	r := usageTestDB(t)

	if err := r.InsertUsageHistory("openai", "gpt-4o", "conn-1", "sk-x", "/v1/chat",
		100, 50, 0.01, "success", 150, "{}", `{"prompt_tokens":100,"completion_tokens":50}`); err != nil {
		t.Fatal(err)
	}
	if err := r.InsertUsageHistory("openai", "gpt-4o", "conn-1", "sk-x", "/v1/chat",
		101, 50, 0.01, "success", 151, "{}", `{"prompt_tokens":101,"completion_tokens":50}`); err != nil {
		t.Fatal(err)
	}

	if n := r.countUsageHistory(t); n != 2 {
		t.Errorf("usageHistory rows = %d, want 2: different token counts are "+
			"different requests", n)
	}
}

// A different provider or model with the same counts is also a different
// request, so the guard must not collapse those.
func TestInsertUsageHistorySeparatesProviderAndModel(t *testing.T) {
	r := usageTestDB(t)

	if err := r.InsertUsageHistory("openai", "gpt-4o", "conn-1", "sk-x", "/v1/chat",
		100, 50, 0.01, "success", 150, "{}", `{"prompt_tokens":100,"completion_tokens":50}`); err != nil {
		t.Fatal(err)
	}
	if err := r.InsertUsageHistory("anthropic", "claude", "conn-1", "sk-x", "/v1/chat",
		100, 50, 0.01, "success", 150, "{}", `{"prompt_tokens":100,"completion_tokens":50}`); err != nil {
		t.Fatal(err)
	}

	if n := r.countUsageHistory(t); n != 2 {
		t.Errorf("usageHistory rows = %d, want 2: a different provider/model is "+
			"a different request", n)
	}
}

// When the duplicate row was written without an endpoint (legacy shape) and the
// re-call knows one, the guard backfills it instead of discarding the new
// information.
func TestInsertUsageHistoryBackfillsMissingEndpoint(t *testing.T) {
	r := usageTestDB(t)

	if err := r.InsertUsageHistory("openai", "gpt-4o", "conn-1", "sk-x", "",
		100, 50, 0.01, "success", 150, "{}", `{"prompt_tokens":100,"completion_tokens":50}`); err != nil {
		t.Fatal(err)
	}
	if err := r.InsertUsageHistory("openai", "gpt-4o", "conn-1", "sk-x", "/v1/chat",
		100, 50, 0.01, "success", 150, "{}", `{"prompt_tokens":100,"completion_tokens":50}`); err != nil {
		t.Fatal(err)
	}

	if n := r.countUsageHistory(t); n != 1 {
		t.Fatalf("usageHistory rows = %d, want 1", n)
	}
	var ep string
	if err := r.db.QueryRow(`SELECT COALESCE(endpoint,'') FROM usageHistory`).Scan(&ep); err != nil {
		t.Fatal(err)
	}
	if ep != "/v1/chat" {
		t.Errorf("endpoint = %q, want %q: the guard should backfill it", ep, "/v1/chat")
	}
}

func (r *Repo) countUsageHistory(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM usageHistory`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
