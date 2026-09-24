package chat

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/tokensaver"
)

// seedConnDB inserts a single active connection for the given provider pointing at upstream.
func seedConnDB(t *testing.T, database *sql.DB, provider, connID, apiKey, baseURL string) {
	t.Helper()
	data, _ := json.Marshal(map[string]interface{}{"apiKey": apiKey, "baseUrl": baseURL})
	q := `INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'apikey', 'Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`
	if _, err := database.Exec(q, connID, provider, string(data)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

func TestApplyTokenSavers_AllOff(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got := h.applyTokenSavers(context.Background(), body, false, "test-model")
	if string(got) != string(body) {
		t.Errorf("expected unchanged body when all token savers off")
	}
}

func TestApplyTokenSavers_RTKOnly(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetRTK(true)

	// RTK compresses tool messages with large content. Build via json.Marshal
	// so newlines are properly escaped (raw newlines are invalid JSON).
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString("unique log line number ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}
	body, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "tool", "content": sb.String()},
		},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	got := h.applyTokenSavers(context.Background(), body, false, "test-model")
	if string(got) == string(body) {
		t.Errorf("expected RTK to modify body")
	}
}

func TestApplyTokenSavers_CavemanInjects(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetCaveman(true)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got := h.applyTokenSavers(context.Background(), body, false, "test-model")
	// Caveman prompt text should now appear in the system message.
	if !strings.Contains(string(got), "terse") && !strings.Contains(string(got), "caveman") {
		t.Errorf("expected caveman prompt injected, got %s", got)
	}
}

func TestApplyTokenSavers_PonytailInjects(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetPonytail(true)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got := h.applyTokenSavers(context.Background(), body, false, "test-model")
	if !strings.Contains(string(got), tokensaver.PonytailPrompt[:20]) {
		t.Errorf("expected ponytail prompt injected, got %s", got)
	}
}

// The bypass header must skip every stage, not just the first one. RTK, both
// prompt styles and the injection scan are enabled here so a stage that ignored
// the bypass would show up as a modified body.
func TestApplyTokenSavers_BypassSkipsEveryStage(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetRTK(true)
	h.TokenSaver.SetCaveman(true)
	h.TokenSaver.SetPonytail(true)
	h.TokenSaver.SetInjectionGuard(true)

	toolOutput := strings.Repeat("fatal: something went wrong\n", 60)
	body, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "run it"},
			map[string]any{"role": "tool", "content": toolOutput},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Precondition: without the bypass the body really is rewritten, so the
	// assertion below cannot pass just because nothing ever fires.
	changed := h.applyTokenSavers(context.Background(), body, false, "test-model")
	if string(changed) == string(body) {
		t.Fatal("precondition failed: the savers did not modify the body at all")
	}

	bypassed := tokensaver.WithBypass(context.Background(), "off")
	if got := h.applyTokenSavers(bypassed, body, false, "test-model"); string(got) != string(body) {
		t.Errorf("bypass did not preserve the body:\n in = %s\nout = %s", body, got)
	}
}

// The header value is matched case-insensitively, and any other value leaves
// the pipeline running.
func TestApplyTokenSavers_BypassHeaderValue(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetCaveman(true)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)

	for _, tc := range []struct {
		value string
		want  bool // true = body left untouched
	}{
		{"off", true},
		{"OFF", true},
		{"Off", true},
		{"on", false},
		{"", false},
	} {
		ctx := tokensaver.WithBypass(context.Background(), tc.value)
		got := h.applyTokenSavers(ctx, body, false, "test-model")
		untouched := string(got) == string(body)
		if untouched != tc.want {
			t.Errorf("header %q: untouched = %v, want %v", tc.value, untouched, tc.want)
		}
	}
}

// With Headroom enabled the pipeline posts the conversation to the proxy and
// keeps the compressed messages.
func TestApplyTokenSavers_HeadroomCompresses(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"role":"user","content":"compressed"}],"tokens_before":500,"tokens_after":10,"tokens_saved":490}`))
	}))
	defer srv.Close()

	h.TokenSaver.SetHeadroom(true, srv.URL, 2000)
	// The saver uses the handler's own client; point it at the test server's
	// transport so the request reaches the httptest listener.
	h.Client = srv.Client()

	body := []byte(`{"messages":[{"role":"user","content":"a very long conversation"}]}`)
	got := h.applyTokenSavers(context.Background(), body, false, "test-model")

	if gotPath != "/v1/compress" {
		t.Errorf("proxy path = %q, want /v1/compress", gotPath)
	}
	if !strings.Contains(string(got), "compressed") {
		t.Errorf("compressed content missing from body: %s", got)
	}
}

// A dead Headroom proxy must leave the request body alone. Fail-open is the
// whole reason the integration is safe to enable.
func TestApplyTokenSavers_HeadroomFailureKeepsBody(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	// Nothing is listening on this port; the call must fail and be swallowed.
	h.TokenSaver.SetHeadroom(true, "http://127.0.0.1:1", 200)

	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	if got := h.applyTokenSavers(context.Background(), body, false, "test-model"); string(got) != string(body) {
		t.Errorf("an unreachable proxy changed the body:\n in = %s\nout = %s", body, got)
	}
}

// Headroom is skipped when it is off, even if a URL is configured.
func TestApplyTokenSavers_HeadroomOffByDefault(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer srv.Close()

	h.Client = srv.Client()
	h.TokenSaver.SetHeadroom(false, srv.URL, 2000)

	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	h.applyTokenSavers(context.Background(), body, false, "test-model")
	if called {
		t.Error("the proxy was contacted while Headroom was disabled")
	}
}

func TestTryForwardWithConnection_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"done"}}],"usage":{"prompt_tokens":2,"completion_tokens":2}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, "deepseek", "conn-try", "sk-try", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := h.tryForwardWithConnection(context.Background(), rec, "deepseek", "deepseek-chat", "conn-try", &ConnectionData{APIKey: "sk-try", BaseURL: srv.URL}, body, false, false, "/v1/chat/completions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestTryForwardWithConnection_NoAPIKey(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	err := h.tryForwardWithConnection(context.Background(), rec, "deepseek", "deepseek-chat", "conn-x", &ConnectionData{}, []byte(`{}`), false, false, "/v1/chat/completions")
	if err == nil {
		t.Fatal("expected error when API key missing")
	}
	var ue *upstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *upstreamError, got %T", err)
	}
	if ue.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", ue.StatusCode)
	}
}

func TestHandleAccountFallback_RetryableLocksModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, "deepseek", "conn-429", "sk-429", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := h.handleAccountFallback(context.Background(), rec, "deepseek", "deepseek-chat", "", body, false, false, "/v1/chat/completions")
	if err == nil {
		t.Fatal("expected error after exhausting connections")
	}

	locked, lerr := repo.IsConnectionModelLocked("conn-429", "deepseek-chat")
	if lerr != nil {
		t.Fatalf("IsConnectionModelLocked failed: %v", lerr)
	}
	if !locked {
		t.Error("expected conn-429 per-connection lock after 429 on all connections")
	}
}

func TestHandleMessagesComboFallback_429LocksAndExcludesConnection(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	// Remove the helper's pre-seeded deepseek/groq connections so conn-combo is
	// the only deepseek connection (they're priority 1 and would shadow it).
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-combo", "sk-combo", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// Two models on the SAME provider+connection: after the first 429, the
	// connection is locked AND excluded so the second model must not re-hit it.
	comboModels := []string{"deepseek/deepseek-chat", "deepseek/deepseek-reasoner"}
	modelsJSON, _ := json.Marshal(comboModels)
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('combo-1', 'combo-test', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(modelsJSON)); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	translatedReq := map[string]any{
		"model":      "deepseek-chat",
		"max_tokens": 100,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	}
	rec := httptest.NewRecorder()
	h.handleMessagesComboFallback(context.Background(), rec, translatedReq, comboModels, "fallback", false, "combo-test", 0)

	if got := hits.Load(); got != 1 {
		t.Errorf("expected 1 upstream hit (second combo model excluded), got %d", got)
	}

	locked, lerr := repo.IsConnectionModelLocked("conn-combo", "deepseek-chat")
	if lerr != nil {
		t.Fatalf("IsConnectionModelLocked failed: %v", lerr)
	}
	if !locked {
		t.Error("expected conn-combo locked for deepseek-chat after 429")
	}
}

func TestHandleMessagesComboFallback_RetriesOnceOnBoundedRetryAfter(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			// Upstream says "wait ~4s" (RFC3339). The 429 connection lock is ~2s,
			// so the retry pass after the wait finds it unlocked again.
			ra := time.Now().Add(4 * time.Second).Format(time.RFC3339)
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"retryAfter":"` + ra + `"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":0,"model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	seedConnDB(t, database, "deepseek", "conn-combo", "sk-combo", srv.URL)

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	comboModels := []string{"deepseek/deepseek-chat"}
	modelsJSON, _ := json.Marshal(comboModels)
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('combo-r', 'combo-retry', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(modelsJSON)); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	translatedReq := map[string]any{
		"model":      "deepseek-chat",
		"max_tokens": 100,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	}
	rec := httptest.NewRecorder()
	h.handleMessagesComboFallback(context.Background(), rec, translatedReq, comboModels, "fallback", false, "combo-retry", 0)

	if got := hits.Load(); got != 2 {
		t.Errorf("expected 2 upstream hits (1 failure + 1 retry), got %d", got)
	}
}

func TestHandleAccountFallback_NoConnections(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	body := []byte(`{"model":"deepseek-chat","messages":[]}`)
	rec := httptest.NewRecorder()
	err := h.handleAccountFallback(context.Background(), rec, "nonexistent-provider", "model", "", body, false, false, "/v1/chat/completions")
	if err == nil {
		t.Fatal("expected error when provider has no connections")
	}
}

func TestHandleAccountFallback_503CapacityLocksCanonicalModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{
			"error": {
				"code": 503,
				"message": "No capacity available for model gemini-3.8-flash-tiered on the server",
				"status": "UNAVAILABLE",
				"details": [{"reason": "MODEL_CAPACITY_EXHAUSTED"}]
			}
		}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	agData, _ := json.Marshal(map[string]any{
		"apiKey":      "tok-ag",
		"accessToken": "tok-ag",
		"baseUrl":     srv.URL,
		"projectId":   "test-proj-503",
		"providerSpecificData": map[string]any{
			"projectId": "test-proj-503",
		},
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-ag-cap', 'antigravity', 'oauth', 'AG Cap Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(agData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	body := []byte(`{"model":"gemini-3.8-flash-low","messages":[{"role":"user","content":"ping"}]}`)
	rec := httptest.NewRecorder()
	err := h.handleAccountFallback(context.Background(), rec, "antigravity", "gemini-3.8-flash-low", "", body, false, false, "/v1/chat/completions")
	if err == nil {
		t.Fatal("expected error after 503 capacity exhaustion")
	}

	// Canonical model "gemini-3.8-flash-tiered" must be locked
	lockedCanonical, err := repo.IsConnectionModelLocked("conn-ag-cap", "gemini-3.8-flash-tiered")
	if err != nil {
		t.Fatalf("check canonical lock: %v", err)
	}
	if !lockedCanonical {
		t.Error("expected canonical model gemini-3.8-flash-tiered to be locked on 503 capacity error")
	}

	// Best connection for gemini-3.8-flash-high must now skip this connection because canonical tiered is locked!
	conn, _, _ := h.GetBestConnection("antigravity", "", nil, "gemini-3.8-flash-high")
	if conn != nil {
		t.Errorf("expected no connection available for high tier when canonical model is locked, got %s", conn.ID)
	}
}

func TestExtractErrorText_CloudflareHTML(t *testing.T) {
	htmlBody := []byte(`<!DOCTYPE html>
<html class="no-js" lang="en-US">
<head>
<title>Attention Required! | Cloudflare</title>
<meta charset="UTF-8" />
</head>
<body>
<h1>Attention Required!</h1>
</body>
</html>`)

	got := extractErrorText(htmlBody)
	if !strings.Contains(got, "Cloudflare WAF challenge") {
		t.Errorf("expected Cloudflare WAF challenge in error text, got %q", got)
	}
}

// CodeBuddy reports failures as {"code":N,"msg":"..."}, which is not the OpenAI
// {"error":{"message":"..."}} shape. The text was therefore always empty, so no
// quota rule could match and a spent model quota was retried on the 2-second
// backoff floor.
func TestExtractErrorText_CodebuddyMsgField(t *testing.T) {
	body := []byte(`{"code":6004,"msg":"usage exceeds frequency limit, but don't worry, your usage will reset at 2026-09-23 10:37:27 UTC+8","requestId":"x"}`)

	got := extractErrorText(body)
	if got == "" {
		t.Fatal("CodeBuddy msg field was not read; quota classification cannot work")
	}
	if !strings.Contains(got, "frequency limit") {
		t.Errorf("error text %q should carry the upstream wording", got)
	}
}

// The same body must now classify as an exhausted quota rather than a
// transient 429, so the account is parked for the stated reset instead of
// being retried seconds later.
func TestClassifyError_CodebuddyFrequencyLimitIsQuota(t *testing.T) {
	body := []byte(`{"code":6004,"msg":"usage exceeds frequency limit, but don't worry, your usage will reset at 2026-09-23 10:37:27 UTC+8"}`)
	text := extractErrorText(body)

	if !providers.LooksLikeQuotaExhausted(text) {
		t.Fatalf("error text %q did not read as a spent quota", text)
	}

	got := providers.ClassifyError(429, text, 0)
	if got.CooldownMs < 60_000 {
		t.Errorf("cooldown = %d ms; a spent quota must not use the seconds-scale backoff", got.CooldownMs)
	}
}
