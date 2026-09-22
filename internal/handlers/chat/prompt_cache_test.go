package chat

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/promptcache"
)

func TestPromptCache_HitAndMiss(t *testing.T) {
	h := &ChatHandler{
		PromptCache: promptcache.New(100, 10*time.Minute),
	}

	reqBody := []byte(`{"model":"test-model","messages":[{"role":"user","content":"Hello world"}]}`)

	// First call: cache miss
	rec1 := httptest.NewRecorder()
	hit1 := h.tryServeFromCache(rec1, "test-model", reqBody, false)
	if hit1 {
		t.Fatal("expected first call to be a cache miss")
	}

	// Simulate successful upstream response and caching
	hash, ok := promptcache.HashRequest("test-model", reqBody)
	if !ok {
		t.Fatalf("failed to calculate hash")
	}

	cw := newCachingResponseWriter(rec1, "test-model", hash, h.PromptCache, false)
	cw.Header().Set("Content-Type", "application/json")
	cw.WriteHeader(http.StatusOK)
	cw.Write([]byte(`{"id":"cmpl-123","choices":[{"message":{"content":"Hi there!"}}]}`))
	cw.Finalize()

	// Second call: must be a cache hit
	rec2 := httptest.NewRecorder()
	hit2 := h.tryServeFromCache(rec2, "test-model", reqBody, false)
	if !hit2 {
		t.Fatalf("expected second call to be a cache hit")
	}

	if rec2.Header().Get("X-9Router-Cache") != "HIT" {
		t.Errorf("expected X-9Router-Cache: HIT, got %q", rec2.Header().Get("X-9Router-Cache"))
	}

	wantBody := `{"id":"cmpl-123","choices":[{"message":{"content":"Hi there!"}}]}`
	if !bytes.Equal(rec2.Body.Bytes(), []byte(wantBody)) {
		t.Errorf("cached body mismatch:\n got:  %s\n want: %s", rec2.Body.String(), wantBody)
	}
}

func TestPromptCache_StreamingReplay(t *testing.T) {
	h := &ChatHandler{
		PromptCache: promptcache.New(100, 10*time.Minute),
	}

	reqBody := []byte(`{"model":"stream-model","stream":true,"messages":[{"role":"user","content":"Stream test"}]}`)
	hash, _ := promptcache.HashRequest("stream-model", reqBody)

	rec1 := httptest.NewRecorder()
	cw := newCachingResponseWriter(rec1, "stream-model", hash, h.PromptCache, true)
	cw.Header().Set("Content-Type", "text/event-stream")
	cw.WriteHeader(http.StatusOK)

	chunks := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\" World\"}}]}\n\n",
		"data: [DONE]\n\n",
	}

	for _, c := range chunks {
		cw.Write([]byte(c))
	}
	cw.Finalize()

	// Second call (client stream request) -> instant replay from cache
	rec2 := httptest.NewRecorder()
	if !h.tryServeFromCache(rec2, "stream-model", reqBody, true) {
		t.Fatal("expected streaming cache hit")
	}

	if rec2.Header().Get("X-9Router-Cache") != "HIT" {
		t.Errorf("expected X-9Router-Cache: HIT, got %q", rec2.Header().Get("X-9Router-Cache"))
	}
	if rec2.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type: text/event-stream, got %q", rec2.Header().Get("Content-Type"))
	}

	got := rec2.Body.String()
	want := strings.Join(chunks, "")
	if got != want {
		t.Errorf("stream replay mismatch:\n got:  %q\n want: %q", got, want)
	}
}
