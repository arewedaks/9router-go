package chat

import (
	"strings"
	"testing"

	"9router/proxy/internal/db"
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
	if ua := cfg.StaticHeaders["User-Agent"]; !strings.Contains(ua, "antigravity/ide/") {
		t.Fatalf("default profile should be IDE, got %q", ua)
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
	if ua := cfg.StaticHeaders["User-Agent"]; !strings.Contains(ua, "antigravity/ide/") {
		t.Fatalf("switching back should restore IDE, got %q", ua)
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
	if ua := cfg.StaticHeaders["User-Agent"]; !strings.Contains(ua, "antigravity/ide/") {
		t.Fatalf("stale per-connection profile must not win; got %q", ua)
	}
}
