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

// TestForwardDeepAI_InjectsLaneHeaders proves the two headers the upstream
// meters on reach the wire: the lane version and the auth header name. A bare
// OpenAI forward would send `Authorization` and drop the lane, which the
// upstream refuses with machine_required.
func TestForwardDeepAI_InjectsLaneHeaders(t *testing.T) {
	var gotHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{
		BaseURL:    upstream.URL + "/chat/completions",
		AuthHeader: "Api-Key",
		AuthScheme: "raw",
		StaticHeaders: map[string]string{
			"X-DeepAI-Code-Theos": "4",
		},
	}
	body := `{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`
	req := &Request{
		Ctx:       context.Background(),
		Client:    upstream.Client(),
		Config:    cfg,
		APIKey:    "test-key",
		Body:      []byte(body),
		ModelName: "glm-5.3-flash",
	}

	rec := httptest.NewRecorder()
	if err := ForwardDeepAI(rec, req); err != nil {
		t.Fatalf("ForwardDeepAI: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := gotHeaders.Get("X-DeepAI-Code-Theos"); got != "4" {
		t.Errorf("X-DeepAI-Code-Theos = %q, want 4", got)
	}
	if got := gotHeaders.Get("Api-Key"); got != "test-key" {
		t.Errorf("Api-Key = %q, want test-key", got)
	}
	if got := gotHeaders.Get("Authorization"); got != "" {
		t.Errorf("Authorization must not be set for DeepAI, got %q", got)
	}
}

// TestApplyDeepAIHeaders_DefaultModel proves a bodiless model is filled with the
// lane's bundled default rather than left absent.
func TestApplyDeepAIHeaders_DefaultModel(t *testing.T) {
	cfg := &providers.ProviderConfig{BaseURL: "https://api.deepai.org/deepai_code/chat/completions"}
	req := &Request{Config: cfg, Body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
	applyDeepAIHeaders(req)

	var got map[string]any
	if err := json.Unmarshal(req.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["model"] != DeepAIDefaultModel {
		t.Errorf("model = %v, want %s", got["model"], DeepAIDefaultModel)
	}
	if cfg.StaticHeaders["X-DeepAI-Code-Theos"] != "4" {
		t.Errorf("lane header not injected: %+v", cfg.StaticHeaders)
	}
}

// TestDeepAIModelsURL derives the catalogue path from the chat base.
func TestDeepAIModelsURL(t *testing.T) {
	cfg := &providers.ProviderConfig{BaseURL: "https://api.deepai.org/deepai_code/chat/completions"}
	got := DeepAIModelsURL(cfg)
	if got != "https://api.deepai.org/deepai_code/models" {
		t.Errorf("DeepAIModelsURL = %q", got)
	}
}

// TestParseMachineUUIDs mirrors the vendor's parser: a placeholder UUID reads as
// "no machine id" so the header is omitted rather than sent as a blank digest.
func TestParseMachineUUIDs(t *testing.T) {
	if got := parseSmbiosUUID("00000000-0000-0000-0000-000000000000"); got != "" {
		t.Errorf("placeholder UUID accepted: %q", got)
	}
	if got := parseSmbiosUUID("UUID\n4C4C4544-0032-4A10-8033-B3C04F4E4432\n"); got != "4c4c4544-0032-4a10-8033-b3c04f4e4432" {
		t.Errorf("real UUID parsed as %q", got)
	}
	ioreg := `  "IOPlatformUUID" = "A1B2C3D4-1111-2222-3333-444455556666"`
	if got := parseIOPlatformUUID(ioreg); got != "a1b2c3d4-1111-2222-3333-444455556666" {
		t.Errorf("ioreg UUID parsed as %q", got)
	}
	if sha256Hex("abc") != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Error("sha256Hex mismatch")
	}
}

// TestForwardDeepAI_LaneRefusalSurfaces proves an upstream refusal is not
// swallowed: the auth_required body reaches the client unchanged.
func TestForwardDeepAI_LaneRefusalSurfaces(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"Sign in to DeepAI to use DeepAI Code.","type":"auth_required","code":"auth_required"}}`)
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{BaseURL: upstream.URL + "/chat/completions", AuthHeader: "Api-Key", AuthScheme: "raw"}
	req := &Request{
		Ctx:    context.Background(),
		Client: upstream.Client(),
		Config: cfg,
		APIKey: "bad",
		Body:   []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`),
	}
	rec := httptest.NewRecorder()
	err := ForwardDeepAI(rec, req)
	if err == nil {
		t.Fatalf("expected an error for a 403 upstream, got nil (status %d)", rec.Code)
	}
	if !strings.Contains(err.Error(), "auth_required") {
		t.Errorf("refusal not surfaced through the error: %v", err)
	}
}
