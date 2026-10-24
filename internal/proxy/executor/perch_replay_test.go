package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// The replayed history of a tool-calling turn must keep enough detail for the
// model to continue: which tool, with which arguments, and which result belongs
// to which call. Synthesizing "I will use the tool: bash, bash, bash" and
// flattening results to a bare "[Tool Result]" drops all of it, so a harness
// gets a silent/empty continuation after running tools.
func TestPerchForwardKeepsToolCallArgumentsInReplay(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"text":"ANSWER","model":"m","finishReason":"stop"}`)
	}))
	defer srv.Close()

	cfg := providers.KnownProviders["perch"]
	cfg.BaseURL = srv.URL
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx: context.Background(), Client: http.DefaultClient, Config: &cfg, APIKey: "tok",
		Body: []byte(`{"model":"deepseek-flash-starter","messages":[
			{"role":"system","content":"System prompt"},
			{"role":"user","content":"jalankan node -v dan npm root -g"},
			{"role":"assistant","content":"","tool_calls":[
				{"id":"call_a","type":"function","function":{"name":"bash","arguments":"{\"command\":\"node -v\"}"}},
				{"id":"call_b","type":"function","function":{"name":"bash","arguments":"{\"command\":\"npm root -g\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_a","name":"bash","content":"v22.11.0"},
			{"role":"tool","tool_call_id":"call_b","name":"bash","content":"/usr/lib/node_modules"}
		]}`),
	}
	if err := ForwardPerch(rec, req); err != nil {
		t.Fatalf("ForwardPerch: %v", err)
	}

	msgs, ok := got["request"].(map[string]any)["messages"].([]any)
	if !ok {
		t.Fatalf("expected messages array: %v", got)
	}
	blob, _ := json.Marshal(msgs)
	joined := string(blob)

	for _, want := range []string{"node -v", "npm root -g", "call_a", "call_b"} {
		if !strings.Contains(joined, want) {
			t.Errorf("replayed history lost %q — model cannot continue:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "/usr/lib/node_modules") != 1 {
		t.Errorf("second tool result missing: %s", joined)
	}
}

// Perch may deliver each call in its own event, with the toolCalls array
// restarting at index 0. Keying per-call stream state by array position then
// makes the second call look like a repeat of the first, its declaration is
// suppressed, and the harness waits forever for a tool result nobody asked for.
func TestPerchStreamSeparateEventsPerCall(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"tool_call_delta","toolCalls":[{"id":"call_a","name":"bash","rawArguments":"{\"command\":\"node -v\"}"}]}`,
		`data: {"type":"tool_use_end","toolCalls":[{"id":"call_a","name":"bash","rawArguments":"{\"command\":\"node -v\"}","sealed":true}]}`,
		`data: {"type":"tool_call_delta","toolCalls":[{"id":"call_b","name":"bash","rawArguments":"{\"command\":\"npm root -g\"}"}]}`,
		`data: {"type":"tool_use_end","toolCalls":[{"id":"call_b","name":"bash","rawArguments":"{\"command\":\"npm root -g\"}","sealed":true}]}`,
		`data: {"type":"done","ok":true,"finishReason":"tool_calls"}`,
		"",
	}, "\n")

	rec := httptest.NewRecorder()
	if err := perchStream(rec, strings.NewReader(sse), nil, "m"); err != nil {
		t.Fatalf("perchStream: %v", err)
	}
	out := rec.Body.String()
	for _, want := range []string{"call_a", "call_b", "node -v", "npm root -g"} {
		if !strings.Contains(out, want) {
			t.Errorf("stream lost %q — client sees a truncated tool list:\n%s", want, out)
		}
	}
	if strings.Count(out, `"index":1`) != 1 {
		t.Errorf("second call must be declared once as index 1:\n%s", out)
	}
}
