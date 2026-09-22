package chat

import (
	"testing"

	"9router/proxy/internal/providers"
)

// A third-party Claude-format provider must NOT be treated as Anthropic's own
// host: the credential handling (OAuth bearer, Claude-Code cloaking) is
// Anthropic-specific, while the format marker is what drives translation.
func TestClaudeFormatProvidersAreNotAnthropicUpstream(t *testing.T) {
	for _, name := range []string{"agentrouter", "zcode"} {
		cfg, ok := providers.KnownProviders[name]
		if !ok {
			t.Fatalf("%s has no provider config", name)
		}
		if !cfg.IsClaudeFormat() {
			t.Errorf("%s is not marked FormatClaude", name)
		}
		if isAnthropicUpstream(name, &cfg) {
			t.Errorf("%s must not be treated as Anthropic's own host", name)
		}
		// ...but the response still needs Claude translation.
		if !upstreamClaudeWanted(false, &cfg, false) {
			t.Errorf("%s must request Claude response translation", name)
		}
	}
}

// Anthropic's own host still behaves as before.
func TestAnthropicHostStillDetected(t *testing.T) {
	cfg, _ := providers.KnownProviders["claude"]
	if !isAnthropicUpstream("claude", &cfg) {
		t.Error("claude provider no longer detected as Anthropic upstream")
	}
	if !upstreamClaudeWanted(true, &cfg, false) {
		t.Error("Anthropic upstream no longer requests Claude translation")
	}
	// A client already speaking Messages needs no translation.
	if upstreamClaudeWanted(true, &cfg, true) {
		t.Error("claudeNative request should not request translation")
	}
}
