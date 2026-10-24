package chat

import (
	json "encoding/json/v2"
	"fmt"
)

// repairToolCallIDsInMap ensures every role: "tool" message has a valid tool_call_id (PR #4090).
// Strict upstreams (NVIDIA, OpenAI, Antigravity) reject requests with HTTP 400 when tool_call_id is missing.
//
// It must never hand the same call id to two tool messages: upstreams pair a
// tool response with its call and reject a second one as "Duplicate tool
// response", which is how a body the harness sent with only one response per
// call came back 400. A pending id is therefore consumed when it is met in a
// well-formed response, and only ids that are still unanswered are handed out.
func repairToolCallIDsInMap(body map[string]any) {
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return
	}
	var pendingToolCallIDs []string
	assigned := map[string]bool{}
	toolSeq := 0
	for i, m := range msgs {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		if role == "assistant" {
			if tcs, ok := msgMap["tool_calls"].([]any); ok && len(tcs) > 0 {
				for k, tcRaw := range tcs {
					if tcMap, ok := tcRaw.(map[string]any); ok {
						id, _ := tcMap["id"].(string)
						if id == "" {
							id = fmt.Sprintf("call_%d_%d", toolSeq, k)
							tcMap["id"] = id
						}
						pendingToolCallIDs = append(pendingToolCallIDs, id)
					}
				}
				toolSeq++
			}
		} else if role == "tool" {
			id, _ := msgMap["tool_call_id"].(string)
			// An id already used by an earlier response is not this message's
			// call: leave it alone rather than fabricating a duplicate.
			if id != "" && !assigned[id] {
				assigned[id] = true
				for idx, p := range pendingToolCallIDs {
					if p == id {
						pendingToolCallIDs = append(pendingToolCallIDs[:idx], pendingToolCallIDs[idx+1:]...)
						break
					}
				}
				continue
			}
			if id != "" && assigned[id] {
				// Duplicate response for a call that was already answered. The
				// upstream rejects the whole request, so drop the redundant
				// message instead of forwarding it.
				msgs[i] = nil
				continue
			}
			if len(pendingToolCallIDs) > 0 {
				next := pendingToolCallIDs[0]
				pendingToolCallIDs = pendingToolCallIDs[1:]
				msgMap["tool_call_id"] = next
				assigned[next] = true
			} else {
				synth := fmt.Sprintf("call_tool_%d", i)
				msgMap["tool_call_id"] = synth
				assigned[synth] = true
			}
		}
	}
	// Strip the messages dropped above, preserving order.
	if len(msgs) > 0 {
		kept := msgs[:0]
		for _, m := range msgs {
			if m != nil {
				kept = append(kept, m)
			}
		}
		body["messages"] = kept
	}
}

// repairToolCallIDsInJSON repairs missing tool_call_id directly on raw JSON body if it contains messages.
func repairToolCallIDsInJSON(body []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	if _, ok := m["messages"]; ok {
		repairToolCallIDsInMap(m)
		if out, err := json.Marshal(m); err == nil {
			return out
		}
	}
	return body
}
