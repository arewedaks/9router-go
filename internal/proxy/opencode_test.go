package proxy_test

import (
	"testing"

	"9router/proxy/internal/proxy"
)

func TestBuildOpenCodeHeaders(t *testing.T) {
	headers := proxy.BuildOpenCodeHeaders(nil, "my-session-123", true)
	if headers["User-Agent"] != proxy.DefaultOpenCodeUA {
		t.Errorf("expected User-Agent %s, got %s", proxy.DefaultOpenCodeUA, headers["User-Agent"])
	}
	if headers["x-opencode-client"] != "cli" {
		t.Errorf("expected x-opencode-client cli, got %s", headers["x-opencode-client"])
	}
	if headers["x-opencode-project"] != "global" {
		t.Errorf("v2 defaults the project to the literal global, got %s", headers["x-opencode-project"])
	}
	if !proxy.OpenCodeSessionRegex.MatchString(headers["x-opencode-session"]) {
		t.Errorf("expected canonical x-opencode-session matching regex, got %s", headers["x-opencode-session"])
	}
	if len(headers["x-opencode-session"]) != 30 {
		t.Errorf("expected session length 30, got %d (%s)", len(headers["x-opencode-session"]), headers["x-opencode-session"])
	}
	// v2 sends the session under four twin headers; a v2 fingerprint that sets
	// only one of them is the half-shape the gate refuses.
	for _, h := range []string{"x-opencode-session-id", "x-session-affinity", "x-session-id"} {
		if headers[h] != headers["x-opencode-session"] {
			t.Errorf("%s must mirror x-opencode-session; got %q want %q", h, headers[h], headers["x-opencode-session"])
		}
	}
	// x-opencode-request is a v1-only header: a v2 request must not carry it.
	if _, ok := headers["x-opencode-request"]; ok {
		t.Error("x-opencode-request must not be sent: it is a v1-only header")
	}
	if headers["Accept"] != "text/event-stream" {
		t.Errorf("expected Accept text/event-stream, got %s", headers["Accept"])
	}

	// Non-stream accepts */*
	headersNonStream := proxy.BuildOpenCodeHeaders(nil, "", false)
	if headersNonStream["Accept"] != "*/*" {
		t.Errorf("expected Accept */*, got %s", headersNonStream["Accept"])
	}

	// x-api-key must stay absent. Measured 2026-10-15: an otherwise-identical
	// request answers 200 without it and 403 FreeTierError with x-api-key:
	// public added back.
	if _, ok := headers["x-api-key"]; ok {
		t.Error("x-api-key must not be sent: adding it flips the gate to 403")
	}
}

func TestOpenCodeCanonicalSession(t *testing.T) {
	for range 20 {
		ses := proxy.GenerateOpenCodeSessionID()
		if !proxy.OpenCodeSessionRegex.MatchString(ses) || len(ses) != 30 {
			t.Fatalf("invalid session ID generated: %s (len %d)", ses, len(ses))
		}

		req := proxy.GenerateOpenCodeRequestID()
		if !proxy.OpenCodeRequestRegex.MatchString(req) || len(req) != 30 {
			t.Fatalf("invalid request ID generated: %s (len %d)", req, len(req))
		}
	}

	// Preserves valid canonical session
	valid := "ses_f52b0d414ffeObbCKcHUQYZR2I"
	if translated := proxy.TranslateOpenCodeSessionID(valid, ""); translated != valid {
		t.Errorf("expected %s preserved, got %s", valid, translated)
	}

	// Translates foreign session to canonical 30 chars
	translated := proxy.TranslateOpenCodeSessionID("uuid-1234-5678-abcdef", "claude")
	if !proxy.OpenCodeSessionRegex.MatchString(translated) || len(translated) != 30 {
		t.Errorf("expected valid canonical session from foreign ID, got %s", translated)
	}
}

func TestHasValidOpenCodeVersion(t *testing.T) {
	cases := []struct {
		ua    string
		valid bool
	}{
		{"opencode", false},
		{"opencode/1.16.5", false},
		{"opencode/1.17.0", true},
		{"opencode/1.18.31", true},
		{"opencode/2.0.0", true},
		{"Claude-Code/1.0", false},
		{"curl/8.7.1", false},
	}
	for _, c := range cases {
		if got := proxy.HasValidOpenCodeVersion(c.ua); got != c.valid {
			t.Errorf("HasValidOpenCodeVersion(%q) = %v, want %v", c.ua, got, c.valid)
		}
	}
}

func TestBuildOpenCodeHeaders_PreservesRelayHeaders(t *testing.T) {
	raw := map[string]string{
		"x-relay-target": "https://opencode.ai",
		"x-relay-path":   "/inference/openai/v1/chat/completions",
	}
	res := proxy.BuildOpenCodeHeaders(raw, "", true)
	if res["x-relay-target"] != "https://opencode.ai" {
		t.Errorf("x-relay-target was dropped: got %q", res["x-relay-target"])
	}
	if res["x-relay-path"] != "/inference/openai/v1/chat/completions" {
		t.Errorf("x-relay-path was dropped: got %q", res["x-relay-path"])
	}
}
