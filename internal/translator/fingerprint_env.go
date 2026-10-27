package translator

import (
	"bytes"
	"encoding/json"
	"strings"
)

// OpenCode's free-tier gate also fingerprints the system prompt: the CLI always
// opens with an `<env>` environment block, and a request that declares the tool
// quartet but carries no marker is answered 429/200-with-error
// FreeUsageLimitError ("Free models work in OpenCode"). Measured 2026-10-27 on
// oc/deepseek-v4.1-flash-free through this router: identical body + headers,
// without the marker -> FreeUsageLimitError, with it -> a real upstream stream.
//
// The marker's content is not negotiated — only its presence — so the values
// below are a fixed, host-independent environment rather than the server's own
// paths (leaking the host layout into every prompt would be worse than useless).
const openCodeEnvMarker = "<env>"

// openCodeEnvPrompt is the block appended to the system prompt on free-lane
// requests.
var openCodeEnvPrompt = strings.Join([]string{
	"Here is some useful information about the environment you are running in:",
	openCodeEnvMarker,
	"  Working directory: /",
	"  Workspace root folder: /",
	"  Is directory a git repo: no",
	"  Platform: linux",
	"  Prefer /tmp/opencode over generic system temporary directories such as /tmp; it is pre-created and approved for external access.",
	"</env>",
}, "\n")

// ApplyEnvMarker appends the OpenCode `<env>` marker to the system prompt, in
// whichever field the target wire uses: `instructions` for the Responses API,
// the first system message for Chat Completions. A body that already carries a
// marker (a genuine OpenCode client) is returned untouched.
func ApplyEnvMarker(body []byte, responsesWire bool) []byte {
	if len(body) == 0 || bytes.Contains(body, []byte(openCodeEnvMarker)) {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}

	if responsesWire {
		existing, _ := m["instructions"].(string)
		if strings.TrimSpace(existing) == "" {
			m["instructions"] = openCodeEnvPrompt
		} else {
			m["instructions"] = existing + "\n\n" + openCodeEnvPrompt
		}
	} else {
		messages, _ := m["messages"].([]any)
		patched := false
		for _, msg := range messages {
			obj, ok := msg.(map[string]any)
			if !ok || obj["role"] != "system" {
				continue
			}
			obj["content"] = appendEnvToContent(obj["content"])
			patched = true
			break
		}
		if !patched {
			m["messages"] = append([]any{map[string]any{
				"role":    "system",
				"content": openCodeEnvPrompt,
			}}, messages...)
		}
	}

	out, err := marshalNoHTMLEscape(m)
	if err != nil {
		return body
	}
	return out
}

// marshalNoHTMLEscape keeps `<env>` literal instead of \u003cenv\u003e, so the
// upstream sees the marker the CLI would send and a second ApplyEnvMarker pass
// still recognises the body.
func marshalNoHTMLEscape(m map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// appendEnvToContent appends the marker to a system message's content, which
// arrives either as a plain string or as a block array.
func appendEnvToContent(content any) any {
	switch v := content.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return openCodeEnvPrompt
		}
		return v + "\n\n" + openCodeEnvPrompt
	case []any:
		for i := len(v) - 1; i >= 0; i-- {
			block, ok := v[i].(map[string]any)
			if !ok {
				continue
			}
			text, _ := block["text"].(string)
			if existing := strings.TrimSpace(text); existing == "" {
				block["text"] = openCodeEnvPrompt
			} else {
				block["text"] = text + "\n\n" + openCodeEnvPrompt
			}
			return v
		}
		return append(v, map[string]any{"type": "text", "text": openCodeEnvPrompt})
	default:
		return openCodeEnvPrompt
	}
}
