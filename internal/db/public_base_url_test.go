package db

import (
	"path/filepath"
	"testing"
)

// The public base URL is copied into snippets the operator pastes into their
// clients, and it is a trust boundary: it is operator-supplied free text that
// ends up in generated URLs. These cases pin what is accepted and, more
// importantly, what is refused — a rejected value must never reach storage in a
// quietly repaired form, because that would hand clients an address resolving
// somewhere the operator never typed.
func TestNormalizePublicBaseURL(t *testing.T) {
	accepted := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"https://api.example.com", "https://api.example.com"},
		{"  https://api.example.com  ", "https://api.example.com"},
		{"https://api.example.com/", "https://api.example.com"},
		{"http://gateway.lan:8080", "http://gateway.lan:8080"},
		{"https://ai.example.com:8443", "https://ai.example.com:8443"},
		// An IPv6 literal is a legal origin; brackets must survive intact.
		{"http://[fd00::1]:20128", "http://[fd00::1]:20128"},
	}
	for _, c := range accepted {
		got, err := NormalizePublicBaseURL(c.in)
		if err != nil {
			t.Errorf("NormalizePublicBaseURL(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizePublicBaseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Each of these is refused outright rather than cleaned up.
	rejected := []struct{ in, why string }{
		{"api.example.com", "no scheme"},
		{"javascript:alert(1)", "script scheme"},
		{"file:///etc/passwd", "file scheme"},
		{"ftp://example.com", "unsupported scheme"},
		{"https://", "no host"},
		{"https://user:pw@example.com", "embedded credentials"},
		{"https://example.com/v1", "path"},
		{"https://example.com/?a=1", "query"},
		{"https://example.com/#frag", "fragment"},
		{"https://example.com\nX-Injected: 1", "header injection"},
		{"https://exa mple.com", "embedded space"},
		{"://nope", "malformed"},
	}
	for _, c := range rejected {
		got, err := NormalizePublicBaseURL(c.in)
		if err == nil {
			t.Errorf("NormalizePublicBaseURL(%q) accepted %q, want rejection (%s)", c.in, got, c.why)
		}
		if got != "" {
			t.Errorf("NormalizePublicBaseURL(%q) returned %q alongside an error, want empty", c.in, got)
		}
	}
}

// The value has to survive a save/load cycle, or the operator's domain silently
// disappears on the next restart and clients are handed the wrong address.
func TestPublicBaseURLSurvivesSettingsRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "publicbaseurl.sqlite")
	database, err := OpenDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := EnsureSchema(database); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	repo := NewRepo(database)

	if err := repo.saveSettings(&SettingsData{PublicBaseURL: "https://ai.example.com"}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	got, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if got.PublicBaseURL != "https://ai.example.com" {
		t.Errorf("PublicBaseURL = %q after reload, want %q", got.PublicBaseURL, "https://ai.example.com")
	}
}
