package chat

import (
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// seedConnections inserts n active connections for a provider, all with the
// same priority so only the rotation can distinguish them.
func seedConnections(t *testing.T, repo *db.Repo, provider string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := provider + "-conn-" + string(rune('a'+i))
		if _, err := repo.DB().Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt, consecutiveUseCount)
			 VALUES (?, ?, 'apikey', ?, 1, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z', 0)`,
			id, provider, id,
		); err != nil {
			t.Fatalf("seed connection %s: %v", id, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// loadConns reads the connections back the way the request path does.
func loadConns(t *testing.T, repo *db.Repo, provider string) []*models.ProviderConnection {
	t.Helper()
	all, err := repo.GetAllProviderConnections()
	if err != nil {
		t.Fatalf("GetAllProviderConnections: %v", err)
	}
	var out []*models.ProviderConnection
	for _, c := range all {
		if c.Provider == provider {
			out = append(out, c)
		}
	}
	return out
}

// Turning on Round Robin for a provider must actually spread requests across its
// accounts. VansRouter exposes this as a toggle on the provider page writing
// providerStrategies[id].fallbackStrategy = "round-robin"; the Go build stored
// that key but never read it, so every request kept hitting the highest-priority
// account.
func TestAccountRoundRobinRotatesAcrossConnections(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	seedConnections(t, repo, "rrprov", 3)
	h := &ChatHandler{Repo: repo}
	strat := db.ProviderStrategy{FallbackStrategy: "round-robin", StickyRoundRobinLimit: 1}

	seen := map[string]int{}
	order := loadConns(t, repo, "rrprov")
	for i := 0; i < 6; i++ {
		order = h.applyConnectionStrategy("rrprov", loadConns(t, repo, "rrprov"), strat)
		if len(order) == 0 {
			t.Fatal("no connections returned")
		}
		seen[order[0].ID]++
	}

	if len(seen) != 3 {
		t.Errorf("round-robin used %d of 3 accounts: %v", len(seen), seen)
	}
	for id, n := range seen {
		if n != 2 {
			t.Errorf("account %s served %d of 6 requests, want 2: %v", id, n, seen)
		}
	}
}

// With the toggle off nothing may rotate: fill-first keeps the highest-priority
// account, which is the behaviour every existing install relies on.
func TestAccountRotationOffKeepsPriorityOrder(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	seedConnections(t, repo, "ffprov", 3)
	h := &ChatHandler{Repo: repo}
	// No strategy at all is the default state.
	strat := db.ProviderStrategy{}

	first := ""
	for i := 0; i < 5; i++ {
		order := h.applyConnectionStrategy("ffprov", loadConns(t, repo, "ffprov"), strat)
		if len(order) == 0 {
			t.Fatal("no connections")
		}
		if first == "" {
			first = order[0].ID
		} else if order[0].ID != first {
			t.Errorf("rotation happened without round-robin enabled: %s then %s", first, order[0].ID)
		}
	}
}

// stickyRoundRobinLimit decides how many consecutive requests one account
// serves. VansRouter defaults it to 3 because hopping every request discards the
// provider's prompt cache.
func TestAccountRoundRobinHonoursStickyLimit(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	seedConnections(t, repo, "stickprov", 2)
	h := &ChatHandler{Repo: repo}
	strat := db.ProviderStrategy{FallbackStrategy: "round-robin", StickyRoundRobinLimit: 3}

	var served []string
	for i := 0; i < 6; i++ {
		order := h.applyConnectionStrategy("stickprov", loadConns(t, repo, "stickprov"), strat)
		served = append(served, order[0].ID)
	}

	// Three in a row from one account, then three from the other.
	if served[0] != served[1] || served[1] != served[2] {
		t.Errorf("first account did not serve 3 consecutive requests: %v", served)
	}
	if served[3] != served[4] || served[4] != served[5] {
		t.Errorf("second account did not serve 3 consecutive requests: %v", served)
	}
	if served[0] == served[3] {
		t.Errorf("rotation never switched accounts with sticky limit 3: %v", served)
	}
}

// The rotation state must survive a restart. It lives in the connection row, so
// rebuilding the handler (as a restart does) must not send every request back to
// the same account.
func TestAccountRoundRobinSurvivesRestart(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	seedConnections(t, repo, "restartprov", 2)
	strat := db.ProviderStrategy{FallbackStrategy: "round-robin", StickyRoundRobinLimit: 1}

	// Three requests, then a fresh handler with no in-memory state.
	h1 := &ChatHandler{Repo: repo}
	served := []string{}
	for i := 0; i < 3; i++ {
		order := h1.applyConnectionStrategy("restartprov", loadConns(t, repo, "restartprov"), strat)
		served = append(served, order[0].ID)
	}

	h2 := &ChatHandler{Repo: repo}
	after := h2.applyConnectionStrategy("restartprov", loadConns(t, repo, "restartprov"), strat)

	if after[0].ID == served[len(served)-1] {
		t.Errorf("rotation restarted from the same account after restart: "+
			"last before=%s, first after=%s (served=%v)", served[len(served)-1], after[0].ID, served)
	}
}

// A connection that has never been used must be picked before one that has,
// otherwise a newly added account waits for a full cycle.
func TestAccountRoundRobinPrefersUnusedConnection(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	seedConnections(t, repo, "unusedprov", 2)
	// Mark the first as recently used so the second is the fresh one.
	if _, err := repo.DB().Exec(
		`UPDATE providerConnections SET lastUsedAt = ?, consecutiveUseCount = 1 WHERE id = 'unusedprov-conn-a'`,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("mark used: %v", err)
	}

	h := &ChatHandler{Repo: repo}
	strat := db.ProviderStrategy{FallbackStrategy: "round-robin", StickyRoundRobinLimit: 1}
	order := h.applyConnectionStrategy("unusedprov", loadConns(t, repo, "unusedprov"), strat)

	if order[0].ID != "unusedprov-conn-b" {
		t.Errorf("expected the never-used account first, got %s", order[0].ID)
	}
}

// rotateConnectionsSticky must return every connection, exactly once. A
// duplicate would let one account be tried twice while another is skipped.
func TestAccountRotationReturnsEachConnectionOnce(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	seedConnections(t, repo, "countprov", 4)
	h := &ChatHandler{Repo: repo}
	strat := db.ProviderStrategy{FallbackStrategy: "round-robin", StickyRoundRobinLimit: 1}

	order := h.applyConnectionStrategy("countprov", loadConns(t, repo, "countprov"), strat)
	if len(order) != 4 {
		t.Fatalf("got %d connections, want 4", len(order))
	}
	seen := map[string]bool{}
	for _, c := range order {
		if seen[c.ID] {
			t.Errorf("duplicate connection %s in %v", c.ID, order)
		}
		seen[c.ID] = true
	}
}

// A connection ranked by NextConnectionPriority must land BEHIND the accounts
// that are already established, not at the very back of the listing.
//
// This is the regression guard for the invisible-account bug. When the OAuth
// INSERT left priority NULL, the row sorted last in the listing query
// (ORDER BY ... priority IS NULL THEN 999999) and last in priorityOf, so a
// hand-added account only ever served after every ranked account was exhausted.
// Allocating max+1 puts it at the end of the queue, which is the intended
// behaviour measured here: it must rank below the existing accounts but ABOVE
// nothing — i.e. it is an ordinary queue member, not a permanent last resort.
func TestRankedNewcomerJoinsTheQueueInOrder(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	seedConnections(t, repo, "newprov", 3)
	// Distinct priorities, as a real install has them (1,2,3...). seedConnections
	// gives everyone 1, which would make priority a no-op for this test.
	for i, id := range []string{"newprov-conn-a", "newprov-conn-b", "newprov-conn-c"} {
		if _, err := repo.DB().Exec(`UPDATE providerConnections SET priority = ? WHERE id = ?`, i+1, id); err != nil {
			t.Fatalf("rank %s: %v", id, err)
		}
	}

	// What the OAuth save path now does instead of writing NULL.
	priority, err := repo.NextConnectionPriority("newprov")
	if err != nil {
		t.Fatalf("NextConnectionPriority: %v", err)
	}
	if priority != 4 {
		t.Fatalf("allocated priority = %d, want 4 (behind the three seeded accounts)", priority)
	}

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt, consecutiveUseCount)
		 VALUES ('newprov-newcomer', 'newprov', 'oauth', 'hand-added', ?, 1, '{}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z', 0)`,
		priority,
	); err != nil {
		t.Fatalf("seed ranked newcomer: %v", err)
	}

	h := &ChatHandler{Repo: repo}
	strat := db.ProviderStrategy{FallbackStrategy: "round-robin", StickyRoundRobinLimit: 1}

	// The listing must place it last among the ranked rows, and it is ranked —
	// so it is reachable. A NULL would have made it indistinguishable from
	// "unranked", which is what the fix removes.
	conns := loadConns(t, repo, "newprov")
	if conns[len(conns)-1].ID != "newprov-newcomer" {
		t.Errorf("ranked newcomer should sort last among ranked rows, got order %v", ids(conns))
	}

	// Round-robin must treat it as a full member: it takes a turn.
	led := map[string]bool{}
	for i := 0; i < 4; i++ {
		rotated := h.applyConnectionStrategy("newprov", loadConns(t, repo, "newprov"), strat)
		led[rotated[0].ID] = true
	}
	if !led["newprov-newcomer"] {
		t.Errorf("the ranked newcomer never served over 4 rounds; it is not in the rotation")
	}
}

func ids(conns []*models.ProviderConnection) []string {
	out := make([]string, 0, len(conns))
	for _, c := range conns {
		out = append(out, c.ID)
	}
	return out
}
