package executor

import (
	"strings"
	"testing"
)

// freePiAdGateStream is the gate exactly as upstream serves it to a streaming
// request (captured 2026-11-11): three SSE frames, the instruction carried as
// delta.content, then finish_reason "stop" with usage all zeroes. There is no
// error frame to branch on, which is why detection has to use the zero-usage
// signal rather than the frame type.
const freePiAdGateStream = `data: {"id":"chatcmpl-freepi-x","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"free-pi is free because it shows ads, and this session hasn't loaded any. Requests stay paused until ads load in the official free-pi CLI."},"finish_reason":null}]}

data: {"id":"chatcmpl-freepi-x","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}

data: [DONE]

`

func TestFreePiAdGateStreamBody_RealShape(t *testing.T) {
	got := freePiAdGateStreamBody([]byte(freePiAdGateStream))
	if got == "" {
		t.Fatal("the streaming gate was not detected; it would be streamed to the client as the answer")
	}
	if !strings.Contains(got, "free-pi is free because it shows ads") {
		t.Errorf("detected gate text = %q, want the provider's own wording", got)
	}
}

func TestFreePiAdGateStreamBody_IgnoresRealAnswers(t *testing.T) {
	// A billed stream that merely mentions the phrase is a real answer, and
	// converting it to an error would hide output the account paid for.
	billed := strings.Replace(freePiAdGateStream,
		`"prompt_tokens":0,"completion_tokens":0,"total_tokens":0`,
		`"prompt_tokens":31,"completion_tokens":7,"total_tokens":38`, 1)
	if got := freePiAdGateStreamBody([]byte(billed)); got != "" {
		t.Errorf("a billed answer was misread as the gate: %q", got)
	}
	// A normal stream with no usage frame at all must be left alone rather than
	// guessed at.
	plain := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"
	if got := freePiAdGateStreamBody([]byte(plain)); got != "" {
		t.Errorf("a plain stream was misread as the gate: %q", got)
	}
	for _, empty := range []string{"", "data: [DONE]\n\n"} {
		if got := freePiAdGateStreamBody([]byte(empty)); got != "" {
			t.Errorf("body %q misread as the gate", empty)
		}
	}
}
