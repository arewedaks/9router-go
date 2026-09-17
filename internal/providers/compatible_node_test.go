package providers

import "testing"

// TestSanitizeBaseURL pins the URL shapes an operator actually pastes. The
// Anthropic case is the subtle one: "/v1/messages" must lose only "/messages",
// not the version segment — unlike the Claude Code variant, which does drop it.
func TestSanitizeBaseURL(t *testing.T) {
	cases := []struct {
		name, in, nodeType, compatMode, want string
	}{
		{"openai root", "https://api.openai.com/v1", NodeTypeOpenAICompatible, "", "https://api.openai.com/v1"},
		{"openai trailing slash", "https://api.openai.com/v1/", NodeTypeOpenAICompatible, "", "https://api.openai.com/v1"},
		{"openai pasted chat url", "https://x.dev/v1/chat/completions", NodeTypeOpenAICompatible, "", "https://x.dev/v1"},
		{"openai pasted completions", "https://x.dev/v1/completions", NodeTypeOpenAICompatible, "", "https://x.dev/v1"},
		{"openai whitespace", "  https://x.dev/v1  ", NodeTypeOpenAICompatible, "", "https://x.dev/v1"},
		{"empty stays empty", "   ", NodeTypeOpenAICompatible, "", ""},

		{"anthropic root", "https://api.anthropic.com/v1", NodeTypeAnthropicCompatible, "", "https://api.anthropic.com/v1"},
		{"anthropic pasted messages keeps version", "https://a.dev/v1/messages", NodeTypeAnthropicCompatible, "", "https://a.dev/v1"},
		{"anthropic pasted messages with query keeps version", "https://a.dev/v1/messages?beta=true", NodeTypeAnthropicCompatible, "", "https://a.dev/v1"},

		{"cc drops version segment", "https://cc.dev/v1/messages?beta=true", NodeTypeAnthropicCompatible, CompatModeCC, "https://cc.dev"},
		{"cc plain messages", "https://cc.dev/messages", NodeTypeAnthropicCompatible, CompatModeCC, "https://cc.dev"},
	}
	for _, tc := range cases {
		if got := SanitizeBaseURL(tc.in, tc.nodeType, tc.compatMode); got != tc.want {
			t.Errorf("(%s) SanitizeBaseURL(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestCompatibleNodeID pins the generated-id shapes that the resolution layer
// parses. An Anthropic node must not carry an api type.
func TestCompatibleNodeID(t *testing.T) {
	if got := CompatibleNodeID(NodeTypeOpenAICompatible, "chat", "abc"); got != "openai-compatible-chat-abc" {
		t.Fatalf("openai id = %q", got)
	}
	if got := CompatibleNodeID(NodeTypeOpenAICompatible, "", "abc"); got != "openai-compatible-chat-abc" {
		t.Fatalf("openai id with empty apiType = %q, want the chat default", got)
	}
	if got := CompatibleNodeID(NodeTypeOpenAICompatible, "embeddings", "abc"); got != "openai-compatible-embeddings-abc" {
		t.Fatalf("embeddings id = %q", got)
	}
	if got := CompatibleNodeID(NodeTypeAnthropicCompatible, "chat", "abc"); got != "anthropic-compatible-abc" {
		t.Fatalf("anthropic id = %q, want no apiType", got)
	}
}

// TestNodePrefixCollision pins that a duplicate prefix is detected (and can be
// excluded for an update), and that an empty prefix never collides.
func TestNodePrefixCollision(t *testing.T) {
	existing := map[string]string{"id-a": "bai", "id-b": "atri"}

	if other, clash := NodePrefixCollision(existing, "bai", ""); !clash || other != "id-a" {
		t.Fatalf("expected collision with id-a, got %q clash=%v", other, clash)
	}
	if _, clash := NodePrefixCollision(existing, "new", ""); clash {
		t.Fatal("unexpected collision for a fresh prefix")
	}
	if _, clash := NodePrefixCollision(existing, "  ", ""); clash {
		t.Fatal("empty prefix must not collide")
	}
	// Editing id-a without changing its prefix is not a self-collision.
	if _, clash := NodePrefixCollision(existing, "bai", "id-a"); clash {
		t.Fatal("self-collision must be excluded")
	}
	// Whitespace around the candidate is ignored.
	if _, clash := NodePrefixCollision(existing, " atri ", ""); !clash {
		t.Fatal("whitespace-padded duplicate must still collide")
	}
}
