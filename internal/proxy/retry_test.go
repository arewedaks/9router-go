package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/proxy"
)

// A POST is not idempotent, so net/http never replays it — but the failure it
// refuses to retry here is not an ambiguous one. The connection was closed
// while idle in the keep-alive pool, so the request never reached the server;
// retrying on a fresh connection is safe and is what the upstream expects.
//
// This reproduces the codebuddy 502: TencentEdgeOne drops idle HTTP/2
// connections, and the next POST on the stale one fails with `unexpected EOF`.
func TestDoRequest_RetriesStaleKeepAliveConnection(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			// Simulate the idle-close race: the server closes before reading.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("test server does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			conn.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := &http.Client{}
	resp, err := proxy.DoRequest(context.Background(), client, "POST", srv.URL, nil, []byte(`{"a":1}`))
	if err != nil {
		t.Fatalf("DoRequest returned error on a retryable stale connection: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := attempts.Load(); got < 2 {
		t.Errorf("server saw %d attempts, want >= 2 (request was not retried)", got)
	}
}

// A genuine upstream failure must still surface as an error rather than being
// masked by the retry loop.
func TestDoRequest_DoesNotRetryPermanentFailure(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	_, err := proxy.DoRequest(context.Background(), &http.Client{}, "POST", srv.URL, nil, []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error for a 401 upstream response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q should mention the 401 status", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("server saw %d attempts, want exactly 1 (4xx must not be retried)", got)
	}
}
