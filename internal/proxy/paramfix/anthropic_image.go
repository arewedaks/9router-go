package paramfix

import (
	"bytes"
	"encoding/json/v2"
	"strings"
)

// anthropicImageBlockToOpenAI rewrites Anthropic-shaped content blocks into the
// OpenAI shape the compatible endpoints expect, and reports whether any block
// was changed.
//
// A client speaking the Anthropic dialect can be pointed at /v1/chat/completions
// instead of /v1/messages, and the translator never runs on that path. The body
// then reaches an OpenAI-compatible upstream carrying:
//
//	{"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}
//
// which such an upstream refuses with a 400 naming a nested path:
//
//	{"message":"glm-5.3-flash takes images only, each as a top-level image_url
//	 content part, so this image part is refused.","param":"messages[1].content[1]"}
//
// The generic field repair cannot help: the offending path is nested, and the
// fix is to translate the block, not to delete it. So the shapes are converted
// in place. This mirrors what internal/translator does for /v1/messages, kept
// here as well because that path is not the only one a client can take.
//
// The conversion is shape-driven and conservative: a block that is already
// image_url, or that does not carry a base64 source this understands, is left
// exactly as it was.
func anthropicImageBlockToOpenAI(body []byte) ([]byte, int) {
	if len(body) == 0 || !bytes.Contains(body, []byte(`"source"`)) {
		return body, 0
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, 0
	}
	msgs, ok := m["messages"].([]any)
	if !ok {
		return body, 0
	}

	converted := 0
	for _, rawMsg := range msgs {
		msg, ok := rawMsg.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for i, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			// Only the Anthropic spelling is rewritten. Existing OpenAI blocks,
			// Gemini inlineData and anything else are left to the caller.
			if t, _ := part["type"].(string); !strings.EqualFold(t, "image") {
				continue
			}
			src, ok := part["source"].(map[string]any)
			if !ok {
				continue
			}
			if s, _ := src["type"].(string); !strings.EqualFold(s, "base64") {
				continue
			}
			data, _ := src["data"].(string)
			if data == "" {
				continue
			}
			mediaType, _ := src["media_type"].(string)
			if mediaType == "" {
				mediaType = "image/png"
			}
			parts[i] = map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": "data:" + mediaType + ";base64," + data},
			}
			converted++
		}
		msg["content"] = parts
	}
	if converted == 0 {
		return body, 0
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body, 0
	}
	return out, converted
}

// ConvertAnthropicImageBlocks rewrites Anthropic-style image blocks in an OpenAI
// request body so strict compatible endpoints accept them. It returns the body
// unchanged and 0 when there is nothing to convert.
func ConvertAnthropicImageBlocks(body []byte) ([]byte, int) {
	return anthropicImageBlockToOpenAI(body)
}
