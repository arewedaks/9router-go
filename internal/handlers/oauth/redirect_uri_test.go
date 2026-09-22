package oauth

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The redirect URI must carry the port the server is actually on. A hardcoded
// port sends the browser to a dead one whenever the gateway runs elsewhere, and
// the authorization code is lost with it.
func TestLoopbackRedirectURI_UsesRequestPort(t *testing.T) {
	cases := []struct {
		name string
		host string
		want string
	}{
		{"default port", "localhost:20128", "http://localhost:20128/oauth/antigravity/callback"},
		{"custom port", "localhost:20129", "http://localhost:20129/oauth/antigravity/callback"},
		{"loopback ip", "127.0.0.1:8080", "http://127.0.0.1:8080/oauth/antigravity/callback"},
		{"proxied public port", "gateway.example.com:443", "http://gateway.example.com:443/oauth/antigravity/callback"},
		// No port means a proxy on the default port or a stripped Host header;
		// guessing would be worse than the documented fallback.
		{"no port falls back", "localhost", "http://localhost:20128/oauth/antigravity/callback"},
		{"empty host falls back", "", "http://localhost:20128/oauth/antigravity/callback"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/oauth/antigravity/authorize", nil)
			r.Host = c.host
			if got := loopbackRedirectURI(r, "/oauth/antigravity/callback"); got != c.want {
				t.Errorf("loopbackRedirectURI(host=%q) = %q, want %q", c.host, got, c.want)
			}
		})
	}
}

// An IPv6 zone index is not valid in a URL and must be stripped.
func TestLoopbackRedirectURI_StripsIPv6Zone(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "[::1%25eth0]:20129"
	got := loopbackRedirectURI(r, "/cb")
	if got != "http://[::1]:20129/cb" {
		t.Errorf("got %q, want the zone stripped", got)
	}
}

// A nil request must not panic; it falls back like an unusable Host.
func TestLoopbackRedirectURI_NilRequest(t *testing.T) {
	if got := loopbackRedirectURI(nil, "/cb"); got != "http://localhost:20128/cb" {
		t.Errorf("got %q, want the fallback", got)
	}
}

// The fallback port must stay in step with the documented default, since a
// silent drift here reintroduces exactly the bug this helper exists to fix.
func TestFallbackRedirectURI_MatchesDefaultPort(t *testing.T) {
	if got := fallbackRedirectURI("/cb"); got != "http://localhost:20128/cb" {
		t.Errorf("fallback = %q, want the 20128 default", got)
	}
}

// The callback page must carry the back link for the provider it belongs to, and
// must not be cacheable: it names the account and can echo upstream error text.
func TestWriteCallbackPage(t *testing.T) {
	cases := []struct {
		status   int
		provider string
		wantLink string
		wantMark string
	}{
		{200, "antigravity", "/dashboard#provider/antigravity", "&#10003;"},
		{400, "gemini-cli", "/dashboard#provider/gemini-cli", "&#10007;"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeCallbackPage(rec, c.status, "Title", "Detail", c.provider)

		if rec.Code != c.status {
			t.Errorf("status = %d, want %d", rec.Code, c.status)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `href="`+c.wantLink+`"`) {
			t.Errorf("body missing back link %q: %s", c.wantLink, body)
		}
		if !strings.Contains(body, c.wantMark) {
			t.Errorf("body missing status mark %q", c.wantMark)
		}
		// The detail must be escaped, or an upstream error containing markup
		// would be rendered as HTML in the operator's browser.
		if !strings.Contains(body, "Detail") {
			t.Error("detail text missing")
		}
	}
}

// Escaping is what keeps an upstream error message from injecting markup.
func TestWriteCallbackPage_EscapesDetail(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCallbackPage(rec, 502, "Failed", `<script>alert(1)</script>`, "antigravity")
	body := rec.Body.String()
	if strings.Contains(body, "<script>") {
		t.Errorf("unescaped markup reached the page: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected the detail to be escaped: %s", body)
	}
}
