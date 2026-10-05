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
