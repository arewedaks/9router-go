package chat

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// lockConnModel writes a modelLock_<model> into a connection's data blob, the
// same shape the fallback path writes on a 429.
func lockConnModel(t *testing.T, database *sql.DB, connID, model string, until time.Time) {
	t.Helper()
	_, err := database.Exec(
		`UPDATE providerConnections SET data = json_set(data, ?, ?) WHERE id = ?`,
		"$.modelLock_"+model, until.UTC().Format(time.RFC3339), connID)
	if err != nil {
		t.Fatalf("lock %s: %v", connID, err)
	}
}

// Rotation must scale: measure the DB-backed path at 1000 accounts and assert
// the per-request cost stays sub-linear (no O(n^2) walk).
func TestRoundRobinScale1000(t *testing.T) {
	const n = 1000
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	provider := "openai-compatible-chat-scale"
	seedManyConns(t, database, provider, n)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")
	if err := repo.SetProviderStrategy(provider, db.ProviderStrategy{
		AccountStrategy: "round-robin", FallbackStrategy: "round-robin", StickyRoundRobinLimit: 1,
	}); err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}

	// Warm up, then time a run of 200 requests (not 1000) so the test stays fast
	// while still sampling the rotation at full pool size.
	for i := 0; i < 5; i++ {
		if _, _, err := h.GetBestConnection(provider, "", nil, ""); err != nil {
			t.Fatalf("warmup %d: %v", i, err)
		}
	}
	const probes = 200
	seen := map[string]int{}
	start := time.Now()
	for i := 0; i < probes; i++ {
		conn, _, err := h.GetBestConnection(provider, "", nil, "")
		if err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
		seen[conn.ID]++
	}
	elapsed := time.Since(start)
	perReq := float64(elapsed.Microseconds()) / float64(probes)

	// A 1000-account pool must not repeat an account inside a single pass:
	// 200 probes must all land on distinct accounts.
	if len(seen) != probes {
		t.Errorf("distinct accounts served = %d, want %d (rotation repeated an account before finishing the pass)", len(seen), probes)
	}
	t.Logf("1000-account pool: %d requests in %v (%.0f µs/request)", probes, elapsed, perReq)

	// Guard against accidental O(n^2): at 1000 accounts a per-request cost above
	// ~50 ms would mean the handler is walking the whole pool for every call.
	if perReq > 50_000 {
		t.Errorf("per-request rotation cost %.0f µs at n=%d looks superlinear", perReq, n)
	}
}

// With most accounts locked for the model, rotation must still pick a free one
// and never hand back a locked account.
func TestRoundRobinSkipsLockedAmongMany(t *testing.T) {
	const n = 300
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	provider := "openai-compatible-chat-locked"
	seedManyConns(t, database, provider, n)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")
	const model = "test-model"
	if err := repo.SetProviderStrategy(provider, db.ProviderStrategy{
		AccountStrategy: "round-robin", FallbackStrategy: "round-robin", StickyRoundRobinLimit: 1,
	}); err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}

	// Lock all but the last 5 accounts for this model.
	free := map[string]bool{}
	for i := n - 5; i < n; i++ {
		free[fmt.Sprintf("%s-conn-%04d", provider, i)] = true
	}
	for i := 0; i < n-5; i++ {
		lockConnModel(t, database, fmt.Sprintf("%s-conn-%04d", provider, i), model, time.Now().Add(time.Hour))
	}

	served := map[string]int{}
	for i := 0; i < 25; i++ {
		conn, _, err := h.GetBestConnection(provider, "", nil, model)
		if err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
		if !free[conn.ID] {
			t.Fatalf("probe %d: served locked account %s", i, conn.ID)
		}
		served[conn.ID]++
	}
	if len(served) != 5 {
		t.Errorf("distinct free accounts served = %d, want 5 (rotation is not spreading across the free ones)", len(served))
	}
}
