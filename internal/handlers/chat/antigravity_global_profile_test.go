package chat

import (
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// newAntigravityHandler returns a chat handler backed by a temp DB so the
// provider-wide client profile can be written through the real settings row.
func newAntigravityHandler(t *testing.T) *ChatHandler {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	return NewChatHandler(db.NewRepo(database), nil)
}

// TestAntigravityUserAgentFollowsGlobalSetting is the regression test for moving
// the client profile off the connection and onto settings.
//
// Two accounts on the same provider must present the SAME identity, because the
// profile describes the client being emulated, not the account. The old
// per-connection value allowed them to drift, so this asserts the setting alone
// decides the User-Agent.
func TestAntigravityUserAgentFollowsGlobalSetting(t *testing.T) {
	h := newAntigravityHandler(t)

	cfg, err := h.getProviderConfig("antigravity", &ConnectionData{})
	if err != nil {
		t.Fatalf("getProviderConfig: %v", err)
	}
	// Both profiles now resolve to one identity (the CLI shape), because the
	// ide/ flavor is answered HTTP 404 for Gemini 4 Argon models. What this test
	// still has to prove is that the setting is read from the repository at all:
	// a config that ignored it would keep emitting whatever was compiled in.
	want := providers.AntigravityUserAgent(providers.AntigravityProfileIDE)
	if ua := cfg.StaticHeaders["User-Agent"]; ua != want {
		t.Fatalf("default profile UA = %q, want %q", ua, want)
	}

	if err := h.Repo.SetAntigravityClientProfile("cli"); err != nil {
		t.Fatalf("SetAntigravityClientProfile: %v", err)
	}
	cfg, err = h.getProviderConfig("antigravity", &ConnectionData{})
	if err != nil {
		t.Fatalf("getProviderConfig: %v", err)
	}
	ua := cfg.StaticHeaders["User-Agent"]
	if !strings.Contains(ua, "antigravity/cli/") {
		t.Fatalf("after setting cli the User-Agent should be CLI, got %q", ua)
	}
	if !strings.Contains(ua, "auth_method=consumer") {
		t.Errorf("CLI identity should carry auth_method=consumer, got %q", ua)
	}

	if err := h.Repo.SetAntigravityClientProfile("ide"); err != nil {
		t.Fatalf("SetAntigravityClientProfile: %v", err)
	}
	cfg, err = h.getProviderConfig("antigravity", &ConnectionData{})
	if err != nil {
		t.Fatalf("getProviderConfig: %v", err)
	}
	if ua := cfg.StaticHeaders["User-Agent"]; ua != providers.AntigravityUserAgent(providers.AntigravityProfileIDE) {
		t.Fatalf("switching back should restore the IDE profile's identity, got %q", ua)
	}
}

// A stale per-account clientProfile left in connection data must be ignored:
// otherwise the old value would silently override the global dropdown and the UI
// would show one identity while the router sent another.
func TestAntigravityIgnoresStalePerConnectionProfile(t *testing.T) {
	h := newAntigravityHandler(t)

	if err := h.Repo.SetAntigravityClientProfile("ide"); err != nil {
		t.Fatalf("SetAntigravityClientProfile: %v", err)
	}

	connData := &ConnectionData{
		ProviderSpecificData: map[string]any{
			"clientProfile": "cli",
		},
	}
	cfg, err := h.getProviderConfig("antigravity", connData)
	if err != nil {
		t.Fatalf("getProviderConfig: %v", err)
	}
	// A stale per-connection profile must not override the account-wide
	// setting. Both profiles share one identity now, so pin the exact value the
	// global default produces rather than a substring of the old ide/ shape.
	want := providers.AntigravityUserAgent(providers.AntigravityProfileIDE)
	if ua := cfg.StaticHeaders["User-Agent"]; ua != want {
		t.Fatalf("stale per-connection profile must not win; got %q want %q", ua, want)
	}
}
