package executor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// The whole point of the Zed lane: the upstream must receive
// "Authorization: <user_id> <token>" with no bearer prefix, and a connection
// without the user id must fail loudly instead of sending a token Zed rejects.
func TestForwardZedSendsRawUserIDToken(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = buf
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(upstream.Close)

	cfg := &providers.ProviderConfig{
		BaseURL:    upstream.URL + "/completions",
		AuthHeader: "Authorization",
		AuthScheme: "raw",
	}
	req := &Request{
		Ctx:       t.Context(),
		Client:    upstream.Client(),
		Config:    cfg,
		APIKey:    "secret-token",
		ZedUserID: "41234",
		Body:      []byte(`{"model":"zed-large","messages":[{"role":"user","content":"hi"}]}`),
		Provider:  "zed",
	}
	rec := httptest.NewRecorder()
	if err := ForwardZed(rec, req); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if gotAuth != "41234 secret-token" {
		t.Fatalf("Authorization = %q, want the raw user id and token", gotAuth)
	}
	if !strings.HasSuffix(gotPath, "/completions") {
		t.Errorf("path = %q", gotPath)
	}
	if len(gotBody) == 0 {
		t.Error("body was not forwarded")
	}
	if rec.Code >= 400 {
		t.Errorf("client got %d: %s", rec.Code, rec.Body.String())
	}

	// Without the user id there is no valid credential; refuse before the call.
	req2 := &Request{Ctx: t.Context(), Client: upstream.Client(), Config: cfg, APIKey: "secret-token", Body: []byte(`{}`)}
	if err := ForwardZed(httptest.NewRecorder(), req2); err == nil {
		t.Fatal("a connection without zedUserId was forwarded")
	}
}
