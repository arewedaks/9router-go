package executor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// twinmindSSEUpstream drives a full round-trip against a mock TwinMind: the
// request must arrive as the app-shaped JSON with a flattened query, and the
// SSE reply must come back as OpenAI chunks (stream) or one completion (not).
func twinmindSSEUpstream(t *testing.T, sse string, check func(r *http.Request, body string)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1<<20)
		n, _ := r.Body.Read(buf)
		check(r, string(buf[:n]))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
}

func TestForwardTwinMindStreamsOpenAIChunks(t *testing.T) {
	upstream := twinmindSSEUpstream(t,
		"data: {\"type\":\"run_start\",\"version\":1,\"session_id\":\"s-1\"}\n\n"+
			"data: {\"type\":\"thinking_start\"}\n\n"+
			"data: {\"type\":\"thinking_delta\",\"content\":\"let me think\"}\n\n"+
			"data: {\"type\":\"text_start\"}\n\n"+
			"data: {\"type\":\"text_delta\",\"content\":\"Hel\"}\n\n"+
			"data: {\"type\":\"text_delta\",\"content\":\"lo\"}\n\n"+
			"data: {\"type\":\"text_delta\",\"content\":\"\"}\n\n"+
			"data: {\"type\":\"done\",\"sequence_num\":0}\n\n",
		func(r *http.Request, body string) {
			if r.Header.Get("Authorization") != "Bearer id-token" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			if !strings.Contains(body, `"query"`) || !strings.Contains(body, "hello there") {
				t.Errorf("payload missing flattened query: %s", body[:min(len(body), 200)])
			}
			if !strings.Contains(body, `"model_name":"gemini-3.7-flash"`) {
				t.Errorf("explicit model must ride as {model_name}: %s", body[:min(len(body), 300)])
			}
		})

	w := httptest.NewRecorder()
	err := ForwardTwinMind(w, &Request{
		Ctx:      t.Context(),
		Client:   upstream.Client(),
		Config:   &providers.ProviderConfig{BaseURL: upstream.URL},
		APIKey:   "id-token",
		IsStream: true,
		Body: []byte(`{"model":"gemini-3.7-flash","messages":[` +
			`{"role":"system","content":"be brief"},{"role":"user","content":"hello there"}]}`),
	})
	if err != nil {
		t.Fatalf("ForwardTwinMind: %v", err)
	}
	out := w.Body.String()
	for _, want := range []string{`"content":"Hel"`, `"content":"lo"`, `"finish_reason":"stop"`, ""} {
		if !strings.Contains(out, want) {
			t.Errorf("stream output missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"content":"let me think"`) {
		t.Error("thinking leaked into content deltas")
	}
	if !strings.Contains(out, `"reasoning_content":"let me think"`) {
		t.Errorf("thinking must ride reasoning_content:\n%s", out)
	}
}

func TestForwardTwinMindNonStreamBuildsOneCompletion(t *testing.T) {
	upstream := twinmindSSEUpstream(t,
		"data: {\"type\":\"run_start\",\"session_id\":\"s-2\"}\n\n"+
			"data: {\"type\":\"text_delta\",\"content\":\"PONG\"}\n\n"+
			"data: {\"type\":\"done\"}\n\n",
		func(r *http.Request, body string) {
			if !strings.Contains(body, `"model":"auto"`) {
				t.Errorf(`model "auto" must ride as the literal string: %s`, body)
			}
		})
	w := httptest.NewRecorder()
	err := ForwardTwinMind(w, &Request{
		Ctx:    t.Context(),
		Client: upstream.Client(),
		Config: &providers.ProviderConfig{BaseURL: upstream.URL},
		APIKey: "id-token",
		Body:   []byte(`{"model":"auto","messages":[{"role":"user","content":"ping"}]}`),
	})
	if err != nil {
		t.Fatalf("ForwardTwinMind: %v", err)
	}
	out := w.Body.String()
	if !strings.Contains(out, `"content":"PONG"`) || !strings.Contains(out, `"role":"assistant"`) {
		t.Errorf("non-stream completion wrong: %s", out)
	}
	// A non-stream request must produce exactly one JSON document: emitting the
	// upstream deltas as chat.completion.chunk lines next to it produced a body
	// that no client could parse.
	if strings.Contains(out, "chat.completion.chunk") || strings.Contains(out, "data: ") {
		t.Errorf("non-stream request emitted SSE chunks alongside the completion:\n%s", out)
	}
}

func TestForwardTwinMindStripsVendorPrefix(t *testing.T) {
	upstream := twinmindSSEUpstream(t,
		"data: {\"type\":\"done\"}\n\n",
		func(r *http.Request, body string) {
			if strings.Contains(body, `"model_name":"google/`) {
				t.Errorf("vendor prefix leaked upstream: %s", body)
			}
			if !strings.Contains(body, `"model_name":"gemini-3.7-flash"`) {
				t.Errorf("bare model name missing: %s", body)
			}
		})
	w := httptest.NewRecorder()
	err := ForwardTwinMind(w, &Request{
		Ctx:    t.Context(),
		Client: upstream.Client(),
		Config: &providers.ProviderConfig{BaseURL: upstream.URL},
		APIKey: "id-token",
		Body:   []byte(`{"model":"google/gemini-3.7-flash","messages":[{"role":"user","content":"x"}]}`),
	})
	if err != nil {
		t.Fatalf("ForwardTwinMind: %v", err)
	}
}

func TestForwardTwinMindMapsUpstreamErrorEvent(t *testing.T) {
	upstream := twinmindSSEUpstream(t,
		"data: {\"type\":\"error\",\"content\":\"model not allowed\"}\n\n",
		func(*http.Request, string) {})
	w := httptest.NewRecorder()
	err := ForwardTwinMind(w, &Request{
		Ctx:    t.Context(),
		Client: upstream.Client(),
		Config: &providers.ProviderConfig{BaseURL: upstream.URL},
		APIKey: "id-token",
		Body:   []byte(`{"model":"auto","messages":[{"role":"user","content":"x"}]}`),
	})
	if err == nil {
		t.Fatal("expected an upstream error for the error event")
	}
	if !strings.Contains(err.Error(), "model not allowed") {
		t.Errorf("error message lost: %v", err)
	}
}
