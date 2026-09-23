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

// HashRequest must be deterministic for a given input. It previously was not:
// the messages were decoded into []map[string]any and re-marshalled, and since
// Go randomises map iteration (and json/v2 does not sort object keys) the same
// body produced two different digests about 15% of the time. The cache then
// missed on requests that should have hit, silently defeating the feature.
//
// 200 iterations is enough to catch it: the original implementation produced
// both hashes within a handful of calls.
func TestHashRequest_DeterministicAcrossRepeatedCalls(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"Hello world"}]}`)

	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		h, ok := HashRequest("m", body)
		if !ok {
			t.Fatal("HashRequest failed")
		}
		seen[h]++
	}
	if len(seen) != 1 {
		t.Fatalf("HashRequest produced %d distinct hashes for one input: %v", len(seen), seen)
	}
}

// A body with no messages cannot be cached; returning a hash for it would let
// every malformed request collide onto a single entry.
func TestHashRequest_RejectsBodyWithoutMessages(t *testing.T) {
	if _, ok := HashRequest("m", []byte(`{"model":"m"}`)); ok {
		t.Error("expected no hash for a body without messages")
	}
}

// Fields that change the answer must change the digest; fields that do not are
// deliberately excluded so they cannot split the cache.
func TestHashRequest_SensitiveToAnswerAffectingFields(t *testing.T) {
	base := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`

	variants := map[string]string{
		"different temperature": `{"model":"m","messages":[{"role":"user","content":"hi"}],"temperature":0.9}`,
		"different tools":       `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`,
		"different system":      `{"model":"m","messages":[{"role":"user","content":"hi"}],"system":"be terse"}`,
		"different messages":    `{"model":"m","messages":[{"role":"user","content":"bye"}]}`,
	}

	baseHash, ok := HashRequest("m", []byte(base))
	if !ok {
		t.Fatal("base hash failed")
	}
	for name, body := range variants {
		got, ok := HashRequest("m", []byte(body))
		if !ok {
			t.Fatalf("%s: hash failed", name)
		}
		if got == baseHash {
			t.Errorf("%s produced the same hash as the base request; the cache would serve a wrong answer", name)
		}
	}

	// An ignored field must NOT split the cache.
	withIgnored, ok := HashRequest("m", []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"user":"abc"}`))
	if !ok {
		t.Fatal("ignored-field hash failed")
	}
	if withIgnored != baseHash {
		t.Error("stream/user changed the hash; these do not affect the answer and should share a cache entry")
	}
}
