package providers

import "testing"

// Alysis serves one key on three transports and the gateway 400s a model sent
// to the wrong one, so the per-model rewrite is the difference between a working
// model and "Luna uses /v1/responses for reasoning and tools".
func TestForModelRoutesTransport(t *testing.T) {
	base := "https://gw.example/functions/v1/llm/v1/chat/completions"
	cfg := &ProviderConfig{
		BaseURL:    base,
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"User-Agent": "probe/1",
		},
		ModelFormats: map[string]string{
			"gpt-6-luna":       FormatOpenAIResponses,
			"claude-haiku-5-5": FormatClaude,
			"never-switch":     "",
		},
	}

	chat := cfg.ForModel("deepseek-flash")
	if chat != cfg {
		t.Fatalf("unmapped model should reuse the config pointer, got %+v", chat)
	}

	luna := cfg.ForModel("gpt-6-luna")
	if luna == cfg {
		t.Fatal("mapped model must return a copy, not mutate the shared config")
	}
	if luna.Format != FormatOpenAIResponses {
		t.Errorf("luna format = %q, want %q", luna.Format, FormatOpenAIResponses)
	}
	if want := "https://gw.example/functions/v1/llm/v1/responses"; luna.BaseURL != want {
		t.Errorf("luna base URL = %q, want %q", luna.BaseURL, want)
	}

	claude := cfg.ForModel("claude-haiku-5-5")
	if !claude.IsClaudeFormat() {
		t.Errorf("claude format = %q, want %q", claude.Format, FormatClaude)
	}
	if want := "https://gw.example/functions/v1/llm/v1/messages"; claude.BaseURL != want {
		t.Errorf("claude base URL = %q, want %q", claude.BaseURL, want)
	}
	if got := claude.StaticHeaders["anthropic-version"]; got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
	if got := claude.StaticHeaders["User-Agent"]; got != "probe/1" {
		t.Errorf("existing static header dropped: User-Agent = %q", got)
	}
	if _, ok := cfg.StaticHeaders["anthropic-version"]; ok {
		t.Error("the shared config gained a header; the copy is not isolated")
	}

	if got := cfg.ForModel("never-switch"); got != cfg {
		t.Errorf("empty mapping should be a no-op, got %+v", got)
	}

	// The registry entry itself must map the models the gateway splits; a typo
	// here is invisible until the model 400s in production.
	alysis, ok := KnownProviders["alysis"]
	if !ok {
		t.Fatal("alysis is not registered")
	}
	for model, want := range map[string]string{
		"gpt-6-luna":        FormatOpenAIResponses,
		"gpt-6.1-sol":       FormatOpenAIResponses,
		"claude-haiku-5-5":  FormatClaude,
		"claude-sonnet-5-5": FormatClaude,
	} {
		if got := alysis.ModelFormats[model]; got != want {
			t.Errorf("alysis ModelFormats[%q] = %q, want %q", model, got, want)
		}
	}
	if got := alysis.ForModel("deepseek-flash").Format; got != "" {
		t.Errorf("deepseek-flash must stay on chat completions, format = %q", got)
	}
}
