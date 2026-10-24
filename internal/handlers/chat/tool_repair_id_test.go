package chat

import (
	"encoding/json"
	"testing"
)

// toolIDs returns how many tool messages carry each tool_call_id.
func toolIDs(t *testing.T, body []byte) map[string]int {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids := map[string]int{}
	msgs, _ := m["messages"].([]any)
	for _, msg := range msgs {
		mm, ok := msg.(map[string]any)
		if !ok || mm["role"] != "tool" {
			continue
		}
		id, _ := mm["tool_call_id"].(string)
		ids[id]++
	}
	return ids
}

// TestRepairToolCallIDsNeverDuplicates pins the invariant strict upstreams
// enforce: "Each tool_call must have exactly one matching tool response". The
// repair used to hand a still-pending call id to a second tool message that
// arrived without an id, producing a duplicate the upstream rejected with 400 —
// on a body the caller had sent correctly.
func TestRepairToolCallIDsNeverDuplicates(t *testing.T) {
	cases := map[string]string{
		"one call, one id-less response": `{"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"X","type":"function","function":{"name":"t","arguments":"{}"}}]},
			{"role":"tool","content":"result"}]}`,
		"one call, id-less response then same id": `{"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"X","type":"function","function":{"name":"t","arguments":"{}"}}]},
			{"role":"tool","content":"result"},
			{"role":"tool","tool_call_id":"X","content":"result"}]}`,
		"two calls, two id-less responses": `{"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"A","type":"function","function":{"name":"t","arguments":"{}"}},
				{"id":"B","type":"function","function":{"name":"t","arguments":"{}"}}]},
			{"role":"tool","content":"a"},
			{"role":"tool","content":"b"}]}`,
	}
	for name, in := range cases {
		ids := toolIDs(t, repairToolCallIDsInJSON([]byte(in)))
		for id, n := range ids {
			if n > 1 {
				t.Errorf("%s: tool_call_id %q answered %d times, want at most 1", name, id, n)
			}
		}
		if name == "two calls, two id-less responses" && len(ids) != 2 {
			t.Errorf("%s: got %d distinct ids, want 2", name, len(ids))
		}
	}
}

// TestRepairToolCallIDsPairsMissingIDs keeps the original contract: an id-less
// tool response still gets the pending call's id, so a lenient upstream sees a
// complete pairing.
func TestRepairToolCallIDsPairsMissingIDs(t *testing.T) {
	in := `{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"X","type":"function","function":{"name":"t","arguments":"{}"}}]},
		{"role":"tool","content":"result"}]}`
	ids := toolIDs(t, repairToolCallIDsInJSON([]byte(in)))
	if ids["X"] != 1 {
		t.Errorf("id-less response was not paired with X: %v", ids)
	}
}
