package providers

import (
	"strings"
	"testing"
)

func TestGitHubCopilotChatHeadersUseCLIIdentity(t *testing.T) {
	// The catalogue-unlock lever: the CLI identity exposes the full entitled
	// model set, so a regression back to vscode-chat would silently narrow
	// Import again.
	cfg, ok := KnownProviders["github"]
	if !ok {
		t.Fatal("github provider missing from KnownProviders")
	}
	if cfg.BaseURL != "https://api.githubcopilot.com/chat/completions" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}

	h := cfg.StaticHeaders
	if got := h["copilot-integration-id"]; got != "copilot-developer-cli" {
		t.Errorf("copilot-integration-id = %q, want copilot-developer-cli", got)
	}
	if got := h["user-agent"]; !strings.HasPrefix(got, "copilot/") {
		t.Errorf("user-agent = %q, want copilot/<version>", got)
	}
	if got := h["openai-intent"]; got != "conversation-agent" {
		t.Errorf("openai-intent = %q, want conversation-agent", got)
	}
	if got := h["copilot-harness-id"]; got != "copilot-sdk" {
		t.Errorf("copilot-harness-id = %q, want copilot-sdk", got)
	}
	if got := h["x-github-api-version"]; got != GitHubCopilotAPIVersion {
		t.Errorf("x-github-api-version = %q, want %q", got, GitHubCopilotAPIVersion)
	}
	if got := h["x-client-machine-id"]; strings.TrimSpace(got) == "" {
		t.Error("x-client-machine-id must be a stable non-empty fingerprint")
	}
	// VS Code-only headers must be ABSENT: an over-complete fingerprint is
	// itself a flagging signal (per the reference).
	for _, forbidden := range []string{"editor-plugin-version", "x-vscode-user-agent-library-version"} {
		if _, present := h[forbidden]; present {
			t.Errorf("%s must not be sent by the CLI profile", forbidden)
		}
	}
}

func TestGitHubCopilotMachineIDStable(t *testing.T) {
	// A per-call random id would be an anti-fingerprint tell; the id must be
	// stable for the process.
	if a, b := GitHubCopilotMachineID(), GitHubCopilotMachineID(); a != b {
		t.Errorf("machine id changed between calls: %q vs %q", a, b)
	}
	if got := GitHubCopilotMachineID(); got != GitHubCopilotMachineID() {
		t.Errorf("machine id unstable: %q", got)
	}
}

func TestGitHubCopilotChatHeadersDefaults(t *testing.T) {
	// Empty accept/initiator must fall back to the CLI defaults rather than
	// emitting empty header values.
	h := GitHubCopilotChatHeaders("", "")
	if h["Accept"] != "application/json" {
		t.Errorf("Accept = %q, want application/json", h["Accept"])
	}
	if h["X-Initiator"] != "user" {
		t.Errorf("X-Initiator = %q, want user", h["X-Initiator"])
	}
	h2 := GitHubCopilotChatHeaders("text/event-stream", "agent")
	if h2["Accept"] != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", h2["Accept"])
	}
	if h2["X-Initiator"] != "agent" {
		t.Errorf("X-Initiator = %q, want agent", h2["X-Initiator"])
	}
}

func TestGitHubCopilotRefreshHeadersTokenScheme(t *testing.T) {
	// The refresh endpoint uses the legacy UA, and the caller supplies the
	// `token <pat>` scheme (not Bearer).
	h := GitHubCopilotRefreshHeaders("token gho_abc")
	if h["Authorization"] != "token gho_abc" {
		t.Errorf("Authorization = %q, want token gho_abc", h["Authorization"])
	}
	if h["User-Agent"] != "GithubCopilot/1.0" {
		t.Errorf("User-Agent = %q, want GithubCopilot/1.0", h["User-Agent"])
	}
	if !strings.HasPrefix(h["Editor-Version"], "copilot/") {
		t.Errorf("Editor-Version = %q, want copilot/<version>", h["Editor-Version"])
	}
}
