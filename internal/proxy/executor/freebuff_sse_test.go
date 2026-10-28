package executor

import (
	"strings"
	"testing"
)

// The free lane always streams, so a non-streaming caller's body has to be
// rebuilt from the SSE. Tool-call arguments arrive as fragments keyed by index
// and must be concatenated in order, or the caller gets truncated JSON.
func TestFreebuffAggregateSSE(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1793080000,"model":"deepseek/deepseek-v4-flash","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-1","created":1793080000,"model":"deepseek/deepseek-v4-flash","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-1","created":1793080000,"model":"deepseek/deepseek-v4-flash","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-1","created":1793080000,"model":"deepseek/deepseek-v4-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_files","arguments":"{\"pat"}}]},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-1","created":1793080000,"model":"deepseek/deepseek-v4-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"h\":\"a.go\"}"}}]},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-1","created":1793080000,"model":"deepseek/deepseek-v4-flash","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: {"id":"chatcmpl-1","created":1793080000,"model":"deepseek/deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	out, err := freebuffAggregateSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	text := string(out)

	for _, want := range []string{
		`"id":"chatcmpl-1"`,
		`"object":"chat.completion"`,
		`"content":"Hello"`,
		`"name":"read_files"`,
		`"arguments":"{\"path\":\"a.go\"}"`,
		`"finish_reason":"tool_calls"`,
		`"total_tokens":18`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("aggregated body is missing %s\n%s", want, text)
		}
	}
	if strings.Contains(text, `"object":"chat.completion.chunk"`) {
		t.Error("chunk object leaked into the aggregated body")
	}
}

// A gateway error arrives as plain JSON, not SSE, and must not be fed to the
// aggregator (which would produce an empty completion and hide the error).
func TestFreebuffLooksLikeSSE(t *testing.T) {
	if !freebuffLooksLikeSSE([]byte("data: {\"id\":\"x\"}\n\n")) {
		t.Error("SSE body not detected")
	}
	if freebuffLooksLikeSSE([]byte(`{"error":{"message":"model_locked"}}`)) {
		t.Error("plain JSON misdetected as SSE")
	}
}

// finish_reason must never be empty: a caller that checks it would treat the
// response as truncated.
func TestFreebuffAggregateSSEDefaultsFinishReason(t *testing.T) {
	stream := "data: {\"id\":\"a\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n"
	out, err := freebuffAggregateSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"finish_reason":"stop"`) {
		t.Fatalf("finish_reason not defaulted: %s", out)
	}
}
