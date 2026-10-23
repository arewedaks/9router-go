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
	if _, has := gotBody["preferredModelId"]; has {
		t.Error("preferredModelId must not be sent: the server ignores it and it is not the pin")
	}
	if gotBody["strictManual"] != true {
		t.Error("strictManual must be true, otherwise the server may substitute its default model")
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
