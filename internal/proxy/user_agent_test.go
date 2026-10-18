package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Cloudflare-fronted inference hosts answer 403 to Go's default
// "Go-http-client/1.1" User-Agent. A missing override is invisible in unit
// tests until the operator clicks something against a real host, so pin it.
func TestDoRequestSendsANonGoUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	resp, err := DoRequest(context.Background(), srv.Client(), "POST", srv.URL, nil, []byte(`{}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if got == "" {
		t.Fatal("no User-Agent was sent; Cloudflare would answer 403")
	}
	if got == "Go-http-client/1.1" {
		t.Fatalf("default Go User-Agent was sent (%q); Cloudflare answers 403 to it", got)
	}
}

// A provider that sets its own fingerprint (opencode, grok-cli, antigravity)
// must not be overwritten by the fallback.
func TestDoRequestKeepsCallerUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := DoRequest(context.Background(), srv.Client(), "POST", srv.URL,
		map[string]string{"User-Agent": "opencode/1.17.0"}, []byte(`{}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if got != "opencode/1.17.0" {
		t.Fatalf("User-Agent = %q, want the caller's value", got)
	}
}
