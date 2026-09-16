package providers

import (
	"strings"
	"testing"
)

// TestClineAccessTokenPrefixesOnce covers the decisive rule: Cline rejects a
// bare bearer, so the prefix must be applied — but exactly once. Re-normalizing
// an already-normalized value (which happens whenever a stored token is read
// back and passed through again) must not yield `workos:workos:…`, because that
// is a hard 401 that looks like an invalid token rather than a client bug.
func TestClineAccessTokenPrefixesOnce(t *testing.T) {
	const raw = "eyJhbGciOiJSUzI1NiJ9.payload.sig"

	got := ClineAccessToken(raw)
	if got != "workos:"+raw {
		t.Fatalf("ClineAccessToken(%q) = %q, want workos: prefix", raw, got)
	}

	// Idempotence: feeding the result back must be a no-op.
	again := ClineAccessToken(got)
	if again != got {
		t.Errorf("ClineAccessToken is not idempotent: %q -> %q", got, again)
	}
	if strings.HasPrefix(again, "workos:workos:") {
		t.Error("double workos: prefix produced — this is the 401 failure mode")
	}
}

// TestClineAccessTokenLeavesBYOKKeys verifies an sk_-style key is passed through
// untouched. Those are direct API keys (ClinePass BYOK) that ride a plain
// bearer; prefixing them would break authentication.
func TestClineAccessTokenLeavesBYOKKeys(t *testing.T) {
	const key = "sk_live_abcdef123456"
	if got := ClineAccessToken(key); got != key {
		t.Errorf("ClineAccessToken(%q) = %q, want it unchanged", key, got)
	}
}

func TestClineAccessTokenEmptyInput(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n"} {
		if got := ClineAccessToken(in); got != "" {
			t.Errorf("ClineAccessToken(%q) = %q, want empty", in, got)
		}
	}
}

func TestClineAccessTokenTrimsWhitespace(t *testing.T) {
	// A token copied out of a UI field routinely carries surrounding spaces; the
	// prefix has to land before the token, not before the whitespace.
	got := ClineAccessToken("  abc.def  ")
	if got != "workos:abc.def" {
		t.Errorf("ClineAccessToken with padding = %q, want workos:abc.def", got)
	}
}

func TestClineAuthHeader(t *testing.T) {
	if got := ClineAuthHeader("tok"); got != "Bearer workos:tok" {
		t.Errorf("ClineAuthHeader = %q, want Bearer workos:tok", got)
	}
	// An empty token must yield an empty header so callers can detect "no
	// credential" instead of sending a malformed `Bearer ` value.
	if got := ClineAuthHeader("   "); got != "" {
		t.Errorf("ClineAuthHeader(blank) = %q, want empty", got)
	}
}

// TestClineClientIdentityHeaders pins the identity set. Cline's gateway rejects
// requests that do not identify as a Cline client, and the model catalogue
// endpoint needs the full set — not just Authorization.
func TestClineClientIdentityHeaders(t *testing.T) {
	h := ClineClientIdentityHeaders()

	want := map[string]string{
		"HTTP-Referer":       "https://cline.bot",
		"X-Title":            "Cline",
		"User-Agent":         "Cline/" + ClineClientVersion,
		"X-CLIENT-TYPE":      "cline-cli",
		"X-CLIENT-VERSION":   ClineClientVersion,
		"X-CORE-VERSION":     ClineClientVersion,
		"X-PLATFORM":         "linux",
		"X-PLATFORM-VERSION": ClineClientVersion,
		"X-IS-MULTIROOT":     "false",
	}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("header %s = %q, want %q", k, h[k], v)
		}
	}
}

// TestClineClientIdentityHeadersAreFresh copies the invariant that callers may
// mutate the returned map (the catalogue handler stamps extra headers): a shared
// map would leak those mutations into every other Cline request.
func TestClineClientIdentityHeadersAreFresh(t *testing.T) {
	first := ClineClientIdentityHeaders()
	first["X-LEAK"] = "1"

	if _, leaked := ClineClientIdentityHeaders()["X-LEAK"]; leaked {
		t.Error("ClineClientIdentityHeaders returned a shared map")
	}
}
