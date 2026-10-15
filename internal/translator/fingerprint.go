package translator

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"strings"
)

// Tool-name handling for the OpenCode Zen free-tier gate.
//
// The upstream gate fingerprints its official client through the case of the
// file-search tool quartet (bash, glob, grep, read) carried in the request body.
// Claude Code CLI sends the same built-in tools capitalised
// (Bash, Glob, Grep, Read), so the fingerprint never matches and the gate
// rejects every request coming from that harness.
//
// Measured directly against the upstream (2026-09-18):
//
//	capitalised only        -> HTTP 403 FreeTierError
//	capitalised + lowercase -> HTTP 500 server_error (upstream sees duplicates)
//	lowercase only          -> HTTP 200
//
// A case-variant therefore has to be RENAMED to the canonical lowercase form and
// not added alongside it: appending "bash" next to the caller's "Bash" yields two
// tools sharing a name and turns the 403 into a 500.
//
// Renaming alone satisfies the gate, but the client must still receive the tool
// name it declared itself, so the helpers below also restore the original
// spelling on the response.
//
// Ported from the equivalent fix in decolua/9router (opencodeFingerprint.js).

// OpenCodeFingerprintTools holds the canonical names the upstream gate looks for.
//
// The gate needs at least TWO declarations present, and `read` plus `shell` is
// the pair the official client sends: measured 2026-09-20, a single tool (
// `read` alone) still returned 403 while `read`+`shell` returned 200. `bash`,
// `glob` and `grep` are kept because they are what an older gate revision
// fingerprinted and clients send them capitalised, so the rename path still
// matters.
var OpenCodeFingerprintTools = []string{"read", "shell", "bash", "glob", "grep"}

// openCodeInjectedTools is the set that may be synthesised when the client sent
// nothing, or too few, to satisfy the gate.
//
// The measured rule (2026-10-15, ~60 trials against the live endpoint, all with
// identical headers) is:
//
//   - the tools array must hold at least five declarations, and
//   - every declaration needs a non-empty function.parameters.properties.
//
// Only the name and the presence of a populated formal schema are inspected.
// The description is not compared: swapping it for "DIUBAH" still returns 200,
// and five arbitrary names with a populated schema also return 200. The failure
// modes are quantitative — four tools, or five tools whose schemas are empty,
// both answer 403 FreeTierError.
var openCodeInjectedTools = []string{"read", "shell", "bash", "glob", "grep"}

// FingerprintToolKey returns the canonical lowercase name when name is a quartet
// member, or "" when it is not.
func FingerprintToolKey(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, t := range OpenCodeFingerprintTools {
		if lower == t {
			return t
		}
	}
	return ""
}

// toolNameOf reads a tool name from either shape: "name" at the top level
// (Responses shape) or nested under "function" (Chat Completions shape).
func toolNameOf(tool any) string {
	m, ok := tool.(map[string]any)
	if !ok {
		return ""
	}
	if n, ok := m["name"].(string); ok && strings.TrimSpace(n) != "" {
		return strings.TrimSpace(n)
	}
	if fn, ok := m["function"].(map[string]any); ok {
		if n, ok := fn["name"].(string); ok {
			return strings.TrimSpace(n)
		}
	}
	return ""
}

// setToolName writes name back into whichever shape the tool uses.
func setToolName(tool any, name string) {
	m, ok := tool.(map[string]any)
	if !ok {
		return
	}
	if _, hasTop := m["name"]; hasTop {
		m["name"] = name
		return
	}
	if fn, ok := m["function"].(map[string]any); ok {
		fn["name"] = name
	}
}

// ConcealFingerprintTools renames quartet case-variants to the canonical
// lowercase form, drops duplicate names (the upstream rejects a body carrying two
// tools with the same name), and appends the missing members as no-op
// declarations.
//
// responsesWire selects the declaration shape: true puts "name" at the top level
// (the Responses API), false wraps it in "function" (Chat Completions). The
// caller knows which endpoint it is about to hit; inferring it from the tools
// already present meant an empty tools array decided nothing and produced the
// wrong shape, which the chat endpoint rejected with "tools.0.function Field
// required" and the Responses endpoint with "tools[0] missing required field
// name".
//
// It returns the resulting body together with a map of sent-name -> original-name
// for the response side. When nothing needed to change, the original body is
// returned unchanged with an empty map.
func ConcealFingerprintTools(body []byte, responsesWire bool) ([]byte, map[string]string) {
	noop := map[string]string{}
	if len(body) == 0 {
		return body, noop
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, noop
	}

	// A body with no tools array still has to receive the quartet: the gate
	// rejects tool-less requests too (measured 403, 2026-09-18).
	var tools []any
	if raw, ok := m["tools"].([]any); ok {
		tools = raw
	}

	toolNameMap := map[string]string{}
	seen := map[string]bool{}
	kept := make([]any, 0, len(tools)+len(OpenCodeFingerprintTools))

	for _, tool := range tools {
		current := toolNameOf(tool)
		if current == "" {
			kept = append(kept, tool)
			continue
		}
		key := FingerprintToolKey(current)
		finalName := current
		if key != "" {
			finalName = key
		}
		dedupeKey := strings.ToLower(finalName)
		if seen[dedupeKey] {
			continue
		}
		seen[dedupeKey] = true
		if key != "" && key != current {
			toolNameMap[key] = current
			setToolName(tool, key)
		}
		kept = append(kept, tool)
	}

	// Every declaration the gate inspects needs a populated
	// function.parameters.properties. A client that sends its own five tools
	// with `parameters: {}` is rejected (measured 403), so repair them in
	// place rather than trusting the caller's schema.
	for _, tool := range kept {
		if obj, ok := tool.(map[string]any); ok {
			ensurePopulatedSchema(obj)
		}
	}

	// Injected declarations take the shape the target endpoint expects; the
	// caller states which one it is (see the responsesWire parameter).
	flat := responsesWire

	for _, name := range openCodeInjectedTools {
		if seen[name] {
			continue
		}
		// Every declaration the gate sees must carry a populated formal
		// schema, so the synthesised entry does too; see openCodeInjectedTools
		// for the measured threshold.
		def := openCodeFingerprint[name]
		// Shape matters as much as content: the nested form carries exactly
		// two keys, `type` and `function`. Hoisting description/parameters to
		// the top level as well (which an earlier revision of this function
		// did) is not equivalent.
		var entry map[string]any
		if flat {
			entry = map[string]any{
				"type":        "function",
				"name":        name,
				"description": def.Description,
				"parameters":  def.Parameters,
			}
		} else {
			entry = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        name,
					"description": def.Description,
					"parameters":  def.Parameters,
				},
			}
		}
		kept = append(kept, entry)
		seen[name] = true
	}
	m["tools"] = kept

	// Point an explicit tool_choice at the renamed tool.
	if choice, ok := m["tool_choice"].(map[string]any); ok {
		if n, ok := choice["name"].(string); ok {
			if key := FingerprintToolKey(n); key != "" && toolNameMap[key] != "" {
				choice["name"] = key
			}
		}
	}

	// Force streaming. The gate rejects a non-streaming request outright (403
	// FreeTierError) even when the tools and headers are otherwise perfect:
	// measured 2026-09-20, `tools + non-stream` -> 403 while `tools + stream` ->
	// 200 on the same body otherwise. The caller is told the truth by the
	// response writer, which converts the upstream SSE back into a single JSON
	// body for a client that did not ask to stream.
	m["stream"] = true

	out, err := json.Marshal(m)
	if err != nil {
		return body, noop
	}
	return out, toolNameMap
}

// RestoreToolNames restores the caller's original tool names on a JSON response
// payload. It covers Claude content_block_start (tool_use), Chat Completions
// choices[].{delta,message}.tool_calls[].function.name, and Responses output[]
// items of type function_call / custom_tool_call.
func RestoreToolNames(payload []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 || len(payload) == 0 {
		return payload
	}

	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return payload
	}

	changed := false

	// Claude: content_block_start with content_block.type == "tool_use"
	if block, ok := m["content_block"].(map[string]any); ok && block["type"] == "tool_use" {
		if n, ok := block["name"].(string); ok {
			if orig, found := toolNameMap[n]; found {
				block["name"] = orig
				changed = true
			}
		}
	}

	// OpenAI: choices[].delta.tool_calls[] and choices[].message.tool_calls[]
	if choices, ok := m["choices"].([]any); ok {
		for _, c := range choices {
			choice, ok := c.(map[string]any)
			if !ok {
				continue
			}
			for _, holder := range []string{"delta", "message"} {
				h, ok := choice[holder].(map[string]any)
				if !ok {
					continue
				}
				calls, ok := h["tool_calls"].([]any)
				if !ok {
					continue
				}
				for _, call := range calls {
					cm, ok := call.(map[string]any)
					if !ok {
						continue
					}
					fn, ok := cm["function"].(map[string]any)
					if !ok {
						continue
					}
					n, ok := fn["name"].(string)
					if !ok || n == "" {
						continue
					}
					if orig, found := toolNameMap[n]; found {
						fn["name"] = orig
						changed = true
					}
				}
			}
		}
	}

	// Responses: output[] items of type function_call / custom_tool_call
	if output, ok := m["output"].([]any); ok {
		for _, item := range output {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			t, _ := im["type"].(string)
			if t != "function_call" && t != "custom_tool_call" {
				continue
			}
			if n, ok := im["name"].(string); ok {
				if orig, found := toolNameMap[n]; found {
					im["name"] = orig
					changed = true
				}
			}
		}
	}

	if !changed {
		return payload
	}
	out, err := json.Marshal(m)
	if err != nil {
		return payload
	}
	return out
}

// RestoreToolNamesInSSE applies RestoreToolNames to an SSE frame while keeping
// the "data: " prefix and the original line framing intact. Lines that are not
// JSON data (for example "event: ..." or "[DONE]") pass through untouched.
func RestoreToolNamesInSSE(chunk []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 || len(chunk) == 0 {
		return chunk
	}
	if !bytes.Contains(chunk, []byte("data:")) {
		return chunk
	}

	var out bytes.Buffer
	out.Grow(len(chunk))
	remaining := chunk
	changedAny := false

	for len(remaining) > 0 {
		nl := bytes.IndexByte(remaining, '\n')
		var line []byte
		if nl < 0 {
			line = remaining
			remaining = nil
		} else {
			line = remaining[:nl+1]
			remaining = remaining[nl+1:]
		}

		body := bytes.TrimRight(line, "\r\n")
		if bytes.HasPrefix(body, []byte("data:")) {
			jsonPart := bytes.TrimSpace(body[len("data:"):])
			if len(jsonPart) > 0 && !bytes.Equal(jsonPart, []byte("[DONE]")) {
				restored := RestoreToolNames(jsonPart, toolNameMap)
				if !bytes.Equal(restored, jsonPart) {
					out.WriteString("data: ")
					out.Write(restored)
					out.WriteByte('\n')
					changedAny = true
					continue
				}
			}
		}
		out.Write(line)
	}

	if !changedAny {
		return chunk
	}
	return out.Bytes()
}

// RestoreToolNamesInPayload picks the right shape: an SSE frame when the chunk
// carries "data:" lines, otherwise a single JSON body for a non-streaming
// response.
func RestoreToolNamesInPayload(chunk []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 || len(chunk) == 0 {
		return chunk
	}
	if bytes.Contains(chunk, []byte("data:")) {
		return RestoreToolNamesInSSE(chunk, toolNameMap)
	}
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return chunk
	}
	return RestoreToolNames(trimmed, toolNameMap)
}

// contextKeyToolNameMap is the context key holding the rename map. Response paths
// only receive a context (not the Request), so the map travels there.
type contextKeyToolNameMap struct{}

// WithToolNameMap stores the renamed-tool map on the context.
func WithToolNameMap(ctx context.Context, toolNameMap map[string]string) context.Context {
	if len(toolNameMap) == 0 {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKeyToolNameMap{}, toolNameMap)
}

// ToolNameMapFromContext reads back the map stored by WithToolNameMap.
func ToolNameMapFromContext(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(contextKeyToolNameMap{}).(map[string]string)
	return m
}
