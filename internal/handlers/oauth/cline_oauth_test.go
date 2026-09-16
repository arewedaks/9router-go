package oauth

import (
	"encoding/base64"
	json "encoding/json/v2"
	"net/url"
	"strings"
	"testing"
)

// TestClineAuthorizeURLShape pins the query contract Cline's authorize endpoint
// actually honours. It is not a standard OAuth2 URL builder: `client_type` is
// required (it selects the extension/IDE token audience) and the redirect is
// carried as `callback_url` — the endpoint 302s to WorkOS using Cline's *own*
// callback, then bounces to ours.
func TestClineAuthorizeURLShape(t *testing.T) {
	if got := ghTestHost(clineAuthorizeURL); got != "api.cline.bot" {
		t.Errorf("authorize host = %q, want api.cline.bot", got)
	}
	if got := ghTestHost(clineTokenURL); got != "api.cline.bot" {
		t.Errorf("token host = %q, want api.cline.bot", got)
	}

	q := url.Values{
		"client_type":  {"extension"},
		"callback_url": {clineDefaultRedirectURI},
		"redirect_uri": {clineDefaultRedirectURI},
	}
	full := clineAuthorizeURL + "?" + q.Encode()
	u, err := url.Parse(full)
	if err != nil {
		t.Fatalf("authorize URL does not parse: %v", err)
	}
	got := u.Query()
	if got.Get("client_type") != "extension" {
		t.Errorf("client_type = %q, want extension", got.Get("client_type"))
	}
	if got.Get("callback_url") == "" {
		t.Error("callback_url missing; Cline would have nowhere to deliver the tokens")
	}
}

func TestClineDefaultRedirectIsLoopback(t *testing.T) {
	// Cline only accepts a loopback callback_url. A non-loopback value is silently
	// ignored upstream, which would strand the flow with no redirect at all.
	if !strings.HasPrefix(clineDefaultRedirectURI, "http://localhost") {
		t.Errorf("default redirect = %q, want an http://localhost… loopback URI", clineDefaultRedirectURI)
	}
}

// TestDecodeClineEmbeddedToken covers the normal Cline callback: the tokens are
// base64-encoded JSON carried inside the `code` value, so there is no network
// round-trip and no separate exchange step.
func TestDecodeClineEmbeddedToken(t *testing.T) {
	payload := map[string]any{
		"accessToken":  "workos-access-token",
		"refreshToken": "refresh-token",
		"expiresAt":    "2030-01-02T03:04:05Z",
		"email":        "dev@example.com",
		"firstName":    "Dev",
		"lastName":     "Example",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)

	got := decodeClineEmbeddedToken(encoded)
	if got == nil {
		t.Fatal("decodeClineEmbeddedToken returned nil for a valid blob")
	}
	if got.AccessToken != "workos-access-token" {
		t.Errorf("access token = %q", got.AccessToken)
	}
	if got.RefreshToken != "refresh-token" {
		t.Errorf("refresh token = %q", got.RefreshToken)
	}
	if got.Email != "dev@example.com" {
		t.Errorf("email = %q", got.Email)
	}
	if got.ExpiresAt != "2030-01-02T03:04:05Z" {
		t.Errorf("expiresAt = %q", got.ExpiresAt)
	}
}

// TestDecodeClineEmbeddedTokenPaddingAndSuffix mirrors the reference decoder,
// which tolerates stripped base64 padding and trailing bytes after the JSON
// object. Real Cline callbacks have been observed with both quirks.
func TestDecodeClineEmbeddedTokenPaddingAndSuffix(t *testing.T) {
	raw := []byte(`{"accessToken":"tok","refreshToken":"r"}`)
	encoded := base64.StdEncoding.EncodeToString(raw)
	encoded = strings.TrimRight(encoded, "=") // strip padding

	if got := decodeClineEmbeddedToken(encoded); got == nil || got.AccessToken != "tok" {
		t.Fatalf("unpadded blob did not decode, got %+v", got)
	}

	withSuffix := base64.StdEncoding.EncodeToString(append(raw, []byte("trailing")...))
	if got := decodeClineEmbeddedToken(withSuffix); got == nil || got.AccessToken != "tok" {
		t.Fatalf("blob with trailing bytes did not decode, got %+v", got)
	}
}

// TestDecodeClineEmbeddedTokenRejectsNonBlob guards the fallback path: anything
// that is not an embedded token blob must return nil so the caller tries the
// documented token endpoint instead of erroring out.
func TestDecodeClineEmbeddedTokenRejectsNonBlob(t *testing.T) {
	for _, in := range []string{
		"",
		"not-base64!!",
		base64.StdEncoding.EncodeToString([]byte("plain text, not json")),
		base64.StdEncoding.EncodeToString([]byte(`{"noAccessToken":true}`)),
	} {
		if got := decodeClineEmbeddedToken(in); got != nil {
			t.Errorf("decodeClineEmbeddedToken(%q) = %+v, want nil", in, got)
		}
	}
}

// TestClineCodeFromCallbackURL checks both the query-string and fragment forms,
// plus the raw-code fallback, so an operator can paste whatever the browser
// shows.
func TestClineCodeFromCallbackURL(t *testing.T) {
	cases := map[string]string{
		"http://localhost:20128/oauth/cline/callback?code=abc123":         "abc123",
		"http://localhost:20128/oauth/cline/callback?state=x&code=abc123": "abc123",
		"http://localhost:20128/oauth/cline/callback#code=abc123":         "abc123",
		"abc123": "abc123",
		"":       "",
	}
	for in, want := range cases {
		if got := clineCodeFromCallbackURL(in); got != want {
			t.Errorf("clineCodeFromCallbackURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestClineCodeFromCallbackURLPercentEncoded ensures a percent-encoded code is
// decoded before base64 handling; the value is base64, so `+`/`=` routinely
// arrive escaped and would otherwise fail to decode.
func TestClineCodeFromCallbackURLPercentEncoded(t *testing.T) {
	raw := []byte(`{"accessToken":"tok"}`)
	encoded := base64.StdEncoding.EncodeToString(raw)

	u := url.Values{"code": {encoded}}
	callback := "http://localhost:20128/oauth/cline/callback?" + u.Encode()

	code := clineCodeFromCallbackURL(callback)
	if code != encoded {
		t.Fatalf("extracted code = %q, want %q", code, encoded)
	}
	if got := decodeClineEmbeddedToken(code); got == nil || got.AccessToken != "tok" {
		t.Fatalf("percent-encoded blob did not decode, got %+v", got)
	}
}
