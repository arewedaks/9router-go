package executor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// The Zed lane is not a passthrough: it mints an LLM token from the sign-in
// credential, resolves the upstream provider per model, wraps the request in
// {provider, model, provider_request}, and unwraps the native event stream.
// This test drives the whole path against a fake cloud and checks the three
// invariants that used to break: Bearer llm token (not the raw sign-in pair),
// an anthropic-shaped provider_request, and a translated chat.completion body.
func TestForwardZedMintsTokenAndTranslates(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody []byte
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/client/users/me":
			_, _ = io.WriteString(w, `{"default_organization_id":"org1"}`)
		case r.URL.Path == "/client/llm_tokens":
			_, _ = io.WriteString(w, `{"token":"llm-token-123"}`)
		case r.URL.Path == "/models":
			_, _ = io.WriteString(w, `{"models":[{"id":"zed-large","provider":"anthropic"}]}`)
		case r.URL.Path == "/completions":
			gotAuth = r.Header.Get("Authorization")
			gotPath = r.URL.Path
			gotBody, _ = io.ReadAll(r.Body)
			// Native Anthropic events, one bare JSON object per line.
			_, _ = io.WriteString(w, "{\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"zed-large\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n")
			_, _ = io.WriteString(w, "{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n")
			_, _ = io.WriteString(w, "{\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cloud.Close)

	oldBase := providers.ZedCloudBase
	providers.ZedCloudBase = cloud.URL
	t.Cleanup(func() { providers.ZedCloudBase = oldBase })
	providers.ResetZedLLMTokens()
	providers.ResetZedCatalog()

	req := &Request{
		Ctx:       t.Context(),
		Client:    cloud.Client(),
		Config:    &providers.ProviderConfig{},
		APIKey:    "secret-token",
		ZedUserID: "41234",
		Body:      []byte(`{"model":"zed-large","messages":[{"role":"user","content":"hi"}]}`),
	}
	rec := httptest.NewRecorder()
	if err := ForwardZed(rec, req); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if gotAuth != "Bearer llm-token-123" {
		t.Fatalf("Authorization = %q, want Bearer llm token", gotAuth)
	}
	if gotPath != "/completions" {
		t.Fatalf("path = %q", gotPath)
	}

	var envelope struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Request  struct {
			MaxTokens int `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		} `json:"provider_request"`
	}
	if err := json.Unmarshal(gotBody, &envelope); err != nil {
		t.Fatalf("envelope not json: %v", err)
	}
	if envelope.Provider != "anthropic" || envelope.Model != "zed-large" {
		t.Fatalf("envelope = provider %q model %q", envelope.Provider, envelope.Model)
	}
	if len(envelope.Request.Messages) != 1 || envelope.Request.Messages[0].Content[0].Type != "text" {
		t.Fatalf("provider_request is not anthropic messages: %s", gotBody)
	}

	if rec.Code >= 400 {
		t.Fatalf("client got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"content":"hello"`) {
		t.Fatalf("translated body missing hello: %s", rec.Body.String())
	}

	// Without the user id there is no credential; refuse before the call.
	req2 := &Request{Ctx: t.Context(), Client: cloud.Client(), Config: &providers.ProviderConfig{}, APIKey: "secret-token", Body: []byte(`{}`)}
	if err := ForwardZed(httptest.NewRecorder(), req2); err == nil {
		t.Fatal("a connection without zedUserId was forwarded")
	}
}

// zedProviderRequest must match the live shapes (missing `type` on Responses
// input items or a missing forced stream answer 400/empty from Zed).
func TestZedProviderRequestShapes(t *testing.T) {
	chat := zedChat{model: "m", maxTokens: 16, messages: []zedMessage{{role: "user", text: "hi"}}}

	oa, err := zedProviderRequest("open_ai", "m", chat)
	if err != nil {
		t.Fatal(err)
	}
	var responses struct {
		Stream bool `json:"stream"`
		Input  []struct {
			Type string `json:"type"`
		} `json:"input"`
	}
	if err := json.Unmarshal(oa, &responses); err != nil {
		t.Fatal(err)
	}
	if !responses.Stream || responses.Input[0].Type != "message" {
		t.Fatalf("open_ai shape wrong: %s", oa)
	}

	an, err := zedProviderRequest("anthropic", "m", chat)
	if err != nil {
		t.Fatal(err)
	}
	var msgs struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(an, &msgs); err != nil {
		t.Fatal(err)
	}
	if msgs.Messages[0].Content[0].Type != "text" {
		t.Fatalf("anthropic shape wrong: %s", an)
	}
}
