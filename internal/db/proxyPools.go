package db

import (
	"crypto/rand"
	json "encoding/json/v2"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/handlerutil"
)

// ProxyPool represents a pool of proxy URLs for routing requests.
type ProxyPool struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	IsActive    bool     `json:"isActive"`
	URLs        []string `json:"urls"`
	Strategy    string   `json:"strategy"` // "round-robin" or "random"
	Type        string   `json:"type"`     // "http", "vercel", "cloudflare", "deno"
	NoProxy     string   `json:"noProxy"`
	StrictProxy bool     `json:"strictProxy"`
	index       uint64   // atomic counter for round-robin
}

var proxyPoolCache sync.Map // map[string]*ProxyPool

// GetProxyPool reads a proxy pool from the proxyPools table.
func (r *Repo) GetProxyPool(poolID string) (*ProxyPool, error) {
	var data string
	var isActive int
	err := r.db.QueryRow(
		`SELECT data, isActive FROM proxyPools WHERE id = ?`, poolID,
	).Scan(&data, &isActive)
	if err != nil {
		return nil, fmt.Errorf("proxy pool %s: %w", poolID, err)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("parse pool data: %w", err)
	}

	pool := &ProxyPool{
		ID:       poolID,
		IsActive: isActive == 1,
		Name:     handlerutil.GetString(raw, "name"),
		Strategy: handlerutil.GetString(raw, "strategy"),
	}

	if urls, ok := raw["urls"].([]any); ok {
		for _, u := range urls {
			if s, ok := u.(string); ok && s != "" {
				pool.URLs = append(pool.URLs, s)
			}
		}
	}
	if len(pool.URLs) == 0 {
		if singleURL := handlerutil.GetString(raw, "proxyUrl"); singleURL != "" {
			pool.URLs = append(pool.URLs, singleURL)
		}
	}

	pool.Type = handlerutil.GetString(raw, "type")
	if pool.Type == "" {
		pool.Type = "http"
	}
	pool.NoProxy = handlerutil.GetString(raw, "noProxy")
	if sp, ok := raw["strictProxy"].(bool); ok {
		pool.StrictProxy = sp
	}

	if pool.Strategy == "" {
		pool.Strategy = "round-robin"
	}

	if cached, ok := proxyPoolCache.Load(poolID); ok {
		return cached.(*ProxyPool), nil
	}
	// Store the freshly-read pool. If another goroutine won the race, return
	// its value instead of overwriting — never mutate a value in the cache,
	// since concurrent readers use it lock-free via NextURL.
	actual, _ := proxyPoolCache.LoadOrStore(poolID, pool)
	return actual.(*ProxyPool), nil
}

// NextURL returns the next proxy URL using round-robin selection.
func (p *ProxyPool) NextURL() string {
	if len(p.URLs) == 0 {
		return ""
	}
	idx := atomic.AddUint64(&p.index, 1)
	return p.URLs[idx%uint64(len(p.URLs))]
}

// ProxyPoolData is the JSON payload stored in the proxyPools.data column for
// deploy-type pools. Field order matches what the Next.js dashboard writes so
// the shared DB stays byte-compatible.
type ProxyPoolData struct {
	Name         string  `json:"name"`
	ProxyURL     string  `json:"proxyUrl"`
	NoProxy      string  `json:"noProxy"`
	Type         string  `json:"type"`
	StrictProxy  bool    `json:"strictProxy"`
	LastTestedAt *string `json:"lastTestedAt"`
	LastError    *string `json:"lastError"`
}

// InsertProxyPool inserts a new proxy pool row and returns the pool object in
// the same shape as the dashboard's createProxyPool result.
func (r *Repo) InsertProxyPool(d ProxyPoolData) (map[string]any, error) {
	id := randomID()
	now := time.Now().UTC().Format(time.RFC3339)
	dataBytes, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshal pool data: %w", err)
	}
	_, err = r.db.Exec(
		`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		id, 1, "unknown", string(dataBytes), now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert proxy pool: %w", err)
	}
	return map[string]any{
		"id":           id,
		"name":         d.Name,
		"proxyUrl":     d.ProxyURL,
		"noProxy":      d.NoProxy,
		"type":         d.Type,
		"strictProxy":  d.StrictProxy,
		"isActive":     true,
		"testStatus":   "unknown",
		"lastTestedAt": nil,
		"lastError":    nil,
		"createdAt":    now,
		"updatedAt":    now,
	}, nil
}

// ProxyPoolSummary is the dashboard-facing view of a pool. It carries just
// enough for the providerStrategies picker: id, name, type and whether the pool
// has a usable URL. Deliberately omits credentials embedded in proxy URLs.
type ProxyPoolSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	IsActive   bool   `json:"isActive"`
	HasURL     bool   `json:"hasUrl"`
	URLCount   int    `json:"urlCount"`
	TestStatus string `json:"testStatus,omitempty"`
}

// ListProxyPools returns every pool, newest name-first, optionally restricted to
// active pools that actually carry a URL (the eligibility rule used by proxy
// rotation). Mirrors VansRouter's getProxyPools({ isActive }) shape.
func (r *Repo) ListProxyPools(onlyActiveWithURL bool) ([]ProxyPoolSummary, error) {
	rows, err := r.db.Query(`SELECT id, isActive, testStatus, data FROM proxyPools ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list proxy pools: %w", err)
	}
	defer rows.Close()

	var out []ProxyPoolSummary
	for rows.Next() {
		var id, data string
		var isActive int
		var testStatus *string
		if err := rows.Scan(&id, &isActive, &testStatus, &data); err != nil {
			return nil, fmt.Errorf("scan proxy pool: %w", err)
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			continue // Skip unparseable rows rather than failing the whole list.
		}
		sum := ProxyPoolSummary{
			ID:       id,
			Name:     handlerutil.GetString(raw, "name"),
			Type:     handlerutil.GetString(raw, "type"),
			IsActive: isActive == 1,
		}
		if testStatus != nil {
			sum.TestStatus = *testStatus
		}
		if sum.Type == "" {
			sum.Type = "http"
		}
		if urls, ok := raw["urls"].([]any); ok {
			for _, u := range urls {
				if s, ok := u.(string); ok && s != "" {
					sum.URLCount++
				}
			}
		}
		if sum.URLCount == 0 && handlerutil.GetString(raw, "proxyUrl") != "" {
			sum.URLCount = 1
		}
		sum.HasURL = sum.URLCount > 0
		if onlyActiveWithURL && (!sum.IsActive || !sum.HasURL) {
			continue
		}
		out = append(out, sum)
	}
	return out, rows.Err()
}

// EligibleProxyPoolIDs returns the ids of active pools that carry a URL, in a
// stable order. This is the candidate set for dynamic rotation.
func (r *Repo) EligibleProxyPoolIDs() ([]string, error) {
	pools, err := r.ListProxyPools(true)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(pools))
	for _, p := range pools {
		ids = append(ids, p.ID)
	}
	return ids, nil
}

// poolCursors tracks round-robin position per (provider, strategy) key so that
// consecutive requests rotate through the eligible set. Mirrors VansRouter's
// module-level _poolCursors in src/lib/network/connectionProxy.js.
var (
	poolCursorsMu sync.Mutex
	poolCursors   = map[string]uint64{}
)

// PickProxyPoolID chooses a pool for dynamic rotation. `eligible` is the
// candidate set (active pools with URLs); `targets`, when non-empty, narrows it
// to those ids. Returns "" when nothing is eligible, which callers treat as
// "direct connection".
//
// Strategies mirror VansRouter: "fill-first" (and any unknown) takes the first
// eligible pool; "round-robin" and "smart" advance a per-key cursor; "random"
// picks uniformly. "none" is handled by callers before reaching here.
func PickProxyPoolID(eligible, targets []string, strategy, providerID string) string {
	candidates := filterTargetPoolIDs(eligible, targets)
	if len(candidates) == 0 {
		// A target list that matches nothing still falls back to the full set,
		// so a stale subset never silently disables proxying.
		candidates = eligible
	}
	if len(candidates) == 0 {
		return ""
	}

	switch strings.ToLower(strategy) {
	case "round-robin", "smart", "sticky":
		key := providerID + ":" + strings.ToLower(strategy) + ":" + strings.Join(candidates, ",")
		poolCursorsMu.Lock()
		idx := poolCursors[key] % uint64(len(candidates))
		poolCursors[key] = (idx + 1) % uint64(len(candidates))
		poolCursorsMu.Unlock()
		return candidates[idx]
	case "random":
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(candidates))))
		if err != nil {
			return candidates[0]
		}
		return candidates[n.Int64()]
	default:
		return candidates[0]
	}
}

// filterTargetPoolIDs keeps only eligible ids present in targets. An empty
// targets list means "no restriction".
func filterTargetPoolIDs(eligible, targets []string) []string {
	if len(targets) == 0 {
		return eligible
	}
	allowed := make(map[string]bool, len(targets))
	for _, t := range targets {
		allowed[t] = true
	}
	out := make([]string, 0, len(eligible))
	for _, id := range eligible {
		if allowed[id] {
			out = append(out, id)
		}
	}
	return out
}

// randomID returns a random hex id (uuid-shaped, like the dashboard's uuidv4).
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("pool-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}
