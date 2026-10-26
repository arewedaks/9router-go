package chat

import (
	"sort"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

func threeConns() []*models.ProviderConnection {
	p := func(n int) *int { return &n }
	return []*models.ProviderConnection{
		{ID: "conn-1", Priority: p(1)},
		{ID: "conn-2", Priority: p(2)},
		{ID: "conn-3", Priority: p(3)},
	}
}

// accountStrategy must win over rotateStrategy: the latter describes proxy
// pools, and letting it pick accounts pinned every request to one account
// whenever the pool rotation was "sticky".
func TestAccountStrategyFillFirstBeatsRotateStrategy(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetConnectionState("")

	strat := db.ProviderStrategy{RotateStrategy: "round-robin", AccountStrategy: "fill-first"}
	for i := 0; i < 3; i++ {
		got := h.ApplyConnectionStrategy("antigravity", threeConns(), strat)
		if got[0].ID != "conn-1" {
			t.Fatalf("call %d: expected priority order (conn-1), got %s", i+1, got[0].ID)
		}
	}
}

// Blobs written before accountStrategy existed only carry the round-robin
// toggle, and they must keep rotating.
func TestLegacyRoundRobinToggleStillRotates(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetConnectionState("")

	strat := db.ProviderStrategy{FallbackStrategy: "round-robin"}
	first := h.ApplyConnectionStrategy("antigravity", threeConns(), strat)[0].ID
	// Default sticky limit is 3, so the first three requests stay on one account.
	for i := 0; i < 2; i++ {
		if got := h.ApplyConnectionStrategy("antigravity", threeConns(), strat)[0].ID; got != first {
			t.Fatalf("call %d: rotation moved inside the sticky run, got %s want %s", i+2, got, first)
		}
	}
	if got := h.ApplyConnectionStrategy("antigravity", threeConns(), strat)[0].ID; got == first {
		t.Fatalf("call 4: expected handover after 3 requests, still %s", got)
	}
}

func TestAccountStrategyRoundRobinRotatesEveryRequest(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetConnectionState("")

	strat := db.ProviderStrategy{AccountStrategy: "round-robin", StickyRoundRobinLimit: 1}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		seen[h.ApplyConnectionStrategy("antigravity", threeConns(), strat)[0].ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("round-robin with limit 1 covered %d accounts, want 3: %v", len(seen), seen)
	}
}

func TestAccountStrategyStickyHoldsForLimit(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetConnectionState("")

	strat := db.ProviderStrategy{AccountStrategy: "sticky", StickyRoundRobinLimit: 2}
	a := h.ApplyConnectionStrategy("antigravity", threeConns(), strat)[0].ID
	b := h.ApplyConnectionStrategy("antigravity", threeConns(), strat)[0].ID
	if a != b {
		t.Fatalf("sticky limit 2 handed over early: %s then %s", a, b)
	}
	if c := h.ApplyConnectionStrategy("antigravity", threeConns(), strat)[0].ID; c == a {
		t.Fatalf("sticky limit 2 never handed over, still %s", c)
	}
}

func TestAccountStrategyRandomKeepsOrdering(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetConnectionState("")

	got := h.ApplyConnectionStrategy("antigravity", threeConns(), db.ProviderStrategy{AccountStrategy: "random"})
	if len(got) != 3 {
		t.Fatalf("random dropped accounts: %d", len(got))
	}
	ids := []string{got[0].ID, got[1].ID, got[2].ID}
	sort.Strings(ids)
	if ids[0] != "conn-1" || ids[1] != "conn-2" || ids[2] != "conn-3" {
		t.Fatalf("random changed the set: %v", ids)
	}
}

func TestAccountStrategyFastestPicksLowestLatency(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetConnectionState("")

	conns := threeConns()
	conns[0].LatencyMs = 900
	conns[1].LatencyMs = 120
	conns[2].LatencyMs = 450
	got := h.ApplyConnectionStrategy("antigravity", conns, db.ProviderStrategy{AccountStrategy: "fastest"})
	if got[0].ID != "conn-2" {
		t.Fatalf("fastest picked %s, want conn-2 (120ms)", got[0].ID)
	}
	if got[1].ID != "conn-3" || got[2].ID != "conn-1" {
		t.Fatalf("fastest order %s,%s,%s want conn-2,conn-3,conn-1", got[0].ID, got[1].ID, got[2].ID)
	}
}

// A never-measured account must be tried before the measured ones, otherwise it
// can never collect a sample and stays unranked forever.
func TestAccountStrategyFastestSamplesUnmeasuredFirst(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetConnectionState("")

	conns := threeConns()
	conns[0].LatencyMs = 100
	conns[2].LatencyMs = 50
	got := h.ApplyConnectionStrategy("antigravity", conns, db.ProviderStrategy{AccountStrategy: "fastest"})
	if got[0].ID != "conn-2" {
		t.Fatalf("unmeasured account not sampled first: got %s", got[0].ID)
	}
}

func TestAccountStrategyFastestTiesKeepPriority(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetConnectionState("")

	conns := threeConns()
	conns[0].LatencyMs = 200
	conns[1].LatencyMs = 200
	conns[2].LatencyMs = 200
	got := h.ApplyConnectionStrategy("antigravity", conns, db.ProviderStrategy{AccountStrategy: "fastest"})
	if got[0].ID != "conn-1" || got[1].ID != "conn-2" || got[2].ID != "conn-3" {
		t.Fatalf("tie-break lost priority order: %s,%s,%s", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestAccountModeResolution(t *testing.T) {
	cases := []struct {
		strat db.ProviderStrategy
		want  string
	}{
		{db.ProviderStrategy{}, ""},
		{db.ProviderStrategy{AccountStrategy: "fill-first"}, "fill-first"},
		{db.ProviderStrategy{AccountStrategy: " Round-Robin "}, "round-robin"},
		{db.ProviderStrategy{AccountStrategy: "sticky"}, "sticky"},
		{db.ProviderStrategy{AccountStrategy: "random"}, "random"},
		{db.ProviderStrategy{AccountStrategy: " Fastest "}, "fastest"},
		{db.ProviderStrategy{AccountStrategy: "least-latency"}, "fastest"},
		{db.ProviderStrategy{AccountStrategy: "bogus"}, ""},
		{db.ProviderStrategy{FallbackStrategy: "round-robin"}, "round-robin"},
		{db.ProviderStrategy{AccountStrategy: "none", FallbackStrategy: "round-robin"}, "fill-first"},
	}
	for _, c := range cases {
		if got := c.strat.AccountMode(); got != c.want {
			t.Errorf("%+v: AccountMode()=%q want %q", c.strat, got, c.want)
		}
	}
}
