package executor

import (
	"regexp"
	"testing"

	json "encoding/json/v2"
)

// Anthropic requires tool_use ids to match ^[a-zA-Z0-9_-]+$.
var claudeToolIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ensureMessagesMaxTokens must rewrite tool ids that other upstreams (e.g.
// Gemini history translated to OpenAI format) synthesized with characters
// Anthropic rejects. tool_use and tool_result blocks must stay paired.
func TestEnsureClaudeMessages_SanitizesInvalidToolIDs(t *testing.T) {
	body := []byte(`{
		"model": "cc/claude-opus-5",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "run it"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_bad.id/with chars", "type": "function",
				 "function": {"name": "run", "arguments": "{\"x\":1}"}},
				{"id": "call_ok-123", "type": "function",
				 "function": {"name": "check", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_bad.id/with chars", "content": "result-1"},
			{"role": "tool", "tool_call_id": "call_ok-123", "content": "result-2"}
		]
	}`)

	out := EnsureClaudeMessages(body, "claude-opus-5")

	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Collect tool_use and tool_result ids.
	ids := map[string][]string{} // kind -> ids
	for _, msg := range req.Messages {
		blocks, ok := msg.Content.([]any)
		if !ok {
			continue
		}
		for _, b := range blocks {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			switch bm["type"] {
			case "tool_use":
				ids["use"] = append(ids["use"], bm["id"].(string))
			case "tool_result":
				ids["result"] = append(ids["result"], bm["tool_use_id"].(string))
			}
		}
	}

	if len(ids["use"]) != 2 || len(ids["result"]) != 2 {
		t.Fatalf("expected 2 tool_use + 2 tool_result ids, got %v", ids)
	}

	// Every id must be valid for Anthropic.
	for kind, list := range ids {
		for _, id := range list {
			if !claudeToolIDPattern.MatchString(id) {
				t.Errorf("%s id %q does not match Anthropic pattern", kind, id)
			}
		}
	}

	// Pairing must be preserved: tool_result ids match the tool_use ids,
	// and valid ids pass through untouched.
	if ids["use"][0] != ids["result"][0] {
		t.Errorf("invalid id not paired: tool_use=%q tool_result=%q", ids["use"][0], ids["result"][0])
	}
	if ids["use"][0] == "call_bad.id/with chars" {
		t.Errorf("invalid id was not rewritten")
	}
	if ids["use"][1] != "call_ok-123" || ids["result"][1] != "call_ok-123" {
		t.Errorf("valid id should pass through unchanged: use=%q result=%q", ids["use"][1], ids["result"][1])
	}

	// Deterministic: same input id must produce the same replacement id.
	// (Full-body byte equality isn't asserted because encoding/json/v2 may
	// vary map key order — semantically irrelevant for JSON.)
	out2 := EnsureClaudeMessages(body, "claude-opus-5")
	idOf := func(raw []byte) map[string][]string {
		var req2 struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(raw, &req2); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got := map[string][]string{}
		for _, msg := range req2.Messages {
			blocks, ok := msg.Content.([]any)
			if !ok {
				continue
			}
			for _, b := range blocks {
				bm, ok := b.(map[string]any)
				if !ok {
					continue
				}
				switch bm["type"] {
				case "tool_use":
					got["use"] = append(got["use"], bm["id"].(string))
				case "tool_result":
					got["result"] = append(got["result"], bm["tool_use_id"].(string))
				}
			}
		}
		return got
	}
	ids2 := idOf(out2)
	for kind, list := range ids {
		for i, id := range list {
			if ids2[kind][i] != id {
				t.Errorf("%s id %d not deterministic: %q vs %q", kind, i, id, ids2[kind][i])
			}
		}
	}
}

// A tool call whose id is missing entirely must still reach Anthropic with a
// valid id. Forwarding an empty string makes the upstream reject the entire
// request — 400 "messages.N.content.N.tool_use.id: Field required" — instead of
// merely dropping the tool call. History loses ids when another upstream trims
// a tool_calls entry or the client omits the field.
func TestEnsureClaudeMessages_FillsMissingToolIDs(t *testing.T) {
	body := []byte(`{
		"model": "cc/claude-opus-4-6-thinking",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "run it"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"type": "function", "function": {"name": "run", "arguments": "{\"x\":1}"}}
			]},
			{"role": "tool", "tool_call_id": "", "content": "result-1"}
		]
	}`)

	out := ensureMessagesMaxTokens(body, "cc/claude-opus-4-6-thinking")

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	msgs, _ := parsed["messages"].([]any)

	var useID, resultID string
	for _, raw := range msgs {
		msg, _ := raw.(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			bm, _ := b.(map[string]any)
			switch bm["type"] {
			case "tool_use":
				useID, _ = bm["id"].(string)
			case "tool_result":
				resultID, _ = bm["tool_use_id"].(string)
			}
		}
	}

	if useID == "" {
		t.Fatal("tool_use.id is empty; the upstream rejects the whole request with 'Field required'")
	}
	if !claudeToolIDPattern.MatchString(useID) {
		t.Errorf("synthesized tool_use.id %q does not match the Anthropic pattern", useID)
	}
	// The pair must still reference each other, or the conversation is invalid.
	if resultID != useID {
		t.Errorf("tool_result.tool_use_id = %q, want it to match tool_use.id %q", resultID, useID)
	}
}
