package chat

import (
	"database/sql"
	"fmt"
	json "encoding/json/v2"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// seedManyConns inserts n active connections for one provider, all with the
// same apiKey so the fallback walk has something to route to.
func seedManyConns(t *testing.T, database *sql.DB, provider string, n int) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-many", "baseUrl": "https://example.invalid/v1"})
	tx, err := database.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt, consecutiveUseCount, latencyMs)
		VALUES (?, ?, 'apikey', ?, ?, 1, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 0, 0)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-conn-%04d", provider, i)
		if _, err := stmt.Exec(id, provider, id, i, string(data)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// A provider with hundreds of accounts must still rotate: every account should
// be offered a turn before any is reused, and the ordering must not degrade.
func TestRoundRobinManyAccounts(t *testing.T) {
	const n = 300
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	provider := "openai-compatible-chat-many"
	seedManyConns(t, database, provider, n)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")

	// round-robin with sticky limit 1: one account per request
	if err := repo.SetProviderStrategy(provider, db.ProviderStrategy{
		AccountStrategy:       "round-robin",
		FallbackStrategy:      "round-robin",
		StickyRoundRobinLimit: 1,
	}); err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}

	conns, err := repo.GetProviderConnections(provider, true)
	if err != nil {
		t.Fatalf("GetProviderConnections: %v", err)
	}
	if len(conns) != n {
		t.Fatalf("seeded %d connections, fetched %d", n, len(conns))
	}

	seen := map[string]int{}
	order := make([]string, 0, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		rotated := h.ApplyConnectionStrategy(provider, conns, db.ProviderStrategy{
			AccountStrategy:       "round-robin",
			FallbackStrategy:      "round-robin",
			StickyRoundRobinLimit: 1,
		})
		if len(rotated) == 0 {
			t.Fatalf("iteration %d: rotation returned empty", i)
		}
		first := rotated[0].ID
		seen[first]++
		order = append(order, first)
	}
	elapsed := time.Since(start)

	// Every account must have served exactly once in the first n requests.
	if len(seen) != n {
		t.Errorf("distinct accounts served = %d, want %d (rotation is skipping accounts)", len(seen), n)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("account %s served %d times in %d requests, want 1", id, count, n)
		}
	}
	t.Logf("rotated %d accounts in %v (%.1f µs/request)", n, elapsed, float64(elapsed.Microseconds())/float64(n))
}

// The same 300 accounts, but ordered through the full DB-backed path the chat
// handler uses, must also spread evenly.
func TestRoundRobinManyAccounts_ViaGetBestConnection(t *testing.T) {
	const n = 300
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	provider := "openai-compatible-chat-many2"
	seedManyConns(t, database, provider, n)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")

	if err := repo.SetProviderStrategy(provider, db.ProviderStrategy{
		AccountStrategy:       "round-robin",
		FallbackStrategy:      "round-robin",
		StickyRoundRobinLimit: 1,
	}); err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}

	seen := map[string]int{}
	start := time.Now()
	for i := 0; i < n; i++ {
		conn, _, err := h.GetBestConnection(provider, "", nil, "")
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if conn == nil {
			t.Fatalf("iteration %d: nil connection", i)
		}
		seen[conn.ID]++
	}
	elapsed := time.Since(start)

	if len(seen) != n {
		t.Errorf("distinct accounts served = %d, want %d", len(seen), n)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("account %s served %d times in %d requests, want 1", id, count, n)
		}
	}
	t.Logf("GetBestConnection rotated %d accounts in %v (%.1f ms/request)",
		n, elapsed, float64(elapsed.Milliseconds())/float64(n))
}

var _ = models.ProviderConnection{}
