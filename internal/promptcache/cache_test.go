package promptcache

import (
	"testing"
	"time"
)

func TestHashRequest_Deterministic(t *testing.T) {
	req1 := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"temperature":0.7}`)
	req2 := []byte(`{"temperature":0.7,"messages":[{"role":"user","content":"hello"}],"model":"gpt-4o"}`)

	h1, ok1 := HashRequest("gpt-4o", req1)
	h2, ok2 := HashRequest("gpt-4o", req2)

	if !ok1 || !ok2 {
		t.Fatalf("HashRequest failed: ok1=%v, ok2=%v", ok1, ok2)
	}

	if h1 != h2 {
		t.Errorf("expected deterministic hash across different key orders: %s != %s", h1, h2)
	}
}

func TestHashRequest_DifferentModelsDiverge(t *testing.T) {
	req := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	h1, _ := HashRequest("gpt-4o", req)
	h2, _ := HashRequest("claude-3-5-sonnet", req)

	if h1 == h2 {
		t.Errorf("expected distinct hashes for different models, got identical %s", h1)
	}
}

func TestCache_LRUAndTTL(t *testing.T) {
	c := New(2, 50*time.Millisecond)

	c.Set("k1", "m", "text/plain", 200, []byte("body1"), false)
	c.Set("k2", "m", "text/plain", 200, []byte("body2"), false)

	// Access k1 to make k2 the least recently used
	if _, ok := c.Get("k1"); !ok {
		t.Fatal("expected k1 to be present")
	}

	// Add k3 -> k2 should be evicted
	c.Set("k3", "m", "text/plain", 200, []byte("body3"), false)

	if _, ok := c.Get("k2"); ok {
		t.Errorf("expected k2 to be evicted by LRU capacity")
	}
	if _, ok := c.Get("k1"); !ok {
		t.Errorf("expected k1 to remain present")
	}

	// Test TTL expiration
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get("k1"); ok {
		t.Errorf("expected k1 to be expired after TTL")
	}
}
