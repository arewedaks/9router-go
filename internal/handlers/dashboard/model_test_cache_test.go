package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/handlers/chat"
)

// TestDoJSONTestBypassesPromptCache guards the "fake ping" regression: the model
// test posts a byte-identical probe body every run, so without the no-cache
// header the second test is answered from the prompt cache in a few ms and the
// dashboard reports a latency that never reached the upstream.
func TestDoJSONTestBypassesPromptCache(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(chat.NoCacheHeader)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	h := &Handler{}
	if _, err := h.doJSONTest(context.Background(), srv.Client(), srv.URL+"/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), "k", "llm"); err != nil {
		t.Fatalf("doJSONTest: %v", err)
	}
	if got != "1" {
		t.Fatalf("expected %s: 1 on the probe request, got %q", chat.NoCacheHeader, got)
	}
}
