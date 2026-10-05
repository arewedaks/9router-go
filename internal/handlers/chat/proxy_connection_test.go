package chat

import (
	"net/http"
	"testing"

	"9router/proxy/internal/db"
)

func TestGetClientForConnection_ProxyPool(t *testing.T) {
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
		Name:     "sg-proxy",
		ProxyURL: "http://user:pass@proxy.example.com:8080",
		Type:     "http",
	})
	if err != nil {
		t.Fatalf("failed to insert proxy pool: %v", err)
	}

	poolID := pool["id"].(string)

	h := &ChatHandler{
		Client: &http.Client{},
		Repo:   repo,
	}

	connData := &ConnectionData{
		ProxyPoolID: poolID,
	}

	client := h.GetClientForConnection(connData)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client == h.Client {
		t.Fatal("expected new client with custom proxy transport, got default client")
	}

	transport, ok := client.Transport.(*poolTransport)
	if !ok || transport == nil {
		t.Fatal("expected *poolTransport so a dead entry can fail over")
	}

	// The proxy is chosen per attempt, so assert the pool's URL reaches the
	// transport rather than a URL captured when the client was built.
	inner, err := transport.transportFor("http://user:pass@proxy.example.com:8080")
	if err != nil {
		t.Fatalf("transportFor: %v", err)
	}
	req, _ := http.NewRequest("GET", "https://api.openai.com/v1/models", nil)
	proxyURL, err := inner.Proxy(req)
	if err != nil {
		t.Fatalf("proxy resolve error: %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://user:pass@proxy.example.com:8080" {
		t.Errorf("expected proxy URL http://user:pass@proxy.example.com:8080, got %v", proxyURL)
	}
}

// TestGetClientForConnection_ForwardProxyTypes pins the pool types that must
// produce a proxied transport. "https" is the one that regressed: the editor
// offers it next to "http", but the client builder classed it as an Edge Relay
// and returned the direct client, so a pool the dashboard showed as attached
// silently carried no traffic. "socks5" and the relay types are covered here
// too so the branch stays honest.
func TestGetClientForConnection_ForwardProxyTypes(t *testing.T) {
	for _, tc := range []struct {
		poolType  string
		proxyURL  string
		wantProxy bool
	}{
		{"http", "http://user:pass@proxy.example.com:8080", true},
		{"https", "https://user:pass@proxy.example.com:443", true},
		{"", "http://user:pass@proxy.example.com:8080", true},
		{"socks5", "socks5://user:pass@proxy.example.com:1080", true},
		// Edge Relays rewrite the URL and add x-relay headers per request, so
		// they deliberately ride the standard client.
		{"vercel", "http://user:pass@proxy.example.com:8080", false},
		{"cloudflare", "http://user:pass@proxy.example.com:8080", false},
		{"deno", "http://user:pass@proxy.example.com:8080", false},
	} {
		t.Run(tc.poolType+"/"+tc.proxyURL, func(t *testing.T) {
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
				Name:     "pool-" + tc.poolType,
				ProxyURL: tc.proxyURL,
				Type:     tc.poolType,
			})
			if err != nil {
				t.Fatalf("failed to insert proxy pool: %v", err)
			}

			h := &ChatHandler{Client: &http.Client{}, Repo: repo}
			client := h.GetClientForConnection(&ConnectionData{ProxyPoolID: pool["id"].(string)})
			if client == nil {
				t.Fatal("expected non-nil client")
			}

			proxied := client != h.Client
			if proxied != tc.wantProxy {
				t.Fatalf("pool type %q: proxied=%v, want %v (direct egress leaks the host IP)", tc.poolType, proxied, tc.wantProxy)
			}
		})
	}
}
