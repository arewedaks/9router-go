package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// TestHandleModelTest_RequiresModel verifies the single-model endpoint
// rejects an empty model field with a 400.
func TestHandleModelTest_RequiresModel(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/models/test",
		strings.NewReader(`{"model":"  "}`))
	w := httptest.NewRecorder()

	h.HandleModelTest(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty model, got %d", w.Code)
	}
}

// TestHandleModelTest_InvalidJSON verifies malformed bodies are rejected.
func TestHandleModelTest_InvalidJSON(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/models/test",
		strings.NewReader(`{not json`))
	w := httptest.NewRecorder()

	h.HandleModelTest(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

// TestPingModel_ExtractsHTTPError verifies that a non-2xx upstream response is
// turned into a clear "HTTP <code>: <detail>" error.
func TestPingModel_ExtractsHTTPError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"Requested entity was not found."}}`))
	}))
	defer upstream.Close()

	h := &Handler{}
	res, _ := h.doJSONTest(context.Background(), upstream.Client(),
		upstream.URL, []byte(`{"model":"x"}`), "", "llm")

	if res.OK {
		t.Fatalf("expected failure, got OK")
	}
	if !strings.Contains(res.Error, "404") || !strings.Contains(res.Error, "not found") {
		t.Fatalf("error should mention status and detail, got %q", res.Error)
	}
}

// TestDoJSONTest_LLMOK verifies a well-formed OpenAI completion response is
// reported as a pass.
func TestDoJSONTest_LLMOK(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	h := &Handler{}
	res, _ := h.doJSONTest(context.Background(), upstream.Client(),
		upstream.URL, []byte(`{"model":"x"}`), "", "llm")

	if !res.OK {
		t.Fatalf("expected OK, got error: %s", res.Error)
	}
}

// TestDoJSONTest_ReasoningOnly verifies a length-limited reasoning-only reply
// is treated as a pass with a note (mirrors upstream behaviour).
func TestDoJSONTest_ReasoningOnly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","reasoning":"thinking..."},"finish_reason":"length"}]}`))
	}))
	defer upstream.Close()

	h := &Handler{}
	res, _ := h.doJSONTest(context.Background(), upstream.Client(),
		upstream.URL, []byte(`{"model":"x"}`), "", "llm")

	if !res.OK || res.Note == "" {
		t.Fatalf("expected reasoning-only pass with note, got OK=%v note=%q err=%q", res.OK, res.Note, res.Error)
	}
}

// TestDoJSONTest_NoChoices verifies an empty choices array is a failure.
func TestDoJSONTest_NoChoices(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	h := &Handler{}
	res, _ := h.doJSONTest(context.Background(), upstream.Client(),
		upstream.URL, []byte(`{"model":"x"}`), "", "llm")

	if res.OK {
		t.Fatalf("expected failure for empty choices")
	}
}

// TestDoJSONTest_Embedding verifies embedding data detection.
func TestDoJSONTest_Embedding(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer upstream.Close()

	h := &Handler{}
	res, _ := h.doJSONTest(context.Background(), upstream.Client(),
		upstream.URL, []byte(`{"model":"x"}`), "", "embedding")

	if !res.OK {
		t.Fatalf("expected embedding OK, got error: %s", res.Error)
	}
}

// TestDoJSONTest_Image verifies image data detection.
func TestDoJSONTest_Image(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"url":"https://example.com/a.png"}]}`))
	}))
	defer upstream.Close()

	h := &Handler{}
	res, _ := h.doJSONTest(context.Background(), upstream.Client(),
		upstream.URL, []byte(`{"model":"x"}`), "", "image")

	if !res.OK {
		t.Fatalf("expected image OK, got error: %s", res.Error)
	}
}

// TestDoJSONTest_ProviderErrorInside200 verifies an error field smuggled into a
// 200 response is surfaced as a failure.
func TestDoJSONTest_ProviderErrorInside200(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"message":"quota exhausted"}}`))
	}))
	defer upstream.Close()

	h := &Handler{}
	res, _ := h.doJSONTest(context.Background(), upstream.Client(),
		upstream.URL, []byte(`{"model":"x"}`), "", "llm")

	if res.OK || !strings.Contains(res.Error, "quota") {
		t.Fatalf("expected quota error, got OK=%v err=%q", res.OK, res.Error)
	}
}

// TestHandleTestProviderModels_NoModels verifies the batch endpoint reports a
// 400 when a provider has nothing configured.
func TestHandleTestProviderModels_NoModels(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	req := httptest.NewRequest(http.MethodPost,
		"/api/dashboard/providers/unknown-provider-xyz/test-models",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for no models, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestHandleTestProviderModels_ExplicitModelsMissingConn exercises the
// explicit-models path: the handler should still run and return per-model
// results (all failing) without panicking when no connection exists.
func TestHandleTestProviderModels_ExplicitModelsMissingConn(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	// Point the self-call at a dead port so tests never touch the network.
	t.Setenv("PORT", "1")

	req := httptest.NewRequest(http.MethodPost,
		"/api/dashboard/providers/antigravity/test-models",
		strings.NewReader(`{"models":["ag/does-not-exist"],"parallel":true}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Results []modelTestResult `json:"results"`
		Summary map[string]int    `json:"summary"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(payload.Results))
	}
	// ModelID must echo the exact request string so the UI can match it.
	if payload.Results[0].ModelID != "ag/does-not-exist" {
		t.Fatalf("expected echoed model id, got %q", payload.Results[0].ModelID)
	}
}

// TestDedupeStrings verifies order-preserving de-duplication of non-empty
// strings.
func TestDedupeStrings(t *testing.T) {
	got := dedupeStrings([]string{"a", "b", "a", "", "c", "b"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

// TestTruncate verifies truncation adds an ellipsis only when cutting.
func TestTruncate(t *testing.T) {
	if truncate("short", 10) != "short" {
		t.Fatal("short string should be unchanged")
	}
	if truncate("abcdefghij", 4) != "abcd…" {
		t.Fatalf("unexpected truncation: %q", truncate("abcdefghij", 4))
	}
}
