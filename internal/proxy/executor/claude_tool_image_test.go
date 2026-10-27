package executor

import (
	"encoding/json"
	"testing"
)

// Anthropic's tool_result takes text blocks and base64 image sources only. An
// image_url part inside a tool message used to be forwarded verbatim, which the
// API accepted with 200 while silently dropping the image (measured: the model
// answered "No image was attached"). It must now become an image block.
func TestConvertToolResultContentConvertsImages(t *testing.T) {
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="
	content := []any{
		map[string]any{"type": "text", "text": "screenshot follows"},
		map[string]any{"type": "image_url", "image_url": map[string]any{
			"url": "data:image/png;base64," + png,
		}},
	}

	out, ok := convertToolResultContent(content).([]any)
	if !ok || len(out) != 2 {
		t.Fatalf("blocks = %#v, want 2", convertToolResultContent(content))
	}
	if out[0].(map[string]any)["type"] != "text" {
		t.Error("text block was altered")
	}
	img, ok := out[1].(map[string]any)
	if !ok || img["type"] != "image" {
		t.Fatalf("second block = %#v, want an image block", out[1])
	}
	src, _ := img["source"].(map[string]any)
	if src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != png {
		t.Fatalf("source = %#v, want base64 image/png with the payload intact", src)
	}

	// A plain string tool result is the common case and must not be touched.
	if got := convertToolResultContent("ok"); got != "ok" {
		t.Errorf("string content = %#v, want passthrough", got)
	}

	// A remote URL cannot be expressed as a base64 source, so it is dropped
	// rather than forwarded as an invalid block.
	remote := convertToolResultContent([]any{
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://x/y.png"}},
	}).([]any)
	if len(remote) != 0 {
		t.Errorf("remote image kept: %#v", remote)
	}
}

// The whole conversion must leave a tool_result the API accepts, with the image
// nested where Anthropic looks for it.
func TestConvertOpenAIMessagesToClaudeKeepsToolImage(t *testing.T) {
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="
	messages := []any{
		map[string]any{"role": "user", "content": "look"},
		map[string]any{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []any{map[string]any{
				"id":       "call_1",
				"type":     "function",
				"function": map[string]any{"name": "screenshot", "arguments": "{}"},
			}},
		},
		map[string]any{
			"role":         "tool",
			"tool_call_id": "call_1",
			"content": []any{
				map[string]any{"type": "text", "text": "here"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + png}},
			},
		},
	}

	_, msgs := convertOpenAIMessagesToClaude(messages)
	raw, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, m := range decoded {
		blocks, _ := m["content"].([]any)
		for _, b := range blocks {
			block, ok := b.(map[string]any)
			if !ok || block["type"] != "tool_result" {
				continue
			}
			inner, _ := block["content"].([]any)
			for _, ib := range inner {
				innerBlock, ok := ib.(map[string]any)
				if ok && innerBlock["type"] == "image" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("no image block survived the conversion: %s", raw)
	}
}
