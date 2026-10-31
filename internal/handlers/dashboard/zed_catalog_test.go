package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Importing Zed models used to fail with "does not support models listing".
// cloud.zed.dev/models answers to "<user_id> <token>" only, so the fetcher has to
// send both halves and map Zed's field names onto the router's.
func TestFetchZedModelsUsesRawUserIDToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"models":[
			{"provider":"anthropic","id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5","max_token_count":200000,"max_output_tokens":64000,"supports_tools":true},
			{"provider":"openai","id":"gpt-5-codex","display_name":"GPT-5 Codex","max_token_count":272000,"max_output_tokens":128000}
		],"default_model":"claude-sonnet-4-5","recommended_models":["claude-sonnet-4-5"]}`)
	}))
	t.Cleanup(srv.Close)

	orig := zedModelsURL
	zedModelsURL = srv.URL
	t.Cleanup(func() { zedModelsURL = orig })

	h := &Handler{}
	res := h.fetchZedModels("zed", `{"accessToken":"tok-1","zedUserId":"41234"}`, srv.Client().Timeout)
	if res.Error != "" {
		t.Fatalf("fetch failed: %s", res.Error)
	}
	if gotAuth != "41234 tok-1" {
		t.Fatalf("Authorization = %q, want the raw user id and token", gotAuth)
	}
	if len(res.Models) != 2 {
		t.Fatalf("got %d models, want 2", len(res.Models))
	}
	first := res.Models[0]
	if first.ID != "claude-sonnet-4-5" || first.Name != "Claude Sonnet 4.5" {
		t.Errorf("model mapped as %+v", first)
	}
	if first.ContextLength != 200000 || first.MaxOutput != 64000 {
		t.Errorf("limits mapped as %d/%d", first.ContextLength, first.MaxOutput)
	}
}

// A connection that has not been signed in must say so instead of hitting the
// endpoint with half a credential.
func TestFetchZedModelsExplainsMissingSignIn(t *testing.T) {
	h := &Handler{}
	res := h.fetchZedModels("zed", `{"accessToken":"tok-1"}`, 0)
	if res.Error == "" {
		t.Fatal("a connection without zedUserId was accepted")
	}
	if res.Supported != true {
		t.Error("zed must stay a supported provider so the panel offers import")
	}
}
