package paramfix

import (
	"encoding/json/v2"
	"testing"
)

func apply(t *testing.T, provider, model string, body map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	out := Apply(provider, model, raw)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, out)
	}
	return got
}

// Claude rejects temperature with a 400 rather than ignoring it.
func TestStripsTemperatureForClaude(t *testing.T) {
	got := apply(t, "anthropic", "claude-opus-5", map[string]any{
		"model":       "claude-opus-5",
		"temperature": 0.7,
		"max_tokens":  100,
	})
	if _, present := got["temperature"]; present {
		t.Error("temperature must be dropped for Claude")
	}
	if got["max_tokens"] == nil {
		t.Error("unrelated params must survive")
	}
}

// Copilot Claude rejects thinking/reasoning_effort, except the 4.6 line.
func TestGithubClaudeThinkingRules(t *testing.T) {
	withThinking := map[string]any{"thinking": map[string]any{"type": "enabled"}, "reasoning_effort": "high"}

	got := apply(t, "github", "claude-sonnet-4-5", map[string]any{
		"thinking": withThinking["thinking"], "reasoning_effort": "high",
	})
	if _, present := got["thinking"]; present {
		t.Error("thinking must be dropped on older Copilot Claude")
	}
	if _, present := got["reasoning_effort"]; present {
		t.Error("reasoning_effort must be dropped on older Copilot Claude")
	}

	// 4.6 supports them, so the rule must not fire.
	got46 := apply(t, "github", "claude-sonnet-4.6", map[string]any{"reasoning_effort": "high"})
	if _, present := got46["reasoning_effort"]; !present {
		t.Error("4.6 supports reasoning_effort and must keep it")
	}
}

// Strict validators reject replayed reasoning on assistant turns.
func TestDropsReasoningFromAssistantMessagesOnly(t *testing.T) {
	got := apply(t, "groq", "llama-3.3-70b", map[string]any{
		"messages": []any{
			map[string]any{"role": "assistant", "content": "hi", "reasoning_content": "thinking..."},
			map[string]any{"role": "user", "content": "yo", "reasoning_content": "not this one"},
		},
	})
	msgs := got["messages"].([]any)
	if _, present := msgs[0].(map[string]any)["reasoning_content"]; present {
		t.Error("assistant reasoning_content must be dropped for groq")
	}
	// Only assistant turns: a user message carrying the field is untouched so the
	// rule cannot quietly rewrite content the model was meant to see.
	if _, present := msgs[1].(map[string]any)["reasoning_content"]; !present {
		t.Error("user messages must not be modified")
	}
}

// Cloudflare Workers AI needs plain-string content.
func TestFlattensContentForCloudflare(t *testing.T) {
	got := apply(t, "cloudflare-ai", "@cf/meta/llama-3.1-8b", map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "hello "},
				map[string]any{"type": "text", "text": "world"},
			}},
		},
	})
	content := got["messages"].([]any)[0].(map[string]any)["content"]
	if s, ok := content.(string); !ok || s != "hello world" {
		t.Errorf("content = %#v, want the flattened string \"hello world\"", content)
	}
}

// The OpenCode muse-spark floor: the reference has no equivalent rule, so this
// is the one behaviour that only exists because it was observed in the wild.
func TestRaisesMuseSparkOutputFloor(t *testing.T) {
	for _, model := range []string{"muse-spark-1.2-contributor-free", "muse-spark-1.3-contributor-free"} {
		got := apply(t, "opencode", model, map[string]any{"max_output_tokens": 5})
		if got["max_output_tokens"] != float64(16) {
			t.Errorf("%s: max_output_tokens = %v, want 16", model, got["max_output_tokens"])
		}
	}

	// An absent cap means "server default" and must not be invented.
	got := apply(t, "opencode", "muse-spark-1.3-contributor-free", map[string]any{"model": "x"})
	if _, present := got["max_output_tokens"]; present {
		t.Error("an absent cap must stay absent")
	}

	// A value already above the floor is left alone.
	ok := apply(t, "opencode", "muse-spark-1.3-contributor-free", map[string]any{"max_output_tokens": 64})
	if ok["max_output_tokens"] != float64(64) {
		t.Errorf("got %v, want 64 untouched", ok["max_output_tokens"])
	}
}

// Ark pins Kimi at 32768 while the advertised ceiling is far higher, so the
// clamp must apply to every cap key the client might have used.
func TestClampsVolcengineKimi(t *testing.T) {
	SetMaxOutputCeilingFunc(func(provider, model string) int { return 262144 })
	defer SetMaxOutputCeilingFunc(nil)

	got := apply(t, "volcengine-ark", "kimi-k2.7-code", map[string]any{
		"max_tokens": 200000,
	})
	if got["max_tokens"] != float64(32768) {
		t.Errorf("max_tokens = %v, want clamped to 32768", got["max_tokens"])
	}
}

// A rule with no provider and no model selector would fire on every request, and
// a rule with no effect is dead weight that reads as if it does something.
func TestEveryRuleIsScopedAndHasAnEffect(t *testing.T) {
	for i, r := range Rules {
		if r.Provider == "" && r.MatchSubstr == "" && r.Match == nil {
			t.Errorf("Rules[%d]: unscoped rules would rewrite all traffic", i)
		}
		hasEffect := len(r.Drop) > 0 || len(r.DropMessageFields) > 0 || r.FlattenContent ||
			r.MaxOutputCap > 0 || r.MinOutput > 0 || r.ClampToModelCeiling
		if !hasEffect {
			t.Errorf("Rules[%d] matches but changes nothing", i)
		}
	}
}

// A body we cannot parse must pass through untouched rather than be dropped.
func TestUnparseableBodyPassesThrough(t *testing.T) {
	in := []byte("not json at all")
	out := Apply("groq", "llama", in)
	if string(out) != string(in) {
		t.Errorf("body was rewritten: %q -> %q", in, out)
	}
}

// Providers with no matching rule must come back byte-identical.
func TestUnmatchedProviderIsUntouched(t *testing.T) {
	body := map[string]any{"model": "gemini-3-flash", "temperature": 0.5, "max_tokens": 100}
	got := apply(t, "gemini", "gemini-3-flash", body)
	if got["temperature"] != 0.5 {
		t.Error("gemini has no rule and must keep temperature")
	}
	if got["max_tokens"] != float64(100) {
		t.Error("gemini has no rule and must keep max_tokens")
	}
}
