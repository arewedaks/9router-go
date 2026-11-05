package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
)

// The gateway accepts only a one-hour Firebase ID token, while the connection
// stores the account's refresh token. Getting this wrong is invisible until the
// upstream answers 401 "Sign in to use included AI" on every request, so pin the
// exchange, the cache and the forwarded headers.
func TestForwardAdCodeExchangesRefreshToken(t *testing.T) {
	var tokenCalls, chatCalls int32
	var gotAuth, gotSource, gotPersona, gotUA atomic.Value

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "refresh-token-value" {
			t.Errorf("refresh_token was not forwarded to the exchange")
		}
		if r.URL.Query().Get("key") == "" {
			t.Error("exchange called without the Firebase api key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id_token":"minted-id-token","expires_in":"3600","refresh_token":"refresh-token-value"}`)
	}))
	t.Cleanup(tokenSrv.Close)

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&chatCalls, 1)
		gotAuth.Store(r.Header.Get("Authorization"))
		gotSource.Store(r.Header.Get("x-adcode-ai-source"))
		gotPersona.Store(r.Header.Get("x-adcode-ai-persona"))
		gotUA.Store(r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(chatSrv.Close)

	orig := providers.AdCodeTokenURL
	providers.AdCodeTokenURL = tokenSrv.URL
	t.Cleanup(func() { providers.AdCodeTokenURL = orig })
	providers.ResetAdCodeTokenCache()

	cfg := providers.KnownProviders["adcode"]
	if cfg.BaseURL == "" {
		t.Fatal("adcode is not registered in providers.KnownProviders")
	}
	cfgCopy := cfg
	cfgCopy.BaseURL = chatSrv.URL

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		err := ForwardAdCode(rec, &Request{
			Ctx:      context.Background(),
			Client:   chatSrv.Client(),
			Config:   &cfgCopy,
			APIKey:   "refresh-token-value",
			Body:     []byte(`{"model":"heabsy-qwen38","messages":[{"role":"user","content":"ping"}]}`),
			Provider: "adcode",
		})
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Fatalf("token exchange ran %d times, want 1 (the second call must use the cache)", got)
	}
	if got := atomic.LoadInt32(&chatCalls); got != 2 {
		t.Fatalf("chat reached upstream %d times, want 2", got)
	}
	if auth, _ := gotAuth.Load().(string); auth != "Bearer minted-id-token" {
		t.Errorf("Authorization = %q, want the minted ID token, not the refresh token", auth)
	}
	if src, _ := gotSource.Load().(string); src != "chat" {
		t.Errorf("x-adcode-ai-source = %q", src)
	}
	if persona, _ := gotPersona.Load().(string); persona != "coding-assistant" {
		t.Errorf("x-adcode-ai-persona = %q", persona)
	}
	if ua, _ := gotUA.Load().(string); !strings.Contains(ua, "ADCode/") {
		t.Errorf("User-Agent = %q, want the desktop client agent (Cloudflare rejects Go's)", ua)
	}
}

// The gateway rejects `store` (any value) and content-part arrays with a bare
// 400, so a harness request must be repaired before it leaves the router. Pin
// both drops at the wire: mock the gateway's own rejections and assert the
// forwarded body no longer carries them.
func TestForwardAdCodeDropsStoreAndFlattensContent(t *testing.T) {
	var lastBody []byte

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id_token":"minted","expires_in":"3600"}`)
	}))
	t.Cleanup(tokenSrv.Close)

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody, _ = io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(lastBody, &m)
		// Reproduce the gateway's two refusals.
		if _, ok := m["store"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"bad-request","message":"Send a supported chat request."}}`)
			return
		}
		if msgs, ok := m["messages"].([]any); ok {
			for _, raw := range msgs {
				if msg, ok := raw.(map[string]any); ok {
					if _, isList := msg["content"].([]any); isList {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"code":"invalid-messages","message":"Included AI could not read this conversation's message format."}}`)
						return
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(chatSrv.Close)

	origTok := providers.AdCodeTokenURL
	providers.AdCodeTokenURL = tokenSrv.URL
	t.Cleanup(func() { providers.AdCodeTokenURL = origTok })
	providers.ResetAdCodeTokenCache()

	cfg := providers.KnownProviders["adcode"]
	cfg.BaseURL = chatSrv.URL

	body := `{"model":"deepseek-v4.1-flash-unc","store":false,"stream":true,` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"say "},{"type":"text","text":"OK"}]}]}`

	rec := httptest.NewRecorder()
	err := ForwardAdCode(rec, &Request{
		Ctx:      context.Background(),
		Client:   chatSrv.Client(),
		Config:   &cfg,
		APIKey:   "refresh-token",
		Body:     []byte(body),
		Provider: "adcode",
	})
	if err != nil {
		t.Fatalf("ForwardAdCode: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s (body sent: %s)", rec.Code, rec.Body.String(), lastBody)
	}
	var sent map[string]any
	if err := json.Unmarshal(lastBody, &sent); err != nil {
		t.Fatalf("upstream body not JSON: %v", err)
	}
	if _, ok := sent["store"]; ok {
		t.Error("store reached the upstream; the gateway rejects it with a field-less 400")
	}
	msgs, _ := sent["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("messages missing from the forwarded body")
	}
	if content, ok := msgs[0].(map[string]any)["content"].(string); !ok || content != "say OK" {
		t.Errorf("content must be flattened to a string, got %#v", msgs[0].(map[string]any)["content"])
	}
}

// A dead refresh token must surface the upstream code, not a generic failure and
// never the token itself.
func TestAdCodeIDTokenRejectsRevokedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":400,"message":"INVALID_REFRESH_TOKEN","status":"INVALID_ARGUMENT"}}`)
	}))
	t.Cleanup(srv.Close)

	orig := providers.AdCodeTokenURL
	providers.AdCodeTokenURL = srv.URL
	t.Cleanup(func() { providers.AdCodeTokenURL = orig })
	providers.ResetAdCodeTokenCache()

	_, err := providers.AdCodeIDToken(context.Background(), srv.Client(), "stale-refresh-token")
	if err == nil {
		t.Fatal("expected an error for a revoked refresh token")
	}
	if !strings.Contains(err.Error(), "INVALID_REFRESH_TOKEN") {
		t.Fatalf("error %q does not name the upstream code", err)
	}
	if strings.Contains(err.Error(), "stale-refresh-token") {
		t.Fatal("the error leaks the refresh token")
	}
}
