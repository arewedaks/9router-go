package executor

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// Antigravity wraps EACH SSE event in its own {"response":{...}} envelope. The
// old path read the whole body and unwrapped once, so a multi-event stream was
// parsed as a single object and everything after the first event was dropped —
// long generations arrived as ~12 chunks of a few KB. This pins that every
// event survives.
func TestAntigravityStreamUnwrapsEveryEvent(t *testing.T) {
	var sb strings.Builder
	for i, ev := range []string{
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"alpha "}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"beta "}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"gamma"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}}`,
	} {
		_ = i
		sb.WriteString("data: " + ev + "\n\n")
	}

	rec := httptest.NewRecorder()
	err := geminiStream(rec, strings.NewReader(sb.String()), context.Background(), true)
	if err != nil {
		t.Fatalf("geminiStream: %v", err)
	}

	body := rec.Body.String()
	for _, want := range []string{"alpha ", "beta ", "gamma"} {
		if !strings.Contains(body, want) {
			t.Fatalf("event text %q was dropped from the stream\nbody: %s", want, body)
		}
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("finish reason lost after unwrap\nbody: %s", body)
	}
}

// The first event of the stream must not be discarded: it carries real content
// whenever the model starts talking immediately.
func TestGeminiStreamKeepsFirstChunk(t *testing.T) {
	sse := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"FIRST\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"SECOND\"}]},\"finishReason\":\"STOP\"}]}\n\n"

	rec := httptest.NewRecorder()
	if err := geminiStream(rec, strings.NewReader(sse), context.Background(), false); err != nil {
		t.Fatalf("geminiStream: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "FIRST") {
		t.Fatalf("first chunk was dropped\nbody: %s", rec.Body.String())
	}
}

// A multi-part candidate must not lose its tail: only parts[0] was read before.
func TestGeminiNonStreamConcatenatesAllParts(t *testing.T) {
	raw := `{"candidates":[{"content":{"role":"model","parts":[{"text":"one"},{"text":" two"}]},"finishReason":"STOP"}]}`

	rec := httptest.NewRecorder()
	if err := geminiNonStream(rec, strings.NewReader(raw)); err != nil {
		t.Fatalf("geminiNonStream: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "one two") {
		t.Fatalf("multi-part text truncated\nbody: %s", rec.Body.String())
	}
}
