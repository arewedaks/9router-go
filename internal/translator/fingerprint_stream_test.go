package translator

import (
	json "encoding/json/v2"
	"testing"
)

// The Zen free-tier gate only serves streaming requests, so the fingerprint
// step forces stream:true on every body it touches.
//
// Measured against the upstream on 2026-09-20:
//
//	tools + non-stream -> HTTP 403 FreeTierError
//	tools + stream     -> HTTP 200
//
// Same headers, same tools, same everything else. A client that asked for one
// JSON object therefore still has to be sent as a stream, and the caller
// collapses the SSE back into a single body.
func TestConcealFingerprintToolsForcesStream(t *testing.T) {
	body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`)

	out, _ := ConcealFingerprintTools(body, false)

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stream, _ := m["stream"].(bool)
	if !stream {
		t.Error("stream was not forced to true; the gate rejects non-streaming requests")
	}
}

// An explicit stream:false must be overwritten, not merely filled in.
func TestConcealFingerprintToolsOverridesStreamFalse(t *testing.T) {
	body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],"stream":false}`)

	out, _ := ConcealFingerprintTools(body, false)

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if stream, _ := m["stream"].(bool); !stream {
		t.Error("stream:false survived; the request would be rejected with 403")
	}
}

// A body with no tools at all must receive Chat Completions shaped declarations.
//
// The shape used to be inferred from the tools already present, which meant an
// empty tools array decided nothing and left flat=true (the Responses shape).
// The chat endpoint then rejected the body with "5 validation errors:
// tools.0.function Field required".
func TestConcealFingerprintToolsDefaultsToChatShape(t *testing.T) {
	body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`)

	out, _ := ConcealFingerprintTools(body, false)

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools, _ := m["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools injected")
	}
	for i, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("tool %d is not an object", i)
		}
		if _, ok := tool["function"]; !ok {
			t.Errorf("tool %d has no \"function\" wrapper; chat completions requires it: %v", i, tool)
		}
	}
}

// The gate needs at least two declarations: `read` alone still returned 403
// while `read`+`shell` returned 200 at the time of measurement.
func TestConcealFingerprintToolsInjectsAtLeastTwo(t *testing.T) {
	body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`)

	out, _ := ConcealFingerprintTools(body, false)

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools, _ := m["tools"].([]any)
	if len(tools) < 2 {
		t.Errorf("only %d tools injected; the gate wants at least two", len(tools))
	}

	names := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		if n, ok := fn["name"].(string); ok {
			names[n] = true
		}
	}
	for _, want := range []string{"read", "shell"} {
		if !names[want] {
			t.Errorf("tool %q missing: %v", want, names)
		}
	}
}

// Caller-supplied tools must not be duplicated or dropped.
func TestConcealFingerprintToolsKeepsClientTools(t *testing.T) {
	body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"my_custom","description":"x","parameters":{"type":"object"}}}]}`)

	out, _ := ConcealFingerprintTools(body, false)

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools, _ := m["tools"].([]any)
	names := map[string]int{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		if n, ok := fn["name"].(string); ok {
			names[n]++
		}
	}
	if names["my_custom"] != 1 {
		t.Errorf("client tool kept %d times, want exactly 1: %v", names["my_custom"], names)
	}
	for n, c := range names {
		if c > 1 {
			t.Errorf("tool %q appears %d times; duplicates turn the 403 into a 500", n, c)
		}
	}
}
