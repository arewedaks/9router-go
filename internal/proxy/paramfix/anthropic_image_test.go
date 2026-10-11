package paramfix

import (
	"encoding/json/v2"
	"testing"
)

// A client speaking the Anthropic dialect pointed at /v1/chat/completions sends
// blocks the OpenAI-compatible upstream refuses:
//
//	{"message":"glm-5.3-flash takes images only, each as a top-level image_url
//	 content part, so this image part is refused.","param":"messages[1].content[1]"}
//
// The nested `param` is why the field repair cannot help — the fix is to
// translate the block, not delete it.
func TestConvertAnthropicImageBlocks(t *testing.T) {
	body := []byte(`{"model":"glm-5.3-flash","messages":[
		{"role":"user","content":[
			{"type":"text","text":"what is this"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}
		]}]}`)

	out, n := ConvertAnthropicImageBlocks(body)
	if n != 1 {
		t.Fatalf("converted = %d, want 1", n)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	parts := m["messages"].([]any)[0].(map[string]any)["content"].([]any)

	// The text block must survive untouched.
	textPart := parts[0].(map[string]any)
	if textPart["type"] != "text" || textPart["text"] != "what is this" {
		t.Errorf("text block must be preserved, got %v", textPart)
	}

	// The image block must now be an image_url with a data URL.
	imgPart := parts[1].(map[string]any)
	if imgPart["type"] != "image_url" {
		t.Fatalf("image block must become image_url, got %v", imgPart["type"])
	}
	if _, ok := imgPart["source"]; ok {
		t.Error("the Anthropic source field must be gone")
	}
	url := imgPart["image_url"].(map[string]any)["url"].(string)
	if url != "data:image/png;base64,AAAA" {
		t.Errorf("data URL = %q, want data:image/png;base64,AAAA", url)
	}

	// And it must still be counted as an image by the trim repair.
	if c := countImagesIn(t, out); c != 1 {
		t.Errorf("converted block must count as an image, got %d", c)
	}
}

// A request that is already in OpenAI shape must come back byte-identical, no
// matter how many images it carries.
func TestConvertAnthropicImageBlocksNoOpOnOpenAIShape(t *testing.T) {
	bodies := []string{
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`,
		`{"model":"m","messages":[{"role":"user","content":"plain string"}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`,
	}
	for _, body := range bodies {
		out, n := ConvertAnthropicImageBlocks([]byte(body))
		if n != 0 {
			t.Errorf("body %s: converted %d blocks from an OpenAI-shaped request", body, n)
		}
		if string(out) != body {
			t.Errorf("body %s must pass through unchanged, got %s", body, out)
		}
	}
}

// Blocks this does not understand are left alone rather than guessed at: a wrong
// conversion would silently change what the model is shown.
func TestConvertAnthropicImageBlocksLeavesUnknownShapesAlone(t *testing.T) {
	bodies := []string{
		// URL-sourced Anthropic image: no base64 to rebuild, and the OpenAI
		// equivalent of a remote source is a different translation.
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://x/y.png"}}]}]}`,
		// Empty data.
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]}]}`,
		// Not JSON at all.
		`not json`,
		// messages is not an array.
		`{"model":"m","messages":"string"}`,
	}
	for _, body := range bodies {
		out, n := ConvertAnthropicImageBlocks([]byte(body))
		if n != 0 {
			t.Errorf("body %s: converted %d blocks it should not have", body, n)
		}
		if string(out) != body {
			t.Errorf("body %s must pass through unchanged, got %s", body, out)
		}
	}
}

// The converted form must feed the image-count repair, so a client that sends
// ten Anthropic blocks still gets trimmed to the model's ceiling.
func TestConvertedBlocksAreTrimmed(t *testing.T) {
	parts := []any{map[string]any{"type": "text", "text": "many"}}
	for i := 0; i < 10; i++ {
		parts = append(parts, map[string]any{
			"type":   "image",
			"source": map[string]any{"type": "base64", "media_type": "image/png", "data": "AAAA"},
		})
	}
	raw, err := json.Marshal(map[string]any{
		"model":    "glm-5.3-flash",
		"messages": []any{map[string]any{"role": "user", "content": parts}},
	})
	if err != nil {
		t.Fatal(err)
	}

	converted, n := ConvertAnthropicImageBlocks(raw)
	if n != 10 {
		t.Fatalf("converted = %d, want 10", n)
	}
	trimmed, removed := TrimImagesToLimit(converted, 8)
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if c := countImagesIn(t, trimmed); c != 8 {
		t.Errorf("remaining images = %d, want 8", c)
	}
}
