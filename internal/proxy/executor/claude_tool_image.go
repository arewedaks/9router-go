package executor

// Tool results that carry images.
//
// An OpenAI tool message can hold a block array, and MCP servers that return
// screenshots (or any tool that reads an image file) put an `image_url` part
// inside it. Anthropic's tool_result accepts only text blocks and base64 image
// sources, so forwarding the part verbatim made the image silently disappear:
// measured 2026-10-27 against ag/claude-sonnet-4-6 and claude-sonnet, both
// answered "No image was attached to your message" while the request was
// accepted with 200 — the loss was invisible to the caller. Ported from
// upstream 9router v0.5.111 (#5125), which converts the part instead.

import (
	"strings"
)

// convertToolResultContent rewrites an OpenAI tool-message content value into
// something Anthropic's tool_result accepts. Plain strings and unrecognised
// shapes pass through untouched, so the only change is for image parts.
func convertToolResultContent(content any) any {
	blocks, ok := content.([]any)
	if !ok {
		return content
	}

	out := make([]any, 0, len(blocks))
	changed := false
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		if block["type"] != "image_url" {
			out = append(out, raw)
			continue
		}
		if img := claudeImageBlock(block); img != nil {
			out = append(out, img)
			changed = true
			continue
		}
		// An image_url we cannot turn into base64 (a remote URL Anthropic will
		// not fetch) is dropped rather than forwarded as an invalid block: the
		// surrounding text still reaches the model.
		changed = true
	}
	if !changed {
		return content
	}
	return out
}

// claudeImageBlock converts an OpenAI image_url part into an Anthropic image
// block, or nil when the part carries nothing Anthropic can accept.
func claudeImageBlock(block map[string]any) map[string]any {
	url := ""
	switch v := block["image_url"].(type) {
	case string:
		url = v
	case map[string]any:
		url, _ = v["url"].(string)
	}
	if url == "" {
		return nil
	}

	// data:<media-type>;base64,<payload> is the only form that needs no fetch.
	if strings.HasPrefix(url, "data:") {
		rest := strings.TrimPrefix(url, "data:")
		semi := strings.Index(rest, ";base64,")
		if semi <= 0 {
			return nil
		}
		mediaType := rest[:semi]
		payload := rest[semi+len(";base64,"):]
		if mediaType == "" || payload == "" {
			return nil
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mediaType,
				"data":       payload,
			},
		}
	}

	// A remote URL: Anthropic's API takes a url source only for the newer
	// image blocks, and older models reject it. Skip instead of guessing.
	return nil
}
