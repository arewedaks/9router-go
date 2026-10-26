package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

func TestApplyConnectionStrategy_RoundRobin(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetConnectionState("")

	c1 := &models.ProviderConnection{ID: "conn-1"}
	c2 := &models.ProviderConnection{ID: "conn-2"}
	c3 := &models.ProviderConnection{ID: "conn-3"}
	conns := []*models.ProviderConnection{c1, c2, c3}

	// Account round-robin rotates every request: sticky limit 1.
	strat := db.ProviderStrategy{RotateStrategy: "round-robin", AccountStrategy: "round-robin", StickyRoundRobinLimit: 1}

	// 1st request -> c1
	r1 := h.ApplyConnectionStrategy("antigravity", conns, strat)
	if r1[0].ID != "conn-1" {
		t.Fatalf("call 1: expected conn-1, got %s", r1[0].ID)
	}

	// 2nd request -> c2
	r2 := h.ApplyConnectionStrategy("antigravity", conns, strat)
	if r2[0].ID != "conn-2" {
		t.Fatalf("call 2: expected conn-2, got %s", r2[0].ID)
	}

	// 3rd request -> c3
	r3 := h.ApplyConnectionStrategy("antigravity", conns, strat)
	if r3[0].ID != "conn-3" {
		t.Fatalf("call 3: expected conn-3, got %s", r3[0].ID)
	}

	// 4th request -> c1 (wrapped)
	r4 := h.ApplyConnectionStrategy("antigravity", conns, strat)
	if r4[0].ID != "conn-1" {
		t.Fatalf("call 4: expected conn-1, got %s", r4[0].ID)
	}
}

func TestApplyConnectionStrategy_Sticky(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetConnectionState("")

	c1 := &models.ProviderConnection{ID: "conn-1"}
	c2 := &models.ProviderConnection{ID: "conn-2"}
	conns := []*models.ProviderConnection{c1, c2}

	strat := db.ProviderStrategy{
		RotateStrategy:  "sticky",
		StickyLimit:     3,
		AccountStrategy: "sticky",
	}

	// First 3 calls should stay on conn-1
	for i := range 3 {
		r := h.ApplyConnectionStrategy("antigravity", conns, strat)
		if r[0].ID != "conn-1" {
			t.Fatalf("call %d: expected conn-1 (sticky), got %s", i+1, r[0].ID)
		}
	}

	// 4th call rotates to conn-2
	r4 := h.ApplyConnectionStrategy("antigravity", conns, strat)
	if r4[0].ID != "conn-2" {
		t.Fatalf("call 4: expected conn-2 after sticky limit, got %s", r4[0].ID)
	}

	// Next 2 calls also stay on conn-2 (total 3 on conn-2)
	for i := range 2 {
		r := h.ApplyConnectionStrategy("antigravity", conns, strat)
		if r[0].ID != "conn-2" {
			t.Fatalf("call %d on conn-2: expected conn-2, got %s", i+2, r[0].ID)
		}
	}

	// Rotates back to conn-1
	r7 := h.ApplyConnectionStrategy("antigravity", conns, strat)
	if r7[0].ID != "conn-1" {
		t.Fatalf("call 7: expected conn-1, got %s", r7[0].ID)
	}
}

func TestGetBestConnection_WithRoundRobinStrategy(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")

	// Seed 3 active connections for antigravity
	seedConnDB(t, database, "antigravity", "conn-ag-1", "tok-1", "https://mock.example.com/1")
	seedConnDB(t, database, "antigravity", "conn-ag-2", "tok-2", "https://mock.example.com/2")
	seedConnDB(t, database, "antigravity", "conn-ag-3", "tok-3", "https://mock.example.com/3")

	// Set round-robin strategy for antigravity
	if err := repo.SetProviderStrategy("antigravity", db.ProviderStrategy{
		RotateStrategy:        "round-robin",
		AccountStrategy:       "round-robin",
		StickyRoundRobinLimit: 1,
	}); err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}

	// Verify rotation through GetBestConnection
	conn1, _, err := h.GetBestConnection("antigravity", "", nil, "")
	if err != nil || conn1 == nil || conn1.ID != "conn-ag-1" {
		t.Fatalf("step 1: expected conn-ag-1, got %v (err=%v)", conn1, err)
	}

	conn2, _, err := h.GetBestConnection("antigravity", "", nil, "")
	if err != nil || conn2 == nil || conn2.ID != "conn-ag-2" {
		t.Fatalf("step 2: expected conn-ag-2, got %v (err=%v)", conn2, err)
	}

	conn3, _, err := h.GetBestConnection("antigravity", "", nil, "")
	if err != nil || conn3 == nil || conn3.ID != "conn-ag-3" {
		t.Fatalf("step 3: expected conn-ag-3, got %v (err=%v)", conn3, err)
	}

	conn4, _, err := h.GetBestConnection("antigravity", "", nil, "")
	if err != nil || conn4 == nil || conn4.ID != "conn-ag-1" {
		t.Fatalf("step 4: expected conn-ag-1, got %v (err=%v)", conn4, err)
	}
}

func TestHandleAccountFallback_RotatesUpstreamRequests(t *testing.T) {
	var hits1, hits2 atomic.Int32

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits1.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"srv1"}}]}`))
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits2.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"srv2"}}]}`))
	}))
	defer srv2.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Clear pre-seeded deepseek connection
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}

	seedConnDB(t, database, "deepseek", "conn-ds-1", "sk-1", srv1.URL)
	seedConnDB(t, database, "deepseek", "conn-ds-2", "sk-2", srv2.URL)
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.ResetConnectionState("")

	// Enable round-robin strategy on deepseek
	if err := repo.SetProviderStrategy("deepseek", db.ProviderStrategy{
		RotateStrategy:        "round-robin",
		AccountStrategy:       "round-robin",
		StickyRoundRobinLimit: 1,
	}); err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}

	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)

	// 1st request -> srv1
	rec1 := httptest.NewRecorder()
	if err := h.handleAccountFallback(context.Background(), rec1, "deepseek", "deepseek-chat", "", body, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("request 1: %v", err)
	}
	if hits1.Load() != 1 || hits2.Load() != 0 {
		t.Errorf("after req 1: hits1=%d, hits2=%d (want 1, 0)", hits1.Load(), hits2.Load())
	}

	// 2nd request -> srv2 (rotated!)
	rec2 := httptest.NewRecorder()
	if err := h.handleAccountFallback(context.Background(), rec2, "deepseek", "deepseek-chat", "", body, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("request 2: %v", err)
	}
	if hits1.Load() != 1 || hits2.Load() != 1 {
		t.Errorf("after req 2: hits1=%d, hits2=%d (want 1, 1)", hits1.Load(), hits2.Load())
	}
}
