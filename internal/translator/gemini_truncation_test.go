package translator

import (
	"strings"
	"testing"
)

// A stream that ends without a finishReason must not look like a clean finish.
// Before this was fixed the state kept FinishReason == "" and the handler wrote
// `data: [DONE]`, so a cut-off answer reached the client as a success — which is
// why upstream truncation showed up only as a log line with err=<nil>.
func TestStreamTruncationIsDetectable(t *testing.T) {
	st := &GeminiStreamState{}
	for _, c := range []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"partial answ"}]}}]}`,
	} {
		if _, err := TranslateGeminiChunkToOpenAI([]byte(c), st); err != nil {
			t.Fatal(err)
		}
	}
	if st.FinishReason != "" {
		t.Fatalf("truncated stream set FinishReason=%q, want empty", st.FinishReason)
	}

	// And a stream that DOES finish must record it, so it is not misreported.
	st2 := &GeminiStreamState{}
	for _, c := range []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}`,
	} {
		if _, err := TranslateGeminiChunkToOpenAI([]byte(c), st2); err != nil {
			t.Fatal(err)
		}
	}
	if st2.FinishReason == "" {
		t.Fatal("finished stream left FinishReason empty")
	}
}

// EnsureStreamClosedReason must flag a truncated close so a Claude-format client
// is told the answer was cut, instead of receiving a fabricated clean end_turn.
func TestEnsureStreamClosedReasonEmitsErrorWhenTruncated(t *testing.T) {
	for _, tc := range []struct {
		name      string
		truncated bool
		wantErr   bool
	}{
		{"clean close", false, false},
		{"truncated close", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := "test-trunc-" + tc.name
			// Open a stream so there is state to close.
			if _, err := TranslateOpenAIToClaudeStreamSession(key, []byte(
				`{"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`,
			)); err != nil {
				t.Fatal(err)
			}
			out := EnsureStreamClosedReason(key, tc.truncated)
			ClearStreamState(key)
			if len(out) == 0 {
				t.Fatal("expected a close payload")
			}
			gotErr := strings.Contains(string(out), `"type":"error"`)
			if gotErr != tc.wantErr {
				t.Fatalf("truncated=%v produced error event=%v, want %v\npayload: %s",
					tc.truncated, gotErr, tc.wantErr, truncate(string(out), 400))
			}
			if !strings.Contains(string(out), "message_stop") {
				t.Fatal("stream must still be terminated for the client")
			}
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
