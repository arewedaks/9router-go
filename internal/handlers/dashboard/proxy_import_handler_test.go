package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A proxy list imported for a keyless provider must both land in the pool and
// be attached to the provider. They are useless apart: a pool nobody targets
// routes nothing, and a strategy pointing at a missing pool silently falls back
// to the host's own IP — the exact leak a pool exists to prevent.
func TestCreateProxyPoolImportsListAndAssignsProvider(t *testing.T) {
	h, repo, _ := setupTestDashboard(t)

	// Windows line endings on purpose: this is a Telegram-downloaded export.
	list := "user1:pass1@1.2.3.4:3129\r\nuser2:pass2@5.6.7.8:8080\r\n# note\r\nbad-line\r\n"

	body, _ := json.Marshal(map[string]any{
		"name":     "imported",
		"list":     list,
		"provider": "opencode",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/proxy-pools", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleCreateProxyPool(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Pool struct {
			ID string `json:"id"`
		} `json:"pool"`
		Imported int    `json:"imported"`
		Skipped  int    `json:"skipped"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if resp.Imported != 2 {
		t.Errorf("imported = %d, want 2 (body %s)", resp.Imported, rec.Body.String())
	}
	if resp.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the bad line)", resp.Skipped)
	}
	if resp.Pool.ID == "" {
		t.Fatal("no pool id returned")
	}

	// The pool must carry both proxies for round-robin, with credentials intact.
	pool, err := repo.GetProxyPool(resp.Pool.ID)
	if err != nil {
		t.Fatalf("read pool: %v", err)
	}
	if len(pool.URLs) != 2 {
		t.Fatalf("pool has %d urls, want 2: %v", len(pool.URLs), pool.URLs)
	}
	for _, u := range pool.URLs {
		if strings.Contains(u, "\r") {
			t.Errorf("url %q kept a carriage return", u)
		}
		if !strings.Contains(u, "@") {
			t.Errorf("url %q lost its credentials", u)
		}
	}
	if got := pool.NextURL(); got == "" {
		t.Error("round-robin returned an empty URL")
	}

	// And the provider must now point at it.
	settings, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	strat, ok := settings.ProviderStrategies["opencode"]
	if !ok {
		t.Fatal("opencode has no strategy after import")
	}
	if strat.ProxyPoolID != resp.Pool.ID {
		t.Errorf("strategy pool = %q, want %q", strat.ProxyPoolID, resp.Pool.ID)
	}
}

func TestCreateProxyPoolRejectsEmptyAndUnusableLists(t *testing.T) {
	h, _, _ := setupTestDashboard(t)

	for name, list := range map[string]string{
		"empty":    "",
		"blank":    "   \n\n  \n",
		"garbage":  "not-a-proxy\nstill-not-one\n",
		"comments": "# only a comment\n",
	} {
		body, _ := json.Marshal(map[string]any{"list": list})
		req := httptest.NewRequest(http.MethodPost, "/api/dashboard/proxy-pools", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.HandleCreateProxyPool(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (body %s)", name, rec.Code, rec.Body.String())
		}
	}
}

func TestCreateProxyPoolWithoutProviderCreatesUnattachedPool(t *testing.T) {
	h, repo, _ := setupTestDashboard(t)

	body, _ := json.Marshal(map[string]any{"list": "1.2.3.4:8080"})
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/proxy-pools", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleCreateProxyPool(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	pools, err := repo.ListProxyPools(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 || pools[0].URLCount != 1 {
		t.Fatalf("pools = %+v, want one with a url", pools)
	}
	// A single entry must still be served by GetProxyPool via ProxyURL.
	pool, err := repo.GetProxyPool(pools[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if pool.NextURL() != "http://1.2.3.4:8080" {
		t.Errorf("NextURL = %q", pool.NextURL())
	}
}
