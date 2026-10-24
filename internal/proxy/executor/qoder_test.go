package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// qoderFakeStream serves the envelope Qoder really sends: doubly-wrapped
// chunks, an error frame, and — critically — a socket it never closes. A
// correct executor must stop on the sentinel rather than wait for EOF.
func qoderFakeStream(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame)
			if f != nil {
				f.Flush()
			}
		}
		// Hold the connection open: the real gateway does exactly this, and it
		// is what made ScanStream-based reads hang forever. Stop when the client
		// disconnects so Close() does not block.
		<-r.Context().Done()
	}))
}

func qoderEnvelope(inner string) string {
	return `data:{"headers":{"Content-Type":["application/json"]},"body":"` + inner + `","statusCodeValue":200,"statusCode":"OK"}` + "\n\n"
}

// TestExecQoderStreamTerminates is the timeout regression guard: it fails with
// "test timed out" if the executor waits for EOF after the sentinel.
func TestExecQoderStreamTerminates(t *testing.T) {
	srv := qoderFakeStream(t, []string{
		qoderEnvelope(`{\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}`),
		qoderEnvelope(qoderDone),
	})
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	done := make(chan error, 1)
	go func() {
		done <- execQoderStream(rec, resp.Body, &Request{Ctx: context.Background(), IsStream: true})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("execQoderStream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execQoderStream did not return after the sentinel — it waited for EOF")
	}

	out := rec.Body.String()
	if !strings.Contains(out, `"content":"Hi"`) {
		t.Errorf("delta not forwarded: %q", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("no sentinel sent to the client: %q", out)
	}
	if strings.Contains(out, "statusCodeValue") {
		t.Errorf("envelope leaked to the client: %q", out)
	}
}

// TestExecQoderStreamErrorFrame pins the plan-refusal path: an error frame must
// surface as an actionable message and still terminate.
func TestExecQoderStreamErrorFrame(t *testing.T) {
	srv := qoderFakeStream(t, []string{
		`data:{"headers":{},"body":"{\"code\":\"112\",\"message\":\"{\\\"pricingUrl\\\":\\\"x\\\"}\"}","statusCodeValue":403,"statusCode":"FORBIDDEN"}` + "\n\n",
	})
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	done := make(chan error, 1)
	go func() {
		done <- execQoderStream(rec, resp.Body, &Request{Ctx: context.Background(), IsStream: true})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("execQoderStream did not terminate on an error frame")
	}

	if !strings.Contains(rec.Body.String(), "not included in your current plan") {
		t.Errorf("want a plan message, got %q", rec.Body.String())
	}
}

// TestQoderEncodeBody pins the custom alphabet and the three-block rotation:
// the upstream 500s ("Illegal base64 character 22") on a standard encoding.
func TestQoderEncodeBody(t *testing.T) {
	got := string(qoderEncodeBody([]byte("hello world")))
	if strings.ContainsAny(got, "+/=") {
		t.Errorf("standard base64 alphabet/padding survived: %q", got)
	}
	for _, c := range got {
		if !strings.ContainsRune(qoderCustomAlphabet+"$", c) {
			t.Errorf("byte %q is outside the custom alphabet: %q", c, got)
		}
	}

	// The rotation is deterministic, so the same input must always encode the
	// same way — the signature covers these bytes.
	if again := string(qoderEncodeBody([]byte("hello world"))); again != got {
		t.Errorf("encoding is not deterministic: %q vs %q", got, again)
	}
	// The rotation must actually happen: plain substitution would leave a
	// standard-base64 prefix visible.
	if strings.HasPrefix(got, "aGVsbG8") {
		t.Errorf("block rotation missing: %q", got)
	}
}

// TestBuildQoderBodyAgentShape pins the agent envelope fields; an OpenAI-shaped
// body passes the signature check and then dies inside the upstream router.
func TestBuildQoderBodyAgentShape(t *testing.T) {
	raw, err := buildQoderBody([]byte(`{
		"model":"dfmodel",
		"messages":[{"role":"user","content":"hi"}],
		"max_tokens":32
	}`), "dfmodel")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		`"chat_task":"FREE_INPUT"`, `"agent_id":"agent_common"`, `"session_type":"qodercli"`,
		`"enable_thinking":false`, `"reasoning_effort":"none"`, `"key":"dfmodel"`,
		`"messages":[{"role":"user","content":"hi"}]`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("agent envelope missing %s in %s", want, out[:min(300, len(out))])
		}
	}
}

// TestBuildQoderBodySystemMessage pins that a system prompt rides as a leading
// role:system message: the upstream ignores the top-level `system` field.
func TestBuildQoderBodySystemMessage(t *testing.T) {
	raw, err := buildQoderBody([]byte(`{"messages":[
		{"role":"system","content":"be brief"},
		{"role":"user","content":"hi"}
	]}`), "lite")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]`) {
		t.Errorf("system prompt not prepended as a message: %s", raw)
	}
}
