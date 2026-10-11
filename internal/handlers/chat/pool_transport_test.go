package chat

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// A pool whose first entry is dead must still serve the request through a
// healthy entry. Before failover existed, the request that drew the dead URL
// simply failed even though the rest of the pool worked.
func TestPoolTransportFailsOverToLiveProxy(t *testing.T) {
	db.ResetProxyCooloffs()
	var served int
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		// Stand in for a forward proxy: the client asks for an absolute URL and
		// the proxy answers directly, so no upstream is needed.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer live.Close()

	// 127.0.0.1:1 refuses connections, standing in for the dead entry.
	pool := &db.ProxyPool{
		ID:       "test-pool",
		IsActive: true,
		Type:     "http",
		URLs:     []string{"http://127.0.0.1:1", live.URL},
	}

	h := NewChatHandler(nil)
	client := h.poolClient(pool)

	req, err := http.NewRequest(http.MethodPost, "http://upstream.invalid/v1/chat", strings.NewReader(`{"m":1}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed instead of failing over to the live proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the live proxy", resp.StatusCode)
	}
	if served == 0 {
		t.Fatal("the live proxy never received the request")
	}
}

// Every entry in the pool must be reachable over time, not just the first.
func TestPoolTransportRotatesAcrossEntries(t *testing.T) {
	db.ResetProxyCooloffs()
	hits := map[string]int{}
	newProxy := func(name string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[name]++
			_, _ = io.WriteString(w, "ok")
		}))
		// Tag the URL so the handler can tell which entry was used.
		return s
	}
	a, b := newProxy("a"), newProxy("b")
	defer a.Close()
	defer b.Close()

	pool := &db.ProxyPool{ID: "rotate", IsActive: true, Type: "http", URLs: []string{a.URL, b.URL}}
	h := NewChatHandler(nil)
	client := h.poolClient(pool)

	for i := 0; i < 4; i++ {
		resp, err := client.Get("http://upstream.invalid/x")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
	}
	if hits["a"] == 0 || hits["b"] == 0 {
		t.Fatalf("rotation did not use every entry: %v", hits)
	}
}

// A body that cannot be rewound must not be resent: the upstream would receive
// a truncated payload and the caller would silently get a wrong answer.
func TestPoolTransportDoesNotRetryUnrewindableBody(t *testing.T) {
	db.ResetProxyCooloffs()
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the live proxy must not receive a request whose body cannot be rewound")
	}))
	defer live.Close()

	pool := &db.ProxyPool{
		ID: "unrewindable", IsActive: true, Type: "http",
		URLs: []string{"http://127.0.0.1:1", live.URL},
	}
	h := NewChatHandler(nil)
	client := h.poolClient(pool)

	req, err := http.NewRequest(http.MethodPost, "http://upstream.invalid/v1", io.NopCloser(strings.NewReader("x")))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.GetBody = nil // force the unrewindable path
	if _, err := client.Do(req); err == nil {
		t.Fatal("expected failure when the body cannot be rewound for a retry")
	}
}

// A rate-limited proxy answers the request itself, so http.Transport reports a
// successful round trip and the old code handed that 429 back as the upstream's
// answer. The pool must treat it as a refusal and try the next entry.
func TestPoolTransportFailsOverOnRateLimitedProxy(t *testing.T) {
	db.ResetProxyCooloffs()

	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "slow down")
	}))
	defer limited.Close()

	var served int
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		_, _ = io.WriteString(w, "ok")
	}))
	defer live.Close()

	pool := &db.ProxyPool{ID: "ratelimit", IsActive: true, Type: "http",
		URLs: []string{limited.URL, live.URL}}

	h := NewChatHandler(nil)
	client := h.poolClient(pool)

	resp, err := client.Do(mustPost(t, `{"m":1}`))
	if err != nil {
		t.Fatalf("request failed instead of failing over: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the healthy proxy", resp.StatusCode)
	}
	if served == 0 {
		t.Fatal("the healthy proxy never received the request")
	}
}

// A proxy that just refused must not be drawn again on the next request: that
// redraw is exactly the "still routing through the rate-limited proxy" symptom.
func TestPoolTransportSkipsCoolingProxy(t *testing.T) {
	db.ResetProxyCooloffs()

	var limitedHits int
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limitedHits++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer limited.Close()

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer live.Close()

	pool := &db.ProxyPool{ID: "cooloff", IsActive: true, Type: "http",
		URLs: []string{limited.URL, live.URL}}
	h := NewChatHandler(nil)
	client := h.poolClient(pool)

	// First request discovers the limit.
	resp, err := client.Do(mustPost(t, `{"m":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if limitedHits != 1 {
		t.Fatalf("limited proxy hits after first request = %d, want 1", limitedHits)
	}

	// Later requests must not touch it again while it cools.
	for i := 0; i < 5; i++ {
		resp, err := client.Do(mustPost(t, `{"m":1}`))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
	}
	if limitedHits != 1 {
		t.Errorf("limited proxy was reused while cooling: hits = %d, want 1", limitedHits)
	}
}

// exhaustedProxyStatus decides which responses mean "the proxy refused", so it
// must cover the statuses a spent proxy returns and must never include one the
// upstream owns — retrying those would repeat a non-proxy failure.
func TestExhaustedProxyStatus(t *testing.T) {
	for _, code := range []int{429, 407, 502, 503, 504} {
		if !exhaustedProxyStatus(code) {
			t.Errorf("status %d should be treated as a proxy refusal", code)
		}
	}
	for _, code := range []int{200, 201, 400, 401, 403, 404, 422, 500} {
		if exhaustedProxyStatus(code) {
			t.Errorf("status %d belongs to the upstream and must not trigger failover", code)
		}
	}
}

func mustPost(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://upstream.invalid/v1", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}
