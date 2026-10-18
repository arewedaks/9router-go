package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The models-import fetch must not go out with Go's default User-Agent:
// Cloudflare-fronted catalogues (Nous among them) answer 403 with an
// "Attention Required!" page, which surfaces in the dashboard as the import
// button failing for no visible reason.
func TestModelRequestSendsANonGoUserAgent(t *testing.T) {
	h := &Handler{}
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()

	res, err := h.doModelRequest(&http.Client{Timeout: 5 * time.Second}, "nous", srv.URL, http.MethodGet,
		map[string]string{"Accept": "application/json"}, nil)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("fetch error: %s", res.Error)
	}

	if got == "" || got == "Go-http-client/1.1" {
		t.Fatalf("User-Agent = %q; Cloudflare would answer 403", got)
	}
}

// A fetcher that supplies its own UA keeps it.
func TestModelRequestKeepsExplicitUserAgent(t *testing.T) {
	h := &Handler{}
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	_, err := h.doModelRequest(&http.Client{Timeout: 5 * time.Second}, "x", srv.URL, http.MethodGet,
		map[string]string{"User-Agent": "custom/9"}, nil)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got != "custom/9" {
		t.Fatalf("User-Agent = %q, want custom/9", got)
	}
}
