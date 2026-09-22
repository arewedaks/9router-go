package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/promptcache"
)

func TestPromptCache_StatsAndClear(t *testing.T) {
	h := &ChatHandler{
		PromptCache: promptcache.New(100, 10*time.Minute),
	}

	// Populate one item
	h.PromptCache.Set("dummy-hash", "test-model", "application/json", 200, []byte("{}"), false)

	// Check /api/cache/stats
	req := httptest.NewRequest(http.MethodGet, "/api/cache/stats", nil)
	rec := httptest.NewRecorder()
	h.HandleCacheStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"enabled":true`) || !strings.Contains(body, `"count":1`) {
		t.Errorf("unexpected stats response: %s", body)
	}

	// Call /api/cache/clear
	reqClear := httptest.NewRequest(http.MethodPost, "/api/cache/clear", nil)
	recClear := httptest.NewRecorder()
	h.HandleCacheClear(recClear, reqClear)

	if recClear.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on clear, got %d", recClear.Code)
	}

	// Verify stats count is now 0
	recAfter := httptest.NewRecorder()
	h.HandleCacheStats(recAfter, req)
	if !strings.Contains(recAfter.Body.String(), `"count":0`) {
		t.Errorf("expected count 0 after clear, got: %s", recAfter.Body.String())
	}
}
