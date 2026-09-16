package dashboard

import (
	"strings"
	"testing"
)

func TestIsGitHubProvider(t *testing.T) {
	for _, id := range []string{"github", "gh", "copilot", "GitHub", " GH "} {
		if !isGitHubProvider(id) {
			t.Errorf("isGitHubProvider(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"gitlab", "codebuddy-cn", "antigravity", ""} {
		if isGitHubProvider(id) {
			t.Errorf("isGitHubProvider(%q) = true, want false", id)
		}
	}
}

func TestParseGitHubCopilotCatalogKeepsEntitledChatModels(t *testing.T) {
	// A live response contains both chat models and non-chat utilities. Only the
	// entitled chat models should survive, and the filter must be
	// capability-driven so newly-entitled ids appear without a code change.
	raw := []byte(`{
		"data": [
			{"id":"claude-opus-5","name":"Claude Opus 5","capabilities":{"type":"chat"}},
			{"id":"brand-new-model-2099","name":"Unreleased","capabilities":{"type":"chat"}},
			{"id":"text-embedding-3-small","name":"Embeddings","capabilities":{"type":"embeddings"}},
			{"id":"some-completion","name":"Completion","capabilities":{"type":"completion"}},
			{"id":"gemini-3.7-flash","name":"Gemini 3.7 Flash","model_picker_enabled":true}
		]
	}`)

	models := parseGitHubCopilotCatalog(raw)
	got := map[string]string{}
	for _, m := range models {
		got[m.ID] = m.Name
	}

	if _, ok := got["claude-opus-5"]; !ok {
		t.Error("claude-opus-5 (capabilities.type=chat) should be kept")
	}
	// A model absent from any allowlist must still appear: that is the whole
	// point of capability-driven filtering.
	if _, ok := got["brand-new-model-2099"]; !ok {
		t.Error("brand-new-model-2099 should be kept (capability-driven, not allowlisted)")
	}
	if _, ok := got["text-embedding-3-small"]; ok {
		t.Error("embedding model must be dropped")
	}
	if _, ok := got["some-completion"]; ok {
		t.Error("completion (non-chat) model must be dropped")
	}
	// No capabilities.type and no endpoints, id is not a known utility: keep it.
	if _, ok := got["gemini-3.7-flash"]; !ok {
		t.Error("gemini-3.7-flash (no capabilities.type, chat-shaped id) should be kept")
	}
	if len(models) != 3 {
		t.Errorf("kept %d models, want 3", len(models))
	}
}

func TestParseGitHubCopilotCatalogRespectsPolicyAndPicker(t *testing.T) {
	raw := []byte(`{
		"data": [
			{"id":"disabled-model","capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
			{"id":"unlisted-model","capabilities":{"type":"chat"},"model_picker_enabled":false},
			{"id":"enabled-model","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"model_picker_enabled":true}
		]
	}`)
	models := parseGitHubCopilotCatalog(raw)
	got := map[string]bool{}
	for _, m := range models {
		got[m.ID] = true
	}
	// policy.state="disabled" is an authoritative rejection.
	if got["disabled-model"] {
		t.Errorf("disabled-model should be dropped: %+v", models)
	}
	// model_picker_enabled=false with no policy verdict is only a weak hint, and
	// the row still advertises chat capability. Kept: dropping these is exactly
	// the bug that emptied the catalogue for real accounts.
	if !got["unlisted-model"] {
		t.Errorf("unlisted-model should be kept (capability-driven): %+v", models)
	}
	if !got["enabled-model"] {
		t.Errorf("enabled-model should be kept: %+v", models)
	}
}

// TestParseGitHubCopilotCatalogKeepsEntitledModelsWithPickerFalse pins the live
// finding that broke Import Model: an entitled account sees models with
// policy.state="enabled" while model_picker_enabled is false. They must survive.
func TestParseGitHubCopilotCatalogKeepsEntitledModelsWithPickerFalse(t *testing.T) {
	raw := []byte(`{
		"data": [
			{"id":"gpt-4.1","name":"GPT-4.1","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"model_picker_enabled":false},
			{"id":"claude-haiku-4.5","name":"Claude Haiku 4.5","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"model_picker_enabled":false}
		]
	}`)
	models := parseGitHubCopilotCatalog(raw)
	if len(models) != 2 {
		t.Fatalf("kept %d entitled models, want 2: %+v", len(models), models)
	}
}

func TestParseGitHubCopilotCatalogSupportedEndpointsFallback(t *testing.T) {
	// With no capabilities.type, a chat-shaped supported_endpoints entry keeps
	// the row; a non-chat endpoint set drops it.
	raw := []byte(`{
		"data": [
			{"id":"via-responses","supported_endpoints":["/responses"]},
			{"id":"via-messages","capabilities":{"supported_endpoints":["/v1/messages"]}},
			{"id":"via-embeddings-only","supported_endpoints":["/embeddings"]}
		]
	}`)
	models := parseGitHubCopilotCatalog(raw)
	got := map[string]bool{}
	for _, m := range models {
		got[m.ID] = true
	}
	if !got["via-responses"] || !got["via-messages"] {
		t.Errorf("chat-shaped endpoint models missing: %+v", models)
	}
	if got["via-embeddings-only"] {
		t.Error("embeddings-only endpoints must be dropped")
	}
}

func TestParseGitHubCopilotCatalogDeduplicates(t *testing.T) {
	raw := []byte(`{"data":[
		{"id":"dup","capabilities":{"type":"chat"}},
		{"id":"dup","capabilities":{"type":"chat"}}
	]}`)
	if models := parseGitHubCopilotCatalog(raw); len(models) != 1 {
		t.Fatalf("got %d models, want 1 after dedup", len(models))
	}
}

func TestParseGitHubCopilotCatalogRejectsGarbage(t *testing.T) {
	if models := parseGitHubCopilotCatalog([]byte(`not json`)); models != nil {
		t.Errorf("garbage input should yield nil, got %+v", models)
	}
}

func TestGitHubStaticFallbackModels(t *testing.T) {
	models := githubStaticFallbackModels()
	if len(models) != len(githubCopilotStaticFallbackModels) {
		t.Fatalf("got %d fallback models, want %d", len(models), len(githubCopilotStaticFallbackModels))
	}
	for _, m := range models {
		if strings.TrimSpace(m.ID) == "" {
			t.Error("fallback model with empty id")
		}
	}
	// Fallback is a safety net, not the primary source: it should contain the
	// well-known Claude/GPT anchors so an offline Import still looks sane.
	seen := map[string]bool{}
	for _, m := range models {
		seen[m.ID] = true
	}
	for _, want := range []string{"claude-opus-4.6", "gpt-5.4", "gemini-3.6-flash"} {
		if !seen[want] {
			t.Errorf("fallback catalogue missing %q", want)
		}
	}
}

func TestCopilotTokenFromDataPrefersCopilotToken(t *testing.T) {
	// The Copilot API rejects the GitHub access token, so the catalogue call must
	// read providerSpecificData.copilotToken rather than the top-level accessToken.
	data := `{"accessToken":"gho_github","providerSpecificData":{"copilotToken":"copilot_xyz"}}`
	if got := copilotTokenFromData(data); got != "copilot_xyz" {
		t.Errorf("copilotTokenFromData = %q, want copilot_xyz", got)
	}
	if got := copilotTokenFromData(`{"accessToken":"gho_github"}`); got != "" {
		t.Errorf("copilotTokenFromData without the field = %q, want empty", got)
	}
	if got := copilotTokenFromData(`not json`); got != "" {
		t.Errorf("copilotTokenFromData on garbage = %q, want empty", got)
	}
}
