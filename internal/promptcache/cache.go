package promptcache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"sync"
	"time"
)

// Entry stores the cached response for an identical prompt request.
type Entry struct {
	Key         string
	Model       string
	ContentType string
	StatusCode  int
	Body        []byte
	IsStream    bool
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Cache is a thread-safe, memory-bounded LRU cache for prompt responses.
type Cache struct {
	mu       sync.RWMutex
	capacity int
	ttl      time.Duration
	items    map[string]*list.Element
	evict    *list.List
	hits     uint64
	misses   uint64
}

// Config configures the prompt cache.
type Config struct {
	Enabled  bool          `json:"enabled"`
	Capacity int           `json:"capacity"`
	TTL      time.Duration `json:"ttl"`
}

// DefaultConfig returns safe, low-overhead defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:  true,
		Capacity: 500,
		TTL:      15 * time.Minute,
	}
}

// New creates a new prompt cache with the specified capacity and TTL.
func New(capacity int, ttl time.Duration) *Cache {
	if capacity <= 0 {
		capacity = 500
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &Cache{
		capacity: capacity,
		ttl:      ttl,
		items:    make(map[string]*list.Element),
		evict:    list.New(),
	}
}


// HashRequest calculates a deterministic SHA-256 hash for the given request payload.
func HashRequest(model string, body []byte) (string, bool) {
	var raw struct {
		Messages    []map[string]any `json:"messages"`
		System      any              `json:"system"`
		Tools       []map[string]any `json:"tools"`
		Temperature *float64         `json:"temperature"`
		TopP        *float64         `json:"top_p"`
		MaxTokens   *int64           `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", false
	}

	// Normalise messages into re-encoded canonical byte stream
	canon := struct {
		Model       string           `json:"model"`
		Messages    []map[string]any `json:"messages,omitempty"`
		System      any              `json:"system,omitempty"`
		Tools       []map[string]any `json:"tools,omitempty"`
		Temperature *float64         `json:"temperature,omitempty"`
		TopP        *float64         `json:"top_p,omitempty"`
		MaxTokens   *int64           `json:"max_tokens,omitempty"`
	}{
		Model:       model,
		Messages:    raw.Messages,
		System:      raw.System,
		Tools:       raw.Tools,
		Temperature: raw.Temperature,
		TopP:        raw.TopP,
		MaxTokens:   raw.MaxTokens,
	}

	b, err := json.Marshal(canon)
	if err != nil {
		return "", false
	}

	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), true
}

// Get retrieves an unexpired entry by its hash key.
func (c *Cache) Get(key string) (*Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		c.misses++
		return nil, false
	}

	entry := elem.Value.(*Entry)
	if time.Now().After(entry.ExpiresAt) {
		c.evictElement(elem)
		c.misses++
		return nil, false
	}

	c.evict.MoveToFront(elem)
	c.hits++
	return entry, true
}

// Set adds or updates an entry in the cache.
func (c *Cache) Set(key string, model, contentType string, statusCode int, body []byte, isStream bool) {
	if key == "" || len(body) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	// Update existing
	if elem, ok := c.items[key]; ok {
		c.evict.MoveToFront(elem)
		entry := elem.Value.(*Entry)
		entry.Model = model
		entry.ContentType = contentType
		entry.StatusCode = statusCode
		entry.Body = body
		entry.IsStream = isStream
		entry.CreatedAt = now
		entry.ExpiresAt = now.Add(c.ttl)
		return
	}

	// Evict oldest if full
	for c.evict.Len() >= c.capacity {
		c.evictOldest()
	}

	entry := &Entry{
		Key:         key,
		Model:       model,
		ContentType: contentType,
		StatusCode:  statusCode,
		Body:        body,
		IsStream:    isStream,
		CreatedAt:   now,
		ExpiresAt:   now.Add(c.ttl),
	}

	elem := c.evict.PushFront(entry)
	c.items[key] = elem
}

// Clear flushes all entries from the cache.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element)
	c.evict.Init()
}

// Stats returns the current cache metrics.
func (c *Cache) Stats() (len int, hits, misses uint64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.evict.Len(), c.hits, c.misses
}

func (c *Cache) evictOldest() {
	elem := c.evict.Back()
	if elem != nil {
		c.evictElement(elem)
	}
}

func (c *Cache) evictElement(elem *list.Element) {
	c.evict.Remove(elem)
	entry := elem.Value.(*Entry)
	delete(c.items, entry.Key)
}
