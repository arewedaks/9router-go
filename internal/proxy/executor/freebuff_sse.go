package executor

// Freebuff always streams upstream (the CLI sends `stream:!0` unconditionally),
// so a non-streaming caller must get the SSE collapsed back into one body. The
// CLI's own SDK does this aggregation client-side; this is the same job done on
// the proxy side.

import (
	"bufio"
	"bytes"
	json "encoding/json/v2"
	"io"
	"strings"
)

// freebuffAggregateSSE reads an OpenAI-style SSE stream and rebuilds the
// equivalent non-streaming chat.completion object.
//
// It keeps: the id/created/model of the first frame, the assistant's text and
// reasoning deltas, every tool call (reassembled by index, since arguments
// arrive as fragments), finish_reason, and the usage block when the stream
// carries one (the free lane always asks for include_usage).
func freebuffAggregateSSE(upstream io.Reader) ([]byte, error) {
	reader := bufio.NewReaderSize(upstream, 64*1024)

	type toolCall struct {
		Index int
		ID    string
		Type  string
		Name  string
		Args  strings.Builder
	}
	var (
		id, model, finishReason string
		created                 int64
		content, reasoning      strings.Builder
		tools                   []*toolCall
		toolByIndex             = map[int]*toolCall{}
		usage                   any
	)

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(line)
			if payload, ok := strings.CutPrefix(trimmed, "data:"); ok {
				payload = strings.TrimSpace(payload)
				if payload == "[DONE]" {
					break
				}
				if payload != "" {
					var frame map[string]any
					if json.Unmarshal([]byte(payload), &frame) == nil {
						if v, ok := frame["id"].(string); ok && id == "" {
							id = v
						}
						if v, ok := frame["model"].(string); ok && model == "" {
							model = v
						}
						if v, ok := frame["created"].(float64); ok && created == 0 {
							created = int64(v)
						}
						if u, ok := frame["usage"]; ok && u != nil {
							usage = u
						}
						if choices, ok := frame["choices"].([]any); ok && len(choices) > 0 {
							if choice, ok := choices[0].(map[string]any); ok {
								if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
									finishReason = fr
								}
								if delta, ok := choice["delta"].(map[string]any); ok {
									if s, ok := delta["content"].(string); ok {
										content.WriteString(s)
									}
									if s, ok := delta["reasoning_content"].(string); ok {
										reasoning.WriteString(s)
									}
									if raw, ok := delta["tool_calls"].([]any); ok {
										for _, rt := range raw {
											tcMap, ok := rt.(map[string]any)
											if !ok {
												continue
											}
											idx := 0
											if v, ok := tcMap["index"].(float64); ok {
												idx = int(v)
											}
											entry, seen := toolByIndex[idx]
											if !seen {
												entry = &toolCall{Index: idx}
												toolByIndex[idx] = entry
												tools = append(tools, entry)
											}
											if v, ok := tcMap["id"].(string); ok && v != "" {
												entry.ID = v
											}
											if v, ok := tcMap["type"].(string); ok && v != "" {
												entry.Type = v
											}
											if fn, ok := tcMap["function"].(map[string]any); ok {
												if v, ok := fn["name"].(string); ok && v != "" {
													entry.Name = v
												}
												if v, ok := fn["arguments"].(string); ok {
													entry.Args.WriteString(v)
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
	}

	message := map[string]any{"role": "assistant"}
	if content.Len() > 0 {
		message["content"] = content.String()
	} else if len(tools) == 0 {
		message["content"] = ""
	}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(tools) > 0 {
		out := make([]any, 0, len(tools))
		for _, t := range tools {
			tc := map[string]any{
				"id":   t.ID,
				"type": t.Type,
				"function": map[string]any{
					"name":      t.Name,
					"arguments": t.Args.String(),
				},
			}
			if tc["type"] == "" || tc["type"] == nil {
				tc["type"] = "function"
			}
			out = append(out, tc)
		}
		message["tool_calls"] = out
	}

	result := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
	}
	if usage != nil {
		result["usage"] = usage
	}
	if finishReason == "" {
		result["choices"].([]any)[0].(map[string]any)["finish_reason"] = "stop"
	}

	return json.Marshal(result)
}

// freebuffLooksLikeSSE reports whether the upstream body is an event stream. The
// free lane always streams, but a gateway error comes back as plain JSON and
// must not be fed to the aggregator.
func freebuffLooksLikeSSE(body []byte) bool {
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(head, []byte("data:"))
}
