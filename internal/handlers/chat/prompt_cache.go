package chat

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/promptcache"
)

// tryServeFromCache checks if the exact request has a valid cached entry.
// If found, it replays the response immediately with 0 ms latency and sets
// the X-9Router-Cache: HIT header.
func (h *ChatHandler) tryServeFromCache(w http.ResponseWriter, model string, body []byte, isStream bool) bool {
	if h.PromptCache == nil {
		return false
	}

	hash, ok := promptcache.HashRequest(model, body)
	if !ok {
		return false
	}

	entry, found := h.PromptCache.Get(hash)
	if !found {
		return false
	}

	// Serve cache hit
	w.Header().Set("X-9Router-Cache", "HIT")
	w.Header().Set("X-9Router-Cache-Age", fmt.Sprintf("%.1fs", time.Since(entry.CreatedAt).Seconds()))
	if entry.ContentType != "" {
		w.Header().Set("Content-Type", entry.ContentType)
	}

	statusCode := entry.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	w.WriteHeader(statusCode)

	w.Write(entry.Body)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	log.Info("promptcache", "cache hit served", "model", model, "hash", hash[:12], "bytes", len(entry.Body), "isStream", isStream)
	return true
}

// cachingResponseWriter intercepts and records successful HTTP 200 responses
// so they can be placed into the prompt cache once the upstream stream or payload completes.
type cachingResponseWriter struct {
	http.ResponseWriter
	model       string
	hash        string
	cache       *promptcache.Cache
	isStream    bool
	buf         bytes.Buffer
	statusCode  int
	contentType string
	committed   bool
}

func newCachingResponseWriter(w http.ResponseWriter, model, hash string, cache *promptcache.Cache, isStream bool) *cachingResponseWriter {
	return &cachingResponseWriter{
		ResponseWriter: w,
		model:          model,
		hash:           hash,
		cache:          cache,
		isStream:       isStream,
		statusCode:     http.StatusOK,
	}
}

func (c *cachingResponseWriter) WriteHeader(code int) {
	c.statusCode = code
	c.committed = true
	c.ResponseWriter.Header().Set("X-9Router-Cache", "MISS")
	c.ResponseWriter.WriteHeader(code)
}

func (c *cachingResponseWriter) Write(b []byte) (int, error) {
	if !c.committed {
		c.WriteHeader(http.StatusOK)
	}
	if c.contentType == "" {
		c.contentType = c.ResponseWriter.Header().Get("Content-Type")
	}
	// Only buffer successful 2xx responses up to a reasonable bound (e.g. 5MB)
	if c.statusCode >= 200 && c.statusCode < 300 && c.buf.Len()+len(b) <= 5*1024*1024 {
		c.buf.Write(b)
	}
	return c.ResponseWriter.Write(b)
}

func (c *cachingResponseWriter) Flush() {
	if flusher, ok := c.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Finalize records the cached payload if the response succeeded without error.
func (c *cachingResponseWriter) Finalize() {
	if c.cache == nil || c.hash == "" || c.buf.Len() == 0 {
		return
	}
	if c.statusCode >= 200 && c.statusCode < 300 {
		c.cache.Set(c.hash, c.model, c.contentType, c.statusCode, c.buf.Bytes(), c.isStream)
		log.Debug("promptcache", "cached response", "model", c.model, "hash", c.hash[:12], "bytes", c.buf.Len(), "isStream", c.isStream)
	}
}

// HandleCacheStats returns the current statistics of the in-memory prompt cache.
func (h *ChatHandler) HandleCacheStats(w http.ResponseWriter, r *http.Request) {
	if h.PromptCache == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"enabled":false}`))
		return
	}

	count, hits, misses := h.PromptCache.Stats()
	total := hits + misses
	var hitRatio float64
	if total > 0 {
		hitRatio = float64(hits) / float64(total) * 100.0
	}

	resp := map[string]any{
		"enabled":  true,
		"count":    count,
		"hits":     hits,
		"misses":   misses,
		"hitRate":  fmt.Sprintf("%.1f%%", hitRatio),
	}

	w.Header().Set("Content-Type", "application/json")
	data, _ := json.Marshal(resp)
	w.Write(data)
}

// HandleCacheClear empties the prompt cache.
func (h *ChatHandler) HandleCacheClear(w http.ResponseWriter, r *http.Request) {
	if h.PromptCache != nil {
		h.PromptCache.Clear()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok","cleared":true}`))
}
