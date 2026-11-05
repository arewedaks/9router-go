package chat

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// seedConnWithPriority inserts one active connection with an explicit priority.
func seedConnWithPriority(t *testing.T, database *sql.DB, provider, id string, priority int) {
	t.Helper()
	_, err := database.Exec(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt, consecutiveUseCount, latencyMs)
		VALUES (?, ?, 'apikey', ?, ?, 1, '{"apiKey":"sk-f","baseUrl":"https://example.invalid/v1"}',
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 0, 0)`,
		id, provider, id, priority)
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

// fill-first must pin the highest-priority account and never rotate while it
// serves. This is the historical default and the mode that keeps prompt caches
// warm on one account.
func TestFillFirstPinsHighestPriority(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	provider := "fillfirst-basic"
	seedConnWithPriority(t, database, provider, provider+"-p1", 1)
	seedConnWithPriority(t, database, provider, provider+"-p5", 5)
	seedConnWithPriority(t, database, provider, provider+"-p9", 9)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")
	if err := repo.SetProviderStrategy(provider, db.ProviderStrategy{
		AccountStrategy: "fill-first", FallbackStrategy: "fill-first",
	}); err != nil {
		t.Fatalf("strategy: %v", err)
	}

	for i := 0; i < 10; i++ {
		conn, _, err := h.GetBestConnection(provider, "", nil, "m")
		if err != nil {
			t.Fatalf("req %d: %v", i, err)
		}
		if conn.ID != provider+"-p1" {
			t.Fatalf("req %d: served %s, want %s (fill-first must not rotate)",
				i, conn.ID, provider+"-p1")
		}
	}
}

// The critical fill-first case at scale: when the pinned account is locked for
// the model, the walk must fall through to the next priority — and it must not
// repeat the "stuck on one account" failure that round-robin had.
func TestFillFirstFallsThroughWhenPinnedLocked(t *testing.T) {
	const n = 200
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	provider := "fillfirst-locked"

	// Priorities 0..n-1, so order is unambiguous.
	for i := 0; i < n; i++ {
		seedConnWithPriority(t, database, provider, fmt.Sprintf("%s-%04d", provider, i), i)
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")
	const model = "solar-pro4"
	if err := repo.SetProviderStrategy(provider, db.ProviderStrategy{
		AccountStrategy: "fill-first", FallbackStrategy: "fill-first",
	}); err != nil {
		t.Fatalf("strategy: %v", err)
	}

	// First 10 accounts locked for the model.
	for i := 0; i < 10; i++ {
		lockConnModel(t, database, fmt.Sprintf("%s-%04d", provider, i), model, time.Now().Add(time.Hour))
	}

	// fill-first must serve the 11th (priority 10) and keep serving it.
	for i := 0; i < 5; i++ {
		conn, _, err := h.GetBestConnection(provider, "", nil, model)
		if err != nil {
			t.Fatalf("req %d: %v", i, err)
		}
		want := fmt.Sprintf("%s-0010", provider)
		if conn.ID != want {
			t.Fatalf("req %d: served %s, want %s (fill-first must pick the first unlocked by priority)",
				i, conn.ID, want)
		}
	}

	// Lock the 11th too; it must advance to the 12th.
	lockConnModel(t, database, fmt.Sprintf("%s-0010", provider), model, time.Now().Add(time.Hour))
	conn, _, err := h.GetBestConnection(provider, "", nil, model)
	if err != nil {
		t.Fatalf("after locking 0010: %v", err)
	}
	if want := fmt.Sprintf("%s-0011", provider); conn.ID != want {
		t.Errorf("served %s, want %s (must advance to the next priority)", conn.ID, want)
	}
}

// All accounts locked: the caller must get a clear "no available connections"
// error, not a nil connection or a panic.
func TestFillFirstAllLocked(t *testing.T) {
	const n = 50
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	provider := "fillfirst-alllocked"
	for i := 0; i < n; i++ {
		seedConnWithPriority(t, database, provider, fmt.Sprintf("%s-%04d", provider, i), i)
	}
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")
	const model = "solar-pro4"
	if err := repo.SetProviderStrategy(provider, db.ProviderStrategy{
		AccountStrategy: "fill-first", FallbackStrategy: "fill-first",
	}); err != nil {
		t.Fatalf("strategy: %v", err)
	}
	for i := 0; i < n; i++ {
		lockConnModel(t, database, fmt.Sprintf("%s-%04d", provider, i), model, time.Now().Add(time.Hour))
	}

	conn, _, err := h.GetBestConnection(provider, "", nil, model)
	if err == nil {
		t.Fatalf("expected an error when every account is locked, got conn=%v", conn)
	}
	if conn != nil {
		t.Errorf("expected nil connection on total lockout, got %s", conn.ID)
	}
}

// fill-first must survive the whole pool being sampled: after n requests it must
// still be on the first account (no rotation bookkeeping drifting it away).
func TestFillFirstStableAcrossManyRequests(t *testing.T) {
	const n = 300
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	provider := "fillfirst-stable"
	for i := 0; i < n; i++ {
		seedConnWithPriority(t, database, provider, fmt.Sprintf("%s-%04d", provider, i), i)
	}
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")
	if err := repo.SetProviderStrategy(provider, db.ProviderStrategy{
		AccountStrategy: "fill-first", FallbackStrategy: "fill-first",
	}); err != nil {
		t.Fatalf("strategy: %v", err)
	}

	served := map[string]int{}
	for i := 0; i < n; i++ {
		conn, _, err := h.GetBestConnection(provider, "", nil, "")
		if err != nil {
			t.Fatalf("req %d: %v", i, err)
		}
		served[conn.ID]++
	}
	if len(served) != 1 {
		t.Errorf("fill-first used %d distinct accounts, want 1", len(served))
	}
	for id, c := range served {
		if c != n {
			t.Errorf("account %s served %d of %d, want all", id, c, n)
		}
	}
}
