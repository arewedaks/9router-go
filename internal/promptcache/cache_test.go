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

// The entry count is not a memory bound. A production deployment reached 194 MB
// of retained response bodies — 92% of the process heap — with the cache "full"
// at its entry limit and a 0.5% hit rate. The byte budget is what makes the
// footprint predictable, so it is asserted directly.
func TestCacheEnforcesByteBudget(t *testing.T) {
	c := NewWithConfig(Config{Capacity: 1000, TTL: time.Minute, MaxBytes: 1000, MaxEntryBytes: 400})

	for i := 0; i < 10; i++ {
		c.Set(string(rune('a'+i)), "m", "text/plain", 200, make([]byte, 300), false)
	}

	_, bytes, maxBytes, _, _ := c.StatsDetail()
	if bytes > maxBytes {
		t.Errorf("retained %d bytes with a %d byte budget", bytes, maxBytes)
	}
	if entries, _, _, _, _ := c.StatsDetail(); entries > 3 {
		t.Errorf("retained %d entries of 300 bytes under a 1000 byte budget", entries)
	}
}

// A response larger than the per-entry cap must be rejected outright: storing it
// would evict every useful entry to make room for data unlikely to repeat.
func TestCacheRejectsOversizedEntry(t *testing.T) {
	c := NewWithConfig(Config{Capacity: 10, TTL: time.Minute, MaxBytes: 1 << 20, MaxEntryBytes: 100})

	c.Set("small", "m", "text/plain", 200, make([]byte, 50), false)
	c.Set("huge", "m", "text/plain", 200, make([]byte, 500), false)

	if _, ok := c.Get("huge"); ok {
		t.Error("an entry above the per-entry cap was stored")
	}
	if _, ok := c.Get("small"); !ok {
		t.Error("the oversized entry evicted a valid one")
	}
}

// Replacing an entry has to correct the byte total, or the budget would drift
// upward on every update and eventually evict a cache that is mostly empty.
func TestCacheByteTotalTracksReplacement(t *testing.T) {
	c := NewWithConfig(Config{Capacity: 10, TTL: time.Minute, MaxBytes: 1 << 20, MaxEntryBytes: 1 << 20})

	c.Set("k", "m", "text/plain", 200, make([]byte, 1000), false)
	_, afterFirst, _, _, _ := c.StatsDetail()

	c.Set("k", "m", "text/plain", 200, make([]byte, 100), false)
	entries, afterReplace, _, _, _ := c.StatsDetail()

	if entries != 1 {
		t.Fatalf("entries = %d, want 1", entries)
	}
	if afterReplace != afterFirst-900 {
		t.Errorf("bytes = %d after replacing 1000 with 100 (was %d), want %d",
			afterReplace, afterFirst, afterFirst-900)
	}
}

// Expired entries must be released by the cache itself, not only when a lookup
// happens to hit them. Eviction driven purely by capacity pins memory on an idle
// server: nothing arrives to push the old entries out.
func TestCacheSweepsExpiredEntries(t *testing.T) {
	c := NewWithConfig(Config{Capacity: 1000, TTL: 20 * time.Millisecond, MaxBytes: 1 << 20, MaxEntryBytes: 1 << 20})

	for i := 0; i < 5; i++ {
		c.Set(string(rune('a'+i)), "m", "text/plain", 200, make([]byte, 100), false)
	}
	time.Sleep(30 * time.Millisecond)

	// Force the sweep window open: the interval exists so a write does not walk
	// the list on every request.
	c.mu.Lock()
	c.lastSweep = time.Now().Add(-2 * sweepInterval)
	c.mu.Unlock()

	c.Set("fresh", "m", "text/plain", 200, make([]byte, 100), false)

	entries, bytes, _, _, _ := c.StatsDetail()
	if entries != 1 {
		t.Errorf("entries = %d after a sweep, want only the fresh one", entries)
	}
	if bytes != 100 {
		t.Errorf("bytes = %d after a sweep, want 100", bytes)
	}
}

// Clear must reset the byte total, or a cleared cache would still report a full
// budget and evict the first entry written after it.
func TestCacheClearResetsBytes(t *testing.T) {
	c := NewWithConfig(Config{Capacity: 10, TTL: time.Minute, MaxBytes: 1 << 20, MaxEntryBytes: 1 << 20})
	c.Set("k", "m", "text/plain", 200, make([]byte, 500), false)
	c.Clear()

	if _, bytes, _, _, _ := c.StatsDetail(); bytes != 0 {
		t.Errorf("bytes = %d after Clear, want 0", bytes)
	}
	if entries, _, _, _, _ := c.StatsDetail(); entries != 0 {
		t.Errorf("entries = %d after Clear, want 0", entries)
	}
}

// A per-entry cap above the total budget would let one response evict the whole
// cache, so the two limits are reconciled at construction.
func TestCacheClampsEntryCapToBudget(t *testing.T) {
	c := NewWithConfig(Config{Capacity: 10, TTL: time.Minute, MaxBytes: 1000, MaxEntryBytes: 1 << 20})
	if got := c.MaxEntryBytes(); got > 1000 {
		t.Errorf("MaxEntryBytes() = %d, want it clamped to the 1000 byte budget", got)
	}
}
