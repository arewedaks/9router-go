package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An OpenAI-compatible listing that publishes limits must have them carried
// through the fetch, in each of the shapes seen in the wild: OpenRouter's
// context_length + nested top_provider.max_completion_tokens, and the flatter
// max_completion_tokens / context_window spelling some resellers use.
func TestFetchUpstreamModelsCarriesPublishedLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"deepseek-4.1-flash","context_length":1048576,
			 "top_provider":{"context_length":1048576,"max_completion_tokens":393216}},
			{"id":"muse-spark-open","context_window":524288,"max_output_tokens":524288},
			{"id":"bare-model"}
		]}`))
	}))
	defer srv.Close()

	h := &Handler{}
	res, err := h.doModelRequest(&http.Client{Timeout: 5 * time.Second}, "openai-compatible", srv.URL, http.MethodGet,
		map[string]string{"Accept": "application/json"}, nil)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("fetch error: %s", res.Error)
	}

	byID := map[string]UpstreamModel{}
	for _, m := range res.Models {
		byID[m.ID] = m
	}

	// top_provider wins the output cap when the top level omits it.
	if m := byID["deepseek-4.1-flash"]; m.ContextLength != 1048576 || m.MaxOutput != 393216 {
		t.Fatalf("deepseek limits = %d/%d, want 1048576/393216", m.ContextLength, m.MaxOutput)
	}
	if m := byID["muse-spark-open"]; m.ContextLength != 524288 || m.MaxOutput != 524288 {
		t.Fatalf("muse limits = %d/%d, want 524288/524288", m.ContextLength, m.MaxOutput)
	}
	// No published numbers: zero, so the static tables decide.
	if m := byID["bare-model"]; m.ContextLength != 0 || m.MaxOutput != 0 {
		t.Fatalf("bare limits = %d/%d, want 0/0", m.ContextLength, m.MaxOutput)
	}
}
