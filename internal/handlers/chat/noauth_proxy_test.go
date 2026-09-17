package chat

import (
	"database/sql"
	json "encoding/json/v2"
	"testing"

	"9router/proxy/internal/db"
)

func TestGetBestConnection_NoAuth_WithProxyStrategy(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}

	repo := db.NewRepo(database)

	pool, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "mimo-pool-1",
		ProxyURL: "http://proxy1.example.com:8080",
		Type:     "http",
	})
	if err != nil {
		t.Fatalf("insert pool: %v", err)
	}
	poolID := pool["id"].(string)

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}

	// Save settings with providerStrategies
	settingsJSON := `{
		"providerStrategies": {
			"mimo-free": {
				"proxyPoolId": "` + poolID + `",
				"rotateStrategy": "none"
			}
		}
	}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("insert settings: %v", err)
	}

	h := NewChatHandler(repo)

	conn, connData, err := h.GetBestConnection("mimo-free", "", nil, "")
	if err != nil {
		t.Fatalf("GetBestConnection failed: %v", err)
	}

	if conn == nil || connData == nil {
		t.Fatal("expected virtual connection for no-auth provider")
	}

	if connData.ProxyPoolID != poolID {
		t.Errorf("expected ProxyPoolID %s from settings strategy, got %s", poolID, connData.ProxyPoolID)
	}
}

// seedPool creates an active proxy pool and returns its id.
func seedPool(t *testing.T, repo *db.Repo, name, url string) string {
	t.Helper()
	pool, err := repo.InsertProxyPool(db.ProxyPoolData{Name: name, ProxyURL: url, Type: "http"})
	if err != nil {
		t.Fatalf("insert pool %s: %v", name, err)
	}
	return pool["id"].(string)
}

// setStrategy writes providerStrategies into the settings row, replacing any
// previous value (the server treats this field as full-replace).
func setStrategy(t *testing.T, database *sql.DB, provider string, strat map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"providerStrategies": map[string]any{provider: strat}})
	if err != nil {
		t.Fatalf("marshal strategy: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		string(body),
	); err != nil {
		t.Fatalf("insert settings: %v", err)
	}
}

// A rotate strategy must not be pinned to the static pool: selection comes from
// the active pool set, so a rotation key that would otherwise stay on one exit
// now reaches others. (The static pool may still be drawn — it is a valid exit —
// but it must not be the *only* one, which is what the old code did.)
func TestGetBestConnection_NoAuth_RotatingStrategyNotPinnedToStaticPool(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	staticPool := seedPool(t, repo, "static", "http://static.example.com:8080")
	rotateA := seedPool(t, repo, "rotate-a", "http://a.example.com:8080")
	rotateB := seedPool(t, repo, "rotate-b", "http://b.example.com:8080")

	setStrategy(t, database, "mimo-free", map[string]any{
		"proxyPoolId":    staticPool,
		"rotateStrategy": "round-robin",
	})

	h := NewChatHandler(repo)

	valid := map[string]bool{staticPool: true, rotateA: true, rotateB: true}
	seen := map[string]bool{}
	for i := 0; i < 9; i++ {
		_, connData, err := h.GetBestConnection("mimo-free", "", nil, "")
		if err != nil {
			t.Fatalf("GetBestConnection: %v", err)
		}
		if !valid[connData.ProxyPoolID] {
			t.Fatalf("selected pool %q is not an active pool", connData.ProxyPoolID)
		}
		seen[connData.ProxyPoolID] = true
	}
	// With three eligible pools, round-robin over nine picks must visit more
	// than just the static one.
	if len(seen) < 2 {
		t.Errorf("rotation stayed pinned to a single pool: %v", seen)
	}
	if !seen[rotateA] && !seen[rotateB] {
		t.Errorf("rotation never reached a non-static pool: %v", seen)
	}
}

// targetProxyPoolIds narrows rotation to the listed pools.
func TestGetBestConnection_NoAuth_TargetPoolsNarrowRotation(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	allowed := seedPool(t, repo, "allowed", "http://allowed.example.com:8080")
	excluded := seedPool(t, repo, "excluded", "http://excluded.example.com:8080")

	setStrategy(t, database, "mimo-free", map[string]any{
		"rotateStrategy":     "round-robin",
		"targetProxyPoolIds": []string{allowed},
	})

	h := NewChatHandler(repo)

	for i := 0; i < 5; i++ {
		_, connData, err := h.GetBestConnection("mimo-free", "", nil, "")
		if err != nil {
			t.Fatalf("GetBestConnection: %v", err)
		}
		if connData.ProxyPoolID == excluded {
			t.Fatalf("pool %s was excluded by targetProxyPoolIds but got selected", excluded)
		}
		if connData.ProxyPoolID != allowed {
			t.Fatalf("expected %s, got %q", allowed, connData.ProxyPoolID)
		}
	}
}

// A target list that matches nothing must fall back to the full eligible set
// rather than silently disabling proxying.
func TestGetBestConnection_NoAuth_StaleTargetsFallBackToAllPools(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	live := seedPool(t, repo, "live", "http://live.example.com:8080")

	setStrategy(t, database, "mimo-free", map[string]any{
		"rotateStrategy":     "round-robin",
		"targetProxyPoolIds": []string{"pool-that-no-longer-exists"},
	})

	h := NewChatHandler(repo)

	_, connData, err := h.GetBestConnection("mimo-free", "", nil, "")
	if err != nil {
		t.Fatalf("GetBestConnection: %v", err)
	}
	if connData.ProxyPoolID != live {
		t.Errorf("stale targets should fall back to the live pool %s, got %q", live, connData.ProxyPoolID)
	}
}

// With no pools configured at all, rotation resolves to a direct connection
// instead of erroring the request.
func TestGetBestConnection_NoAuth_RotationWithoutPoolsIsDirect(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	setStrategy(t, database, "mimo-free", map[string]any{"rotateStrategy": "round-robin"})

	h := NewChatHandler(repo)

	conn, connData, err := h.GetBestConnection("mimo-free", "", nil, "")
	if err != nil {
		t.Fatalf("GetBestConnection: %v", err)
	}
	if conn == nil || connData == nil {
		t.Fatal("expected virtual connection")
	}
	if connData.ProxyPoolID != "" {
		t.Errorf("expected direct connection (empty pool id), got %q", connData.ProxyPoolID)
	}
}
