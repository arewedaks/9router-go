package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// The upstream free-tier gate compares the `read` and `shell` declarations
// against OpenCode's own, so these two properties are load-bearing:
//
//   - the declarations carry the real description and parameter schema, and
//   - no other synthesised tool is present, because a placeholder with an empty
//     schema flips a passing request to 403 FreeTierError.
//
// Both were violated before this test existed.
func TestFingerprintToolDeclarations(t *testing.T) {
	out, _ := ConcealFingerprintTools(
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), false)

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["stream"] != true {
		t.Error("stream must be forced true: the gate rejects a non-streaming request")
	}

	tools, _ := m["tools"].([]any)
	// The gate needs at least five declarations; anything fewer is rejected
	// regardless of how faithful the ones present are (measured 2026-10-15).
	if len(tools) < 5 {
		names := make([]string, 0, len(tools))
		for _, tl := range tools {
			e, _ := tl.(map[string]any)
			fn, _ := e["function"].(map[string]any)
			n, _ := fn["name"].(string)
			names = append(names, n)
		}
		t.Fatalf("want at least 5 tools, got %d: %v", len(tools), names)
	}

	for _, tl := range tools {
		entry, _ := tl.(map[string]any)
		if len(entry) != 2 {
			t.Errorf("entry has %d keys, want 2 (type, function): %v", len(entry), entry)
		}
		fn, _ := entry["function"].(map[string]any)
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)

		want := openCodeFingerprint[name]
		if want.Description == "" {
			t.Fatalf("unexpected tool %q", name)
		}
		if desc != want.Description {
			t.Errorf("%s description differs from the verbatim copy", name)
		}
		props, _ := params["properties"].(map[string]any)
		if len(props) == 0 {
			t.Errorf("%s has an empty schema; the gate rejects empty properties", name)
		}
	}
}

// A client that sends its own `bash` tool must keep it: those names are renamed
// for routing but never synthesised, and are never dropped.
func TestFingerprintKeepsClientTools(t *testing.T) {
	client := `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[
		{"type":"function","function":{"name":"Bash","description":"my shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}},
		{"type":"function","function":{"name":"my_tool","description":"mine","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}}]}`
	out, toolNameMap := ConcealFingerprintTools([]byte(client), false)

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var names []string
	for _, tl := range m["tools"].([]any) {
		e, _ := tl.(map[string]any)
		fn, _ := e["function"].(map[string]any)
		n, _ := fn["name"].(string)
		names = append(names, n)
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"bash", "my_tool", "read", "shell"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, names)
		}
	}
	if toolNameMap["bash"] != "Bash" {
		t.Errorf("case-variant must be restorable, got %q", toolNameMap["bash"])
	}
}

// TestFingerprintRepairsEmptySchemas covers the measured failure mode where the
// tool list is long enough but every declaration carries `parameters: {}` or no
// parameters at all. The gate answers 403 FreeTierError for those, so they are
// repaired rather than trusted.
func TestFingerprintRepairsEmptySchemas(t *testing.T) {
	client := `{"model":"m","messages":[{"role":"user","content":"hi"}],
		"tools":[
		{"type":"function","function":{"name":"bash","description":"d","parameters":{}}},
		{"type":"function","function":{"name":"edit","description":"d","parameters":{"type":"object","properties":{}}}},
		{"type":"function","function":{"name":"glob","description":"d"}},
		{"type":"function","function":{"name":"grep","description":"d","parameters":{"type":"object","properties":{}}}},
		{"type":"function","function":{"name":"webfetch","description":"d"}}]}`
	out, _ := ConcealFingerprintTools([]byte(client), false)

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools, _ := m["tools"].([]any)
	if len(tools) < 5 {
		t.Fatalf("want at least 5 tools, got %d", len(tools))
	}
	for _, tl := range tools {
		entry, _ := tl.(map[string]any)
		fn, _ := entry["function"].(map[string]any)
		name, _ := fn["name"].(string)
		params, _ := fn["parameters"].(map[string]any)
		props, _ := params["properties"].(map[string]any)
		if len(props) == 0 {
			t.Errorf("%s still has an empty schema after concealment", name)
		}
	}
}
