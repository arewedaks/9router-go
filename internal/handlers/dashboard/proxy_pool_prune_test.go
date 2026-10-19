package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Pruning exists because a pasted vendor list is mostly dead: 60 of the 100
// entries that motivated this endpoint refused connections outright. The pool
// must keep the working members and drop the rest, not judge the pool by its
// first entry the way the Test button does.
func TestPruneProxyPoolKeepsOnlyReachableProxies(t *testing.T) {
	h, _, router := setupTestDashboard(t)

	live := startFakeProxy(t, "HTTP/1.1 200 Connection Established\r\n\r\n")
	dead := "http://127.0.0.1:9" // discard port: connection refused

	pool := createPoolForPrune(t, router, []string{dead, live})
	id, _ := pool["id"].(string)
	if id == "" {
		t.Fatalf("pool id missing from create response: %v", pool)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/dashboard/proxy-pools/"+id+"/prune", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	out := decodeJSON(t, rec.Body.Bytes())
	if out["removed"] != float64(1) || out["kept"] != float64(1) {
		t.Fatalf("removed/kept = %v/%v, want 1/1 (dead entry should go)", out["removed"], out["kept"])
	}

	urls, _ := out["urls"].([]any)
	if len(urls) != 1 || urls[0] != live {
		t.Fatalf("surviving urls = %v, want just %s", urls, live)
	}

	// The prune must be persisted, not just reported: the next request reads
	// the stored pool.
	stored, err := h.repo.GetProxyPoolDetail(id)
	if err != nil {
		t.Fatalf("reload pool: %v", err)
	}
	got, _ := stored["urls"].([]any)
	if len(got) != 1 {
		t.Fatalf("stored urls = %v, want the dead entry removed", got)
	}
}

// Emptying a pool whose members are all dead would make it resolve to no proxy,
// which silently routes from the host IP. Refuse instead, and say so.
func TestPruneProxyPoolRefusesToEmptyAnAllDeadPool(t *testing.T) {
	h, _, router := setupTestDashboard(t)

	pool := createPoolForPrune(t, router, []string{"http://127.0.0.1:9"})
	id, _ := pool["id"].(string)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/dashboard/proxy-pools/"+id+"/prune", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	out := decodeJSON(t, rec.Body.Bytes())
	if out["ok"] != false {
		t.Fatalf("ok = %v, want false for an all-dead pool", out["ok"])
	}
	if out["removed"] != float64(0) {
		t.Fatalf("removed = %v, want 0: the pool must be left intact", out["removed"])
	}

	// A one-entry pool is stored as proxyUrl, not as a urls array (see
	// ProxyPoolDataFromList), so check the entry survived rather than the shape.
	stored, err := h.repo.GetProxyPoolDetail(id)
	if err != nil {
		t.Fatalf("reload pool: %v", err)
	}
	if stored["proxyUrl"] != "http://127.0.0.1:9" {
		t.Fatalf("stored proxyUrl = %v, want the original entry kept", stored["proxyUrl"])
	}
}

func createPoolForPrune(t *testing.T, router http.Handler, urls []string) map[string]any {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"name": "prune pool", "urls": urls})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/dashboard/proxy-pools", bytes.NewReader(raw)))
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create pool: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	created := decodeJSON(t, rec.Body.Bytes())
	if pool, ok := created["pool"].(map[string]any); ok {
		return pool
	}
	return created
}

func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}
