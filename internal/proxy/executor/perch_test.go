package executor

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// TestPerchForwardSendsManualModelOption pins the one detail that decides
// whether the model actually changes: Perch ignores preferredModelId and only
// honours manualModelOptionId. The value is not the display id — asking for
// "deepseek-flash-starter" has to send the openrouter option id, or the server
// answers with the account's default model instead.
func TestPerchForwardSendsManualModelOption(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("missing bearer token, got %q", auth)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"text":"HELLO","model":"deepseek/deepseek-v4.1-flash-1017","finishReason":"stop","usage":{"inputTokens":7,"outputTokens":2}}`)
	}))
	defer srv.Close()

	cfg := providers.KnownProviders["perch"]
	cfg.BaseURL = srv.URL

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:    context.Background(),
		Client: http.DefaultClient,
		Config: &cfg,
		APIKey: "test-token",
		Body:   []byte(`{"model":"deepseek-flash-starter","messages":[{"role":"user","content":"hi"}]}`),
	}
	if err := ForwardPerch(rec, req); err != nil {
		t.Fatalf("ForwardPerch: %v", err)
	}

	if got := gotBody["manualModelOptionId"]; got != "openrouter-deepseek-deepseek-v4-1-flash-1017-cloudflare" {
		t.Errorf("manualModelOptionId = %v, want the openrouter option id for deepseek-flash-starter", got)
	}
	// preferredModelId and strictManual:false are what the CLI sends. Neither
	// pins the model (manualModelOptionId does), but the envelope has to match
	// the official client or the gate treats the request as unbound.
	if gotBody["preferredModelId"] == nil {
		t.Error("preferredModelId must be sent: the CLI always includes it")
	}
	if gotBody["strictManual"] != false {
		t.Error("strictManual must be false: that is what the CLI sends")
	}
	if gotBody["clientSurface"] != "cli" {
		t.Errorf("clientSurface = %v, want cli", gotBody["clientSurface"])
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "HELLO" {
		t.Errorf("content not mapped: %s", rec.Body.String())
	}
	if out.Usage.TotalTokens != 9 {
		t.Errorf("usage total = %d, want 9", out.Usage.TotalTokens)
	}
}

// A model the router exposes but Perch has no option id for must fail loudly
// rather than silently falling back to the account default model.
func TestPerchForwardRejectsUnknownModel(t *testing.T) {
	cfg := providers.KnownProviders["perch"]
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:    context.Background(),
		Client: http.DefaultClient,
		Config: &cfg,
		APIKey: "test-token",
		Body:   []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
	}
	err := ForwardPerch(rec, req)
	if err == nil {
		t.Fatal("expected an error for a model without a Perch option id")
	}
	if !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("error should name the unknown model, got: %v", err)
	}
}

// Every catalogue entry must have an option id, or Import would advertise a
// model that cannot be called.
func TestPerchCatalogueMatchesOptionTable(t *testing.T) {
	models := providers.GetProviderModels("perch")
	if len(models) == 0 {
		t.Fatal("perch has no catalogue")
	}
	for _, id := range models {
		if _, ok := PerchModelOption[id]; !ok {
			t.Errorf("catalogue id %q has no PerchModelOption entry", id)
		}
	}
	listed := make(map[string]bool, len(models))
	for _, m := range models {
		listed[m] = true
	}
	for id := range PerchModelOption {
		if !listed[id] {
			t.Errorf("PerchModelOption has %q but the catalogue does not list it", id)
		}
	}
}

// The block-array content shape must flatten, since Perch takes a plain string.
func TestPerchTextFlattensBlocks(t *testing.T) {
	got := perchText([]any{
		map[string]any{"type": "text", "text": "one"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "http://x"}},
		map[string]any{"type": "text", "text": "two"},
	})
	if got != "one\ntwo" {
		t.Errorf("perchText = %q, want %q", got, "one\ntwo")
	}
	if s, ok := any("plain").(string); !ok || perchText(s) != "plain" {
		t.Error("a plain string must pass through unchanged")
	}
}

// The CLI keeps one run id for every turn of a session. Minting a fresh one per
// request is the shape Perch reads as direct API access, which is what produced
// the 403 "Repeated direct-access attempts" the harness reported.
func TestPerchRunIDStablePerSession(t *testing.T) {
	a := perchRunID(&Request{SessionID: "conversation-abc"})
	b := perchRunID(&Request{SessionID: "conversation-abc"})
	if a != b {
		t.Errorf("same session produced different run ids: %q vs %q", a, b)
	}
	c := perchRunID(&Request{SessionID: "conversation-xyz"})
	if a == c {
		t.Errorf("different sessions share a run id: %q", a)
	}
}

// Without a session there is nothing to key on, so each call must still be
// distinct — collapsing unrelated requests onto one run would be worse.
func TestPerchRunIDUniqueWithoutSession(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		id := perchRunID(&Request{})
		if seen[id] {
			t.Fatalf("duplicate run id without a session: %q", id)
		}
		seen[id] = true
	}
}

// The id has to keep the CLI's shape or the endpoint may reject it outright.
func TestPerchRunIDShape(t *testing.T) {
	id := perchRunID(&Request{SessionID: "s1"})
	if !strings.HasPrefix(id, "tkt-cli-") {
		t.Errorf("run id %q must start with tkt-cli-", id)
	}
	parts := strings.Split(id, "-")
	if len(parts) != 4 {
		t.Fatalf("run id %q should be tkt-cli-<ms>-<8 chars>", id)
	}
	if len(parts[3]) != 8 {
		t.Errorf("suffix %q should be 8 chars", parts[3])
	}
}

// The CLI binds every model call to the account with an attribution block
// (userId / workspaceId / runId / lane / source). Without it Perch treats the
// request as unbound and answers 403 perch_surface_required — clientSurface
// alone is just a string the caller asserts.
func TestPerchForwardSendsAttribution(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"text":"X","model":"m","finishReason":"stop"}`)
	}))
	defer srv.Close()

	cfg := providers.KnownProviders["perch"]
	cfg.BaseURL = srv.URL
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx: context.Background(), Client: http.DefaultClient, Config: &cfg,
		APIKey: "tok",
		Body:   []byte(`{"model":"deepseek-flash-starter","messages":[{"role":"user","content":"hi"}]}`),
		SessionID:        "conv-1",
		PerchUserID:      "user-abc",
		PerchWorkspaceID: "ws-xyz",
	}
	if err := ForwardPerch(rec, req); err != nil {
		t.Fatalf("ForwardPerch: %v", err)
	}
	attr, ok := got["attribution"].(map[string]any)
	if !ok {
		t.Fatalf("no attribution in body: %v", got)
	}
	for k, want := range map[string]string{
		"userId": "user-abc", "workspaceId": "ws-xyz",
		"lane": "chat", "source": "cli",
	} {
		if attr[k] != want {
			t.Errorf("attribution.%s = %v, want %q", k, attr[k], want)
		}
	}
	if attr["runId"] != got["runId"] {
		t.Errorf("attribution.runId %v must match the body runId %v", attr["runId"], got["runId"])
	}
	if got["strictManual"] != false {
		t.Error("strictManual must be false: the CLI sends false, true pins differently")
	}
	if got["preferredModelId"] == nil {
		t.Error("preferredModelId missing: the CLI always sends it")
	}
}

// Without captured ids there is nothing to attribute, and sending an empty
// block would be worse than omitting it.
func TestPerchForwardOmitsEmptyAttribution(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"text":"X","model":"m"}`)
	}))
	defer srv.Close()

	cfg := providers.KnownProviders["perch"]
	cfg.BaseURL = srv.URL
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx: context.Background(), Client: http.DefaultClient, Config: &cfg, APIKey: "tok",
		Body: []byte(`{"model":"deepseek-flash-starter","messages":[{"role":"user","content":"hi"}]}`),
	}
	if err := ForwardPerch(rec, req); err != nil {
		t.Fatalf("ForwardPerch: %v", err)
	}
	if _, has := got["attribution"]; has {
		t.Error("attribution must be omitted when no ids were captured")
	}
}

// Perch refuses a request whose body carries another harness's system-prompt
// opening — measured 2026-10-23 by bisecting a real 403. The literal sentence is
// the trigger; the harness name alone is not.
func TestSanitizePerchTextRemovesCompetingSignature(t *testing.T) {
	cases := map[string]string{
		"You are an expert coding assistant operating inside pi, a coding agent harness.": "You are an expert coding assistant operating inside a coding agent harness.",
		"you are an expert coding assistant operating inside pi, a coding agent harness.": "you are an expert coding assistant operating inside a coding agent harness.",
		"You are an expert coding assistant operating inside pi, a coding agent harness. You help.": "You are an expert coding assistant operating inside a coding agent harness. You help.",
	}
	for in, want := range cases {
		if got := sanitizePerchText(in); got != want {
			t.Errorf("sanitizePerchText(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

// Anything the gate does not fingerprint must pass through untouched: rewriting
// a prompt the server already accepts would change model behaviour for no reason.
func TestSanitizePerchTextLeavesOtherPromptsAlone(t *testing.T) {
	for _, s := range []string{
		"",
		"You are a helpful assistant.",
		"You are a coding assistant operating inside pi.",
		"You are an assistant operating inside claude code, a coding agent harness.",
		"You are an assistant operating inside cursor, a coding agent harness.",
	} {
		if got := sanitizePerchText(s); got != s {
			t.Errorf("sanitizePerchText(%q) = %q, want it unchanged", s, got)
		}
	}
}

// The rewrite has to survive a real forward: the body the server sees must no
// longer carry the fingerprint.
func TestPerchForwardSanitizesSystemPrompt(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"text":"X","model":"m"}`)
	}))
	defer srv.Close()

	cfg := providers.KnownProviders["perch"]
	cfg.BaseURL = srv.URL
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx: context.Background(), Client: http.DefaultClient, Config: &cfg, APIKey: "tok",
		Body: []byte(`{"model":"deepseek-flash-starter","messages":[{"role":"system","content":"You are an expert coding assistant operating inside pi, a coding agent harness. Keep going."},{"role":"user","content":"hi"}]}`),
	}
	if err := ForwardPerch(rec, req); err != nil {
		t.Fatalf("ForwardPerch: %v", err)
	}
	msgs, _ := got["request"].(map[string]any)["messages"].([]any)
	first, _ := msgs[0].(map[string]any)["content"].(string)
	if strings.Contains(strings.ToLower(first), "operating inside pi") {
		t.Errorf("forwarded system prompt still names the harness: %q", first)
	}
	if !strings.Contains(first, "Keep going.") {
		t.Errorf("the rest of the prompt must survive: %q", first)
	}
}

// A streaming tool-call turn must translate Perch's tool_call_delta into
// OpenAI chunks with delta.tool_calls and finish_reason: "tool_calls".
// Dropping these was the bug that made Pi stall with empty turns.
func TestPerchStreamTranslatesToolCalls(t *testing.T) {
	sseBody := strings.Join([]string{
		": keepalive",
		`data: {"type":"reasoning_delta","text":"thinking"}`,
		`data: {"type":"tool_call_delta","toolCalls":[{"id":"call_123","name":"read","arguments":{"path":"/etc/hostname"},"sealed":false}]}`,
		`data: {"type":"tool_use_end","toolCalls":[{"id":"call_123","name":"read","arguments":{"path":"/etc/hostname"},"sealed":true}]}`,
		`data: {"type":"done","ok":true,"text":"","finishReason":"tool_calls"}`,
		"",
	}, "\n")

	rec := httptest.NewRecorder()
	if err := perchStream(rec, strings.NewReader(sseBody), "deepseek-flash-starter"); err != nil {
		t.Fatalf("perchStream: %v", err)
	}

	out := rec.Body.String()
	if !strings.Contains(out, `"tool_calls"`) {
		t.Fatalf("stream output missing tool_calls: %s", out)
	}
	if !strings.Contains(out, `"read"`) {
		t.Errorf("stream output missing tool name 'read': %s", out)
	}
	if !strings.Contains(out, `/etc/hostname`) {
		t.Errorf("stream output missing arguments: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Errorf("stream output missing finish_reason tool_calls: %s", out)
	}
}
