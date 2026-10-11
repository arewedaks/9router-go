package chat

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// The prefix rule strips `store` before it ever reaches the upstream, so the
// repair path is exercised with a parameter no rule covers — a vendor-specific
// field a client added that only the upstream knows is unwanted.
func TestForwardRequestRepairsRejectedParam(t *testing.T) {
	var attempts int
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if strings.Contains(string(raw), "\"service_tier\"") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"Unrecognized request arguments supplied: service_tier","type":"invalid_request_error"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	h := &ChatHandler{Client: srv.Client()}
	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := []byte(`{"model":"solar-pro4","service_tier":"auto","messages":[{"role":"user","content":"hi"}]}`)

	rec := httptest.NewRecorder()
	provider := "openai-compatible-chat-4427a7c2-1329-4aa1-bddc-6bcb581f29b8"
	if err := h.forwardRequest(context.Background(), rec, provider, cfg, "k", body, false, false, nil); err != nil {
		t.Fatalf("forward failed: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("the repaired request must succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	if attempts != 2 {
		t.Errorf("expected original + repair, got %d attempts", attempts)
	}
	if len(bodies) == 2 {
		if !strings.Contains(bodies[0], "\"service_tier\"") {
			t.Error("first attempt must carry service_tier")
		}
		if strings.Contains(bodies[1], "\"service_tier\"") {
			t.Error("the retry must not carry service_tier")
		}
		// The repair must not have touched anything else.
		var m map[string]any
		if err := json.Unmarshal([]byte(bodies[1]), &m); err != nil {
			t.Fatalf("retry body is not JSON: %v", err)
		}
		if m["model"] != "solar-pro4" {
			t.Errorf("repair must not change the model, got %v", m["model"])
		}
	}
}

// An upstream that caps images per request must not fail the call: the excess is
// trimmed and the request retried.
func TestForwardRequestTrimsExcessImages(t *testing.T) {
	var attempts int
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		raw, _ := io.ReadAll(r.Body)
		lastBody = string(raw)
		if strings.Count(string(raw), `"type":"image_url"`) > 2 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"This request carries 3 images; m takes at most 2 per request.","type":"invalid_request_error","code":"bad_request","param":"messages"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	h := &ChatHandler{Client: srv.Client()}
	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	img := `{"type":"image_url","image_url":{"url":"x"}}`
	body := []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":[` + img + `,` + img + `,` + img + `]}]}`)

	rec := httptest.NewRecorder()
	provider := "openai-compatible-chat-5ec3404c-7e60-4eab-a9f5-c7d8f4723a87"
	if err := h.forwardRequest(context.Background(), rec, provider, cfg, "k", body, false, false, nil); err != nil {
		t.Fatalf("forward failed: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("the trimmed request must succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	if attempts != 2 {
		t.Errorf("expected original + repair, got %d attempts", attempts)
	}
	if n := strings.Count(lastBody, `"type":"image_url"`); n != 2 {
		t.Errorf("the retry must carry 2 images, got %d", n)
	}
	// The conversation must survive the trim.
	if !strings.Contains(lastBody, `"messages"`) || !strings.Contains(lastBody, `"model":"glm-5.3-flash"`) {
		t.Errorf("trimming images must not damage the request, got %s", lastBody)
	}
}

// The prefix rule covers custom nodes even when the upstream does not complain:
// the field never reaches it in the first place.
func TestForwardRequestAppliesPrefixRule(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	h := &ChatHandler{Client: srv.Client()}
	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := []byte(`{"model":"solar-pro4","store":false,"stream":true,"messages":[]}`)

	rec := httptest.NewRecorder()
	provider := "openai-compatible-chat-136352c4-5b91-4ce6-ba56-aa94c7fe946c"
	if err := h.forwardRequest(context.Background(), rec, provider, cfg, "k", body, false, false, nil); err != nil {
		t.Fatalf("forward failed: %v", err)
	}

	if strings.Contains(got, "\"store\"") {
		t.Errorf("store must be stripped before reaching a custom node, got %s", got)
	}
	if !strings.Contains(got, "\"stream\"") {
		t.Errorf("unrelated parameters must survive, got %s", got)
	}
}
