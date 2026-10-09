package chat

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"9router/proxy/internal/db"
)

// A pool holding 20 proxies must hand out all 20, one per request, before any
// entry is reused. The cursor is per-pool and monotonic, so this is the
// property to check directly rather than inferring it from a 2-entry test.
func TestPoolTransportRotatesAllTwenty(t *testing.T) {
	const n = 20
	var mu sync.Mutex
	hits := make([]int, n)

	urls := make([]string, 0, n)
	servers := make([]*httptest.Server, 0, n)
	for i := 0; i < n; i++ {
		idx := i
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[idx]++
			mu.Unlock()
			_, _ = io.WriteString(w, "ok")
		}))
		servers = append(servers, s)
		urls = append(urls, s.URL)
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()

	pool := &db.ProxyPool{ID: "twenty", IsActive: true, Type: "http", URLs: urls}
	h := NewChatHandler(nil)
	client := h.poolClient(pool)

	// 20 requests: every entry exactly once.
	for i := 0; i < n; i++ {
		resp, err := client.Get("http://upstream.invalid/x")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
	}

	mu.Lock()
	defer mu.Unlock()
	unused, most := 0, 0
	for i, c := range hits {
		if c == 0 {
			unused++
			t.Logf("entry %d never used", i)
		}
		if c > most {
			most = c
		}
	}
	if unused > 0 {
		t.Errorf("%d of %d entries were never used", unused, n)
	}
	if most != 1 {
		t.Errorf("entries should be used exactly once in %d requests, max was %d", n, most)
	}
}

// With 20 entries and one dead, every request must still succeed: the dead
// entry is skipped by failover rather than failing the request that drew it.
// This is the property that makes a large pool worth having — before failover
// lived here, roughly 1 in 20 requests failed on a pool with one bad proxy.
func TestPoolTransportSkipsDeadEntryAmongTwenty(t *testing.T) {
	const n = 20
	var mu sync.Mutex
	hits := map[string]int{}

	urls := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if i == 7 {
			// A port nothing listens on: dial fails immediately.
			urls = append(urls, "http://127.0.0.1:1")
			continue
		}
		name := "live"
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[name]++
			mu.Unlock()
			_, _ = io.WriteString(w, "ok")
		}))
		defer s.Close()
		urls = append(urls, s.URL)
	}

	pool := &db.ProxyPool{ID: "twenty-dead", IsActive: true, Type: "http", URLs: urls}
	h := NewChatHandler(nil)
	client := h.poolClient(pool)

	// Enough requests to hit the dead entry's position at least once.
	for i := 0; i < 3*n; i++ {
		resp, err := client.Get("http://upstream.invalid/x")
		if err != nil {
			t.Fatalf("request %d failed despite 19 healthy entries: %v", i, err)
		}
		resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["live"] != 3*n {
		t.Errorf("all %d requests should have succeeded via a live entry, got %d", 3*n, hits["live"])
	}
}
