package translator

import (
	"strings"
	"testing"
)

// TestSanitizeOpenAIToolsNames covers both places a function name is validated:
// the tools list and the replayed history. The API is stateless, so a harness
// resends the assistant tool_calls every turn — sanitising only the tools list
// left every second turn failing with the upstream's charset error.
func TestSanitizeOpenAIToolsNames(t *testing.T) {
	in := `{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":null,"tool_calls":[
			{"id":"c1","type":"function","function":{"name":"my tool","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"ok"}],
		"tools":[{"type":"function","function":{"name":"do_thing()","parameters":{"type":"object","properties":{}}}}]}`
	out, err := SanitizeOpenAITools([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`"name":"my_tool"`, `"name":"do_thing__"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, "my tool") || strings.Contains(s, "do_thing()") {
		t.Errorf("illegal name survived: %s", s)
	}
	// Ids and unrelated fields must be untouched.
	if !strings.Contains(s, `"tool_call_id":"c1"`) {
		t.Errorf("history pairing broken: %s", s)
	}
}

// TestSanitizeOpenAIToolsNoTools pins that a plain chat body is returned as-is
// (and that the schema pass no longer requires a tools list to run).
func TestSanitizeOpenAIToolsNoTools(t *testing.T) {
	in := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	out, err := SanitizeOpenAITools([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"content":"hi"`) {
		t.Errorf("body mangled: %s", out)
	}
}
