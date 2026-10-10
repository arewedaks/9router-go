package providers

import "testing"

// The operator-facing requirement: Wally must appear in the API-key catalogue
// with a usable endpoint, so a user pastes a key and is done — no custom node.
func TestWallyIsAPIKeyProvider(t *testing.T) {
	p, ok := GetProviderMeta("wally")
	if !ok {
		t.Fatal("wally is not in the provider catalogue")
	}
	if p.Category != CategoryAPIKey {
		t.Errorf("wally category = %q, want %q", p.Category, CategoryAPIKey)
	}
	if p.AuthType != "apikey" {
		t.Errorf("wally authType = %q, want apikey", p.AuthType)
	}
	if cfg := KnownProviders["wally"]; cfg.BaseURL == "" {
		t.Error("wally has no base URL, so a pasted key has nowhere to go")
	}
}
