package paramfix

import (
	"encoding/json/v2"
	"regexp"
	"strconv"
	"strings"
)

// ImageLimit identifies an upstream refusal over the number of images in a
// request, returning the model's limit. ok is false when the body is not that
// kind of rejection.
//
// Wally/RunAnywhere answers with:
//
//	{"message":"This request carries 9 images; glm-5.3-flash takes at most 8 per request.",
//	 "type":"invalid_request_error","code":"bad_request","param":"messages"}
//
// The `param` is `messages` — the images live inside it — which is exactly why
// the generic field-drop repair cannot handle this: removing `messages` would
// destroy the conversation. The limit is a quantity, so the repair is to trim
// the excess, not to drop a field.
// The trailing "image" is optional: Wally writes "at most 8 per request" while
// other gateways spell out "at most 8 images per request".
var imageLimitRe = regexp.MustCompile(`(?i)carries\s+(\d+)\s+images?\s*;.*?at most\s+(\d+)\s+(?:images?\s+)?per request`)

// ImageLimitFromErrorBody extracts the per-request image ceiling an upstream
// named in its refusal. It returns ok=false when the body is not an image-count
// rejection.
func ImageLimitFromErrorBody(body []byte) (limit int, ok bool) {
	if len(body) == 0 {
		return 0, false
	}
	m := imageLimitRe.FindSubmatch(body)
	if m == nil {
		return 0, false
	}
	// Group 2 is the ceiling; group 1 is only how many we sent, which the caller
	// already knows and which must not be trusted over the real count in the body.
	n, err := strconv.Atoi(string(m[2]))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// TrimImagesToLimit removes images from the request until it carries at most
// limit of them, and reports how many were removed.
//
// Images are dropped oldest-first: the most recent turn is what the caller is
// actually asking about, while older images are context the model has already
// seen. Within a single message the later images are kept for the same reason.
//
// A body that cannot be parsed, or that carries a shape this does not recognise,
// is returned unchanged with 0 removed — an unrecognised request is not ours to
// rewrite.
func TrimImagesToLimit(body []byte, limit int) ([]byte, int) {
	if len(body) == 0 || limit <= 0 {
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

	total := countImages(msgs)
	if total <= limit {
		return body, 0
	}
	remove := total - limit

	// Walk oldest message first: the newest turn is what the caller is asking
	// about, while older images are context the model has already seen.
	for i := 0; i < len(msgs) && remove > 0; i++ {
		msg, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		// Within a message, keep the latest images for the same reason.
		kept := make([]any, 0, len(parts))
		for j := 0; j < len(parts); j++ {
			if remove > 0 && isImagePart(parts[j]) {
				remove--
				continue
			}
			kept = append(kept, parts[j])
		}
		if len(kept) == 0 {
			kept = append(kept, map[string]any{"type": "text", "text": "[images omitted: over the model's per-request image limit]"})
		}
		msg["content"] = kept
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body, 0
	}
	return out, total - limit
}

// countImages counts image parts across every message.
func countImages(msgs []any) int {
	n := 0
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, p := range parts {
			if isImagePart(p) {
				n++
			}
		}
	}
	return n
}

// isImagePart reports whether a content part carries an image in any of the
// spellings the supported providers use.
func isImagePart(p any) bool {
	part, ok := p.(map[string]any)
	if !ok {
		return false
	}
	t, _ := part["type"].(string)
	switch strings.ToLower(t) {
	case "image_url", "image", "input_image", "image_url_delta":
		return true
	}
	// Gemini-style inlineData, which reaches some compatible gateways verbatim.
	if _, ok := part["inlineData"]; ok {
		return true
	}
	return false
}
