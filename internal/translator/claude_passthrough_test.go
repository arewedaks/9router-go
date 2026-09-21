package translator

import (
	"encoding/json"
	"regexp"
	"testing"
)

// toolUseIDPattern is what Anthropic accepts for tool_use.id.
var toolUseIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// decodePassthrough runs the sanitizer and returns the decoded messages.
func decodePassthrough(t *testing.T, body string) []any {
	t.Helper()
	out := SanitizeClaudePassthrough([]byte(body))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, out)
	}
	msgs, _ := m["messages"].([]any)
	return msgs
}

// collectToolBlocks returns the tool_use ids and tool_result ids in order.
func collectToolBlocks(msgs []any) (useIDs, resultIDs []string) {
	for _, raw := range msgs {
		msg, _ := raw.(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			bm, _ := b.(map[string]any)
			switch bm["type"] {
			case "tool_use":
				id, _ := bm["id"].(string)
				useIDs = append(useIDs, id)
			case "tool_result":
				id, _ := bm["tool_use_id"].(string)
				resultIDs = append(resultIDs, id)
			}
		}
	}
	return useIDs, resultIDs
}

// A Claude-native client can send history whose tool_use block has no id. The
// Anthropic API REQUIRES tool_use.id, so one missing id rejects the whole
// request with 400 "messages.N.content.N.tool_use.id: Field required".
//
// This is the passthrough path, distinct from the OpenAI->Claude conversion in
// the executor: the body already looks like Anthropic, so it is forwarded as-is
// and never passes through sanitizeToolUseID.
func TestSanitizeClaudePassthrough_FillsMissingToolUseID(t *testing.T) {
	msgs := decodePassthrough(t, `{
		"model": "claude-opus-4-6-thinking",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "checking"},
				{"type": "tool_use", "name": "search", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "content": "ok"}
			]}
		]
	}`)

	useIDs, resultIDs := collectToolBlocks(msgs)
	if len(useIDs) != 1 {
		t.Fatalf("expected one tool_use block, got %d", len(useIDs))
	}
	if useIDs[0] == "" {
		t.Fatal("tool_use.id is still empty; the upstream rejects the whole request")
	}
	// Anthropic validates tool_use.id against ^[a-zA-Z0-9_-]+$. Note this is NOT
	// the stricter srvtoolu_ pattern, which applies only to server_tool_use.
	if !toolUseIDPattern.MatchString(useIDs[0]) {
		t.Errorf("synthesized id %q is not a valid Anthropic tool id", useIDs[0])
	}
	// Repairing only the tool_use would leave the tool_result pointing at
	// nothing, trading one 400 for another.
	if len(resultIDs) != 1 || resultIDs[0] != useIDs[0] {
		t.Errorf("tool_result ids = %v, want [%q] to match the tool_use", resultIDs, useIDs[0])
	}
}

// An intact history must pass through untouched. The sanitizer runs on every
// Claude request, so rewriting valid ids would corrupt conversations that were
// already correct.
func TestSanitizeClaudePassthrough_LeavesValidIDsAlone(t *testing.T) {
	msgs := decodePassthrough(t, `{
		"model": "claude-opus-4",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "toolu_01ABCdef", "name": "search", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_01ABCdef", "content": "ok"}
			]}
		]
	}`)

	useIDs, resultIDs := collectToolBlocks(msgs)
	if len(useIDs) != 1 || useIDs[0] != "toolu_01ABCdef" {
		t.Errorf("tool_use ids = %v, want the original id preserved", useIDs)
	}
	if len(resultIDs) != 1 || resultIDs[0] != "toolu_01ABCdef" {
		t.Errorf("tool_result ids = %v, want the original id preserved", resultIDs)
	}
}

// A text-only history has no tool blocks and must not gain any. In particular
// the synthesizer must not insert an id into unrelated blocks.
func TestSanitizeClaudePassthrough_DoesNotInventIDsWithoutToolUse(t *testing.T) {
	msgs := decodePassthrough(t, `{
		"model": "claude-opus-4",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": [{"type": "text", "text": "hi there"}]}
		]
	}`)

	useIDs, resultIDs := collectToolBlocks(msgs)
	if len(useIDs) != 0 || len(resultIDs) != 0 {
		t.Errorf("unexpected tool blocks created: use=%v result=%v", useIDs, resultIDs)
	}
	if len(msgs) != 2 {
		t.Errorf("message count changed: got %d, want 2", len(msgs))
	}
}
