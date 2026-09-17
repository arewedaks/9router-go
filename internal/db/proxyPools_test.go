package db

import (
	"database/sql"
	json "encoding/json/v2"
	"sync"
	"testing"
)

// TestInsertProxyPool_DataShape verifies InsertProxyPool writes the proxyPools
// row with the same data JSON shape and values the Next.js dashboard stores, so
// the shared DB stays compatible.
func TestInsertProxyPool_DataShape(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}

	repo := NewRepo(db)
	pool, err := repo.InsertProxyPool(ProxyPoolData{
		Name: "relay-x", ProxyURL: "https://relay-x.example.com", NoProxy: "", Type: "vercel", StrictProxy: false,
	})
	if err != nil {
		t.Fatalf("InsertProxyPool failed: %v", err)
	}
	if pool["id"].(string) == "" {
		t.Fatal("expected non-empty generated id")
	}
	if pool["isActive"] != true {
		t.Errorf("expected isActive=true, got %v", pool["isActive"])
	}

	var row struct {
		IsActive   int
		TestStatus string
		Data       string
		CreatedAt  string
		UpdatedAt  string
	}
	err = db.QueryRow(`SELECT isActive, testStatus, data, createdAt, updatedAt FROM proxyPools WHERE id = ?`, pool["id"]).
		Scan(&row.IsActive, &row.TestStatus, &row.Data, &row.CreatedAt, &row.UpdatedAt)
	if err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if row.IsActive != 1 {
		t.Errorf("expected isActive column 1, got %d", row.IsActive)
	}
	if row.TestStatus != "unknown" {
		t.Errorf("expected testStatus 'unknown', got %q", row.TestStatus)
	}
	if row.CreatedAt != row.UpdatedAt {
		t.Errorf("expected createdAt == updatedAt, got %q vs %q", row.CreatedAt, row.UpdatedAt)
	}

	// Parse the stored data JSON and assert it matches Next's createProxyPool shape.
	var data map[string]any
	if err := json.Unmarshal([]byte(row.Data), &data); err != nil {
		t.Fatalf("parse stored data: %v", err)
	}
	want := map[string]any{
		"name":         "relay-x",
		"proxyUrl":     "https://relay-x.example.com",
		"noProxy":      "",
		"type":         "vercel",
		"strictProxy":  false,
		"lastTestedAt": nil,
		"lastError":    nil,
	}
	if len(data) != len(want) {
		t.Fatalf("expected %d keys in data, got %d: %v", len(want), len(data), data)
	}
	for k, v := range want {
		if got, ok := data[k]; !ok || got != v {
			t.Errorf("data[%q] = %v (present=%v), want %v", k, got, ok, v)
		}
	}
}

func TestGetProxyPool_SingleProxyUrl_AndMetadata(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		data TEXT,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		createdAt TEXT,
		updatedAt TEXT
	);`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	repo := NewRepo(db)

	// Single proxyUrl string (Next.js format)
	poolData := `{"name":"relay-pool","proxyUrl":"https://relay.example.com","type":"vercel","noProxy":"localhost,*.internal","strictProxy":true}`
	if _, err := db.Exec(`INSERT INTO proxyPools (id, data, isActive) VALUES (?, ?, ?)`, "pool-single", poolData, 1); err != nil {
		t.Fatalf("insert: %v", err)
	}

	pool, err := repo.GetProxyPool("pool-single")
	if err != nil {
		t.Fatalf("GetProxyPool failed: %v", err)
	}
	if pool == nil {
		t.Fatal("expected pool, got nil")
	}
	if len(pool.URLs) != 1 || pool.URLs[0] != "https://relay.example.com" {
		t.Errorf("expected URLs [https://relay.example.com], got %v", pool.URLs)
	}
	if pool.Type != "vercel" {
		t.Errorf("expected Type vercel, got %s", pool.Type)
	}
	if pool.NoProxy != "localhost,*.internal" {
		t.Errorf("expected NoProxy localhost,*.internal, got %s", pool.NoProxy)
	}
	if !pool.StrictProxy {
		t.Errorf("expected StrictProxy true, got %v", pool.StrictProxy)
	}
	if next := pool.NextURL(); next != "https://relay.example.com" {
		t.Errorf("expected NextURL https://relay.example.com, got %s", next)
	}
}

// ensureProxyPoolsTable mirrors the schema the deploy paths create, so these
// tests exercise ListProxyPools/EligibleProxyPoolIDs without a full migration.
func ensureProxyPoolsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
}

func TestListProxyPools_ReportsEligibility(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ensureProxyPoolsTable(t, db)
	repo := NewRepo(db)

	withURL, err := repo.InsertProxyPool(ProxyPoolData{Name: "has-url", ProxyURL: "http://p.example.com:8080", Type: "http"})
	if err != nil {
		t.Fatalf("insert with url: %v", err)
	}
	noURL, err := repo.InsertProxyPool(ProxyPoolData{Name: "no-url", Type: "http"})
	if err != nil {
		t.Fatalf("insert without url: %v", err)
	}

	all, err := repo.ListProxyPools(false)
	if err != nil {
		t.Fatalf("ListProxyPools(false): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 pools, got %d", len(all))
	}

	eligible, err := repo.EligibleProxyPoolIDs()
	if err != nil {
		t.Fatalf("EligibleProxyPoolIDs: %v", err)
	}
	if len(eligible) != 1 || eligible[0] != withURL["id"].(string) {
		t.Errorf("expected only the URL-bearing pool %v to be eligible, got %v", withURL["id"], eligible)
	}
	if noURL["id"].(string) == eligible[0] {
		t.Error("pool without a URL must not be eligible")
	}
}

// An inactive pool must not be offered for rotation.
func TestEligibleProxyPoolIDs_SkipsInactive(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ensureProxyPoolsTable(t, db)
	repo := NewRepo(db)

	pool, err := repo.InsertProxyPool(ProxyPoolData{Name: "off", ProxyURL: "http://off.example.com:8080", Type: "http"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Exec(`UPDATE proxyPools SET isActive = 0 WHERE id = ?`, pool["id"]); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	// The pool cache would otherwise serve the stale active row.
	proxyPoolCache = sync.Map{}

	eligible, err := repo.EligibleProxyPoolIDs()
	if err != nil {
		t.Fatalf("EligibleProxyPoolIDs: %v", err)
	}
	if len(eligible) != 0 {
		t.Errorf("inactive pool must not be eligible, got %v", eligible)
	}
}

func TestPickProxyPoolID_Strategies(t *testing.T) {
	eligible := []string{"a", "b", "c"}

	// Unknown/empty strategies take the first candidate.
	if got := PickProxyPoolID(eligible, nil, "", "p"); got != "a" {
		t.Errorf("empty strategy: expected a, got %q", got)
	}
	if got := PickProxyPoolID(eligible, nil, "fill-first", "p"); got != "a" {
		t.Errorf("fill-first: expected a, got %q", got)
	}

	// round-robin must visit every pool before repeating.
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		seen[PickProxyPoolID(eligible, nil, "round-robin", "p")]++
	}
	if len(seen) != 3 {
		t.Errorf("round-robin should reach all 3 pools, reached %v", seen)
	}
	for id, n := range seen {
		if n != 2 {
			t.Errorf("round-robin should cycle evenly, pool %s picked %d times", id, n)
		}
	}

	// random stays within the candidate set.
	for i := 0; i < 20; i++ {
		got := PickProxyPoolID(eligible, nil, "random", "p")
		if got != "a" && got != "b" && got != "c" {
			t.Fatalf("random returned out-of-set pool %q", got)
		}
	}

	// targets narrow the set.
	if got := PickProxyPoolID(eligible, []string{"c"}, "round-robin", "p"); got != "c" {
		t.Errorf("targets should narrow to c, got %q", got)
	}

	// No eligible pools means direct connection.
	if got := PickProxyPoolID(nil, nil, "round-robin", "p"); got != "" {
		t.Errorf("no eligible pools should give empty, got %q", got)
	}
}

// A target list matching nothing falls back to the full eligible set so a stale
// subset never disables proxying entirely.
func TestPickProxyPoolID_StaleTargetsFallBack(t *testing.T) {
	got := PickProxyPoolID([]string{"live"}, []string{"gone"}, "round-robin", "p")
	if got != "live" {
		t.Errorf("stale targets should fall back to live, got %q", got)
	}
}
