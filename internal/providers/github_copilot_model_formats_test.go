package providers

import "testing"

// gpt-5.6-luna and the rest of the GPT-5.6/6.x family are Responses-API only on
// api.githubcopilot.com: chat/completions answers 400 model_not_supported.
func TestGitHubResponsesOnlyModelsGetResponsesTransport(t *testing.T) {
	cfg := KnownProviders["github"]

	for model, wantURL := range map[string]string{
		"gpt-5.6-luna":       "https://api.githubcopilot.com/responses",
		"gpt-6-sol":          "https://api.githubcopilot.com/responses",
		"mai-code-1.1-flash": "https://api.githubcopilot.com/responses",
	} {
		got := cfg.ForModel(model)
		if got.Format != FormatOpenAIResponses || got.BaseURL != wantURL {
			t.Errorf("%s: got format=%q base=%q, want %q %q", model, got.Format, got.BaseURL, FormatOpenAIResponses, wantURL)
		}
	}

	chat := cfg.ForModel("gpt-4.1")
	if chat.Format != "" || chat.BaseURL != "https://api.githubcopilot.com/chat/completions" {
		t.Errorf("gpt-4.1 must stay on chat completions, got format=%q base=%q", chat.Format, chat.BaseURL)
	}
}

// 466 client_upgrade_required is raised for any CLI older than the current
// release, so the pinned version is part of the contract, not a cosmetic detail.
func TestGitHubCopilotVersionNotBehindUpgradeGate(t *testing.T) {
	if GitHubCopilotCLIVersionString() != GitHubCopilotCLIVersion {
		t.Skip("CLI version overridden by env")
	}
	if GitHubCopilotCLIVersion == "1.0.81-6" || GitHubCopilotCLIVersion < "1.0.100" {
		t.Fatalf("CLI version %q is behind the 466 gate", GitHubCopilotCLIVersion)
	}
}
