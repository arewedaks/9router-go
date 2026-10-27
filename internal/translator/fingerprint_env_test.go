package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

// The free gate only checks that the marker is present, so every body shape a
// caller can produce must end up carrying it: no system message, a string
// system message, block content, and the Responses `instructions` field.
func TestApplyEnvMarker(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		flat  bool
		check func(*testing.T, map[string]any)
	}{
		{
			name: "chat: no system message gets one prepended",
			body: `{"model":"x","messages":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, m map[string]any) {
				msgs := m["messages"].([]any)
				if len(msgs) != 2 {
					t.Fatalf("messages = %d, want 2", len(msgs))
				}
				first := msgs[0].(map[string]any)
				if first["role"] != "system" || !strings.Contains(first["content"].(string), "<env>") {
					t.Fatalf("first message = %+v, want a system env marker", first)
				}
			},
		},
		{
			name: "chat: existing string system is appended, not replaced",
			body: `{"messages":[{"role":"system","content":"You are terse."},{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, m map[string]any) {
				content := m["messages"].([]any)[0].(map[string]any)["content"].(string)
				if !strings.HasPrefix(content, "You are terse.") || !strings.Contains(content, "<env>") {
					t.Fatalf("content = %q, want the caller prompt kept and the marker appended", content)
				}
			},
		},
		{
			name: "chat: block content gets the marker in the last text block",
			body: `{"messages":[{"role":"system","content":[{"type":"text","text":"Be brief."}]},{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, m map[string]any) {
				blocks := m["messages"].([]any)[0].(map[string]any)["content"].([]any)
				text := blocks[len(blocks)-1].(map[string]any)["text"].(string)
				if !strings.Contains(text, "Be brief.") || !strings.Contains(text, "<env>") {
					t.Fatalf("text = %q, want caller text plus marker", text)
				}
			},
		},
		{
			name: "responses: instructions is created then appended to",
			body: `{"model":"x","input":[]}`,
			flat: true,
			check: func(t *testing.T, m map[string]any) {
				if instr, _ := m["instructions"].(string); !strings.Contains(instr, "<env>") {
					t.Fatalf("instructions = %q, want marker", instr)
				}
			},
		},
		{
			name: "responses: existing instructions kept",
			body: `{"instructions":"You are a code reviewer.","input":[]}`,
			flat: true,
			check: func(t *testing.T, m map[string]any) {
				instr := m["instructions"].(string)
				if !strings.HasPrefix(instr, "You are a code reviewer.") || !strings.Contains(instr, "<env>") {
					t.Fatalf("instructions = %q", instr)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ApplyEnvMarker([]byte(tc.body), tc.flat)
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			tc.check(t, m)
		})
	}
}

// A genuine OpenCode client already sends the marker; it must pass through
// byte-identical, and a second apply must not duplicate the block.
func TestApplyEnvMarkerIdempotent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"hi"},{"role":"user","content":"x"}]}`)
	once := ApplyEnvMarker(body, false)
	twice := ApplyEnvMarker(once, false)
	if string(once) != string(twice) {
		t.Fatalf("second apply changed the body:\n%s\n%s", once, twice)
	}
	if n := strings.Count(string(once), "<env>"); n != 1 {
		t.Errorf("marker count = %d, want 1", n)
	}
	garbage := []byte("not json")
	if out := ApplyEnvMarker(garbage, false); string(out) != string(garbage) {
		t.Errorf("non-JSON body was rewritten: %s", out)
	}
}
