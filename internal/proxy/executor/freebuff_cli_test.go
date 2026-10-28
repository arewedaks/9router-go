package executor

import (
	"strings"
	"testing"
)

// The released CLI opens every free-lane request with the Buffy marker at
// position 0 and no foreign-harness identity. A foreign phrase is what the free
// lane downgrades on — the model changes silently, so the failure looks like a
// wrong answer rather than an error.
func TestFreebuffNormalizeSystemPrompt(t *testing.T) {
	t.Run("prepends marker when there is no system message", func(t *testing.T) {
		body := map[string]any{"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		}}
		freebuffNormalizeSystemPrompt(body)
		msgs := body["messages"].([]any)
		if len(msgs) != 2 {
			t.Fatalf("messages = %d, want 2", len(msgs))
		}
		first := msgs[0].(map[string]any)
		if first["role"] != "system" || first["content"] != freebuffSystemMarker {
			t.Fatalf("first = %#v", first)
		}
	})

	t.Run("keeps the caller prompt after the marker", func(t *testing.T) {
		body := map[string]any{"messages": []any{
			map[string]any{"role": "system", "content": "You are a reviewer."},
			map[string]any{"role": "user", "content": "hi"},
		}}
		freebuffNormalizeSystemPrompt(body)
		content := body["messages"].([]any)[0].(map[string]any)["content"].(string)
		if !strings.HasPrefix(content, freebuffSystemMarker) {
			t.Fatalf("content = %q, want the marker prefix", content)
		}
		if !strings.Contains(content, "You are a reviewer.") {
			t.Errorf("caller prompt was dropped: %q", content)
		}
	})

	t.Run("scrubs foreign harness identity", func(t *testing.T) {
		body := map[string]any{"messages": []any{
			map[string]any{"role": "system", "content": "You are Claude Code, Anthropic's official CLI. cc_version=2.0.1"},
			map[string]any{"role": "user", "content": "hi"},
		}}
		freebuffNormalizeSystemPrompt(body)
		content := body["messages"].([]any)[0].(map[string]any)["content"].(string)
		for _, banned := range []string{"You are Claude Code", "Anthropic's official CLI", "cc_version="} {
			if strings.Contains(content, banned) {
				t.Errorf("foreign marker %q survived: %q", banned, content)
			}
		}
		if !strings.HasPrefix(content, freebuffSystemMarker) {
			t.Errorf("marker is not the prefix: %q", content)
		}
	})

	t.Run("developer role becomes system", func(t *testing.T) {
		body := map[string]any{"messages": []any{
			map[string]any{"role": "developer", "content": "rules"},
		}}
		freebuffNormalizeSystemPrompt(body)
		first := body["messages"].([]any)[0].(map[string]any)
		if first["role"] != "system" {
			t.Fatalf("role = %v, want system", first["role"])
		}
	})

	t.Run("already-marked body is not double-marked", func(t *testing.T) {
		body := map[string]any{"messages": []any{
			map[string]any{"role": "system", "content": freebuffSystemMarker + "\n\nrules"},
		}}
		freebuffNormalizeSystemPrompt(body)
		content := body["messages"].([]any)[0].(map[string]any)["content"].(string)
		if strings.Count(content, freebuffSystemMarker) != 1 {
			t.Fatalf("marker count = %d, want 1: %q", strings.Count(content, freebuffSystemMarker), content)
		}
	})

	t.Run("block content gets the marker in its first text block", func(t *testing.T) {
		body := map[string]any{"messages": []any{
			map[string]any{"role": "system", "content": []any{
				map[string]any{"type": "text", "text": "be terse"},
			}},
		}}
		freebuffNormalizeSystemPrompt(body)
		blocks := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
		text := blocks[0].(map[string]any)["text"].(string)
		if !strings.HasPrefix(text, freebuffSystemMarker) {
			t.Fatalf("text = %q", text)
		}
	})
}

// The CLI sends stream:true unconditionally and asks for usage; a request
// without usage is one the gateway cannot meter.
func TestFreebuffEnsureStreaming(t *testing.T) {
	body := map[string]any{"model": "x"}
	freebuffEnsureStreaming(body)
	if body["stream"] != true {
		t.Errorf("stream = %v, want true", body["stream"])
	}
	opts, _ := body["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Errorf("include_usage = %v, want true", opts["include_usage"])
	}

	// A caller that set include_usage=false is overridden, matching the CLI.
	body2 := map[string]any{"stream_options": map[string]any{"include_usage": false}}
	freebuffEnsureStreaming(body2)
	if body2["stream_options"].(map[string]any)["include_usage"] != true {
		t.Error("caller's include_usage=false was not overridden")
	}
}
