package chat

import (
	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	internalproxy "9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
	json "encoding/json/v2"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// CredentialFallbacks maps search/tool providers to the primary chat provider whose API key can be reused.
var CredentialFallbacks = map[string]string{
	"ollama-search": "ollama",
	"zai-search":    "glm",
	"cline":         "clinepass",
	"clinepass":     "cline",
}
var (
	proxyClientsMu sync.RWMutex
	proxyClients   = make(map[string]*http.Client)
)

// GetBestConnection retrieves the highest-priority active connection for a provider.
// When connectionID is non-empty, it fetches that specific connection directly.
func (h *ChatHandler) GetBestConnection(provider string, connectionID string, excludeIDs []string, model string) (*models.ProviderConnection, *ConnectionData, error) {
	return h.getBestConnection(provider, connectionID, excludeIDs, model)
}

func (h *ChatHandler) getBestConnection(provider string, connectionID string, excludeIDs []string, model string) (*models.ProviderConnection, *ConnectionData, error) {
	if model != "" && !h.Repo.IsProviderAvailable(provider, model) {
		log.Warn("health", "unhealthy provider", "provider", provider, "model", model)
	}

	var conn *models.ProviderConnection
	var err error

	if connectionID != "" {
		conn, err = h.Repo.GetProviderConnectionByID(connectionID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch connection %s: %w", connectionID, err)
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("connection %s not found", connectionID)
		}
	} else {
		connections, queryErr := h.Repo.GetProviderConnections(provider, true)
		if queryErr != nil {
			return nil, nil, fmt.Errorf("failed to query connections for %s: %w", provider, queryErr)
		}
		if len(connections) == 0 {
			if fallbackProvider, ok := CredentialFallbacks[provider]; ok {
				fallbackConns, fallbackErr := h.Repo.GetProviderConnections(fallbackProvider, true)
				if fallbackErr == nil && len(fallbackConns) > 0 {
					connections = fallbackConns
				}
			}
		}
		if len(connections) == 0 {
			if cfg, ok := providers.KnownProviders[provider]; ok && cfg.NoAuth {
				// Inject a virtual connection for a no-auth provider, carrying only
				// proxy configuration — there is no credential to store. Mirrors
				// VansRouter src/sse/services/auth.js, which returns
				// { id: "noauth", accessToken: "public", providerSpecificData: {...} }
				// for FREE_PROVIDERS[provider].noAuth.
				connData := &ConnectionData{
					AccessToken: "public",
				}
				if settings, err := h.Repo.GetSettings(); err == nil && settings != nil {
					if strat, ok := settings.ProviderStrategies[provider]; ok {
						connData.ProxyPoolID = h.resolveNoAuthProxyPoolID(provider, strat)
					}
				}
				publicName := "Public"
				conn := &models.ProviderConnection{
					ID:       "noauth",
					Provider: provider,
					Name:     &publicName,
					IsActive: 1,
				}
				return conn, connData, nil
			}
			return nil, nil, fmt.Errorf("no active connections for provider: %s", provider)
		}

		// Apply the provider's account routing strategy. Two settings feed this:
		// fallbackStrategy spreads requests across the provider's own ACCOUNTS
		// (the Round Robin toggle on the provider page), while rotateStrategy
		// spreads them across PROXY POOLS. They are independent, and either one
		// being on is a reason to reorder.
		if len(connections) > 1 && h.Repo != nil {
			if settings, sErr := h.Repo.GetSettings(); sErr == nil && settings != nil && settings.ProviderStrategies != nil {
				if strat, ok := settings.ProviderStrategies[provider]; ok {
					poolRotation := strat.RotateStrategy != "" && strat.RotateStrategy != "none"
					if poolRotation || strat.WantsAccountRoundRobin() {
						connections = h.applyConnectionStrategy(provider, connections, strat)
					}
				}
			}
		}

		excludeSet := make(map[string]bool, len(excludeIDs))
		for _, id := range excludeIDs {
			excludeSet[id] = true
		}

		conn = nil
		for _, c := range connections {
			if excludeSet[c.ID] {
				continue
			}
			// Skip connections that have an active per-connection model lock
			if model != "" {
				lockKey := canonicalLockModel(provider, model)
				if locked, _ := h.Repo.IsConnectionModelLocked(c.ID, lockKey); locked {
					continue
				}
				if lockKey != model {
					if locked, _ := h.Repo.IsConnectionModelLocked(c.ID, model); locked {
						continue
					}
				}
				if provider == "antigravity" && IsAntigravityModelBlocked(c.ID, model) {
					continue
				}
			}
			conn = c
			break
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("no available connections for provider: %s (all excluded)", provider)
		}
	}

	var connData ConnectionData
	if conn.Data != "" {
		if err := json.Unmarshal([]byte(conn.Data), &connData); err != nil {
			return nil, nil, fmt.Errorf("failed to parse connection data: %w", err)
		}
	}

	return conn, &connData, nil
}

// resolveNoAuthProxyPoolID decides which proxy pool a no-auth provider should
// use for this request. With rotateStrategy "none" (or unset) the statically
// configured pool wins. With any other strategy the pool is chosen from the
// active pools carrying a URL, narrowed by targetProxyPoolIds — mirroring
// VansRouter's pickProxyPoolId(poolIds, strategy, providerId, targets).
//
// A missing or unusable pool resolves to "", which callers treat as a direct
// connection rather than an error.
func (h *ChatHandler) resolveNoAuthProxyPoolID(provider string, strat db.ProviderStrategy) string {
	if h.Repo == nil {
		return ""
	}
	strategy := strings.TrimSpace(strat.RotateStrategy)
	if strategy == "" || strategy == "none" {
		if strat.ProxyPoolID != "" && strat.ProxyPoolID != "__none__" {
			return strat.ProxyPoolID
		}
		return ""
	}

	eligible, err := h.Repo.EligibleProxyPoolIDs()
	if err != nil {
		log.Warn("proxy", "list proxy pools failed", "provider", provider, "error", err)
		return ""
	}
	return db.PickProxyPoolID(eligible, strat.TargetProxyPoolIds, strategy, provider)
}

// GetProviderConfig returns the upstream configuration for a provider.
func (h *ChatHandler) GetProviderConfig(provider string, connData *ConnectionData) (*providers.ProviderConfig, error) {
	return h.getProviderConfig(provider, connData)
}

func (h *ChatHandler) getProviderConfig(provider string, connData *ConnectionData) (*providers.ProviderConfig, error) {
	var baseCfg *providers.ProviderConfig

	if connData != nil && connData.BaseURL != "" {
		if cfg, ok := providers.KnownProviders[provider]; ok {
			cloned := cfg
			cloned.BaseURL = connData.BaseURL
			baseCfg = &cloned
		} else {
			baseCfg = &providers.ProviderConfig{
				BaseURL:    connData.BaseURL,
				AuthHeader: constants.HeaderAuthorization,
				AuthScheme: constants.AuthSchemeBearer,
			}
		}
	} else if baseURL := providers.AccountScopedBaseURL(provider, connData.ProviderSpecificData); baseURL != "" {
		// Cloudflare Workers AI puts the account id in the URL path, so its base
		// URL is per-connection and cannot live in the static registry. Without
		// this the request went to /accounts//ai/v1/chat/completions and 404ed.
		cloned := providers.KnownProviders[provider]
		cloned.BaseURL = baseURL
		baseCfg = &cloned
	} else if cfg, ok := providers.KnownProviders[provider]; ok {
		// Clone config so per-request headers don't mutate global registry
		cloned := cfg
		baseCfg = &cloned
	} else {
		node, nodeData, err := h.Repo.GetProviderNodeByID(provider)
		if err != nil {
			return nil, fmt.Errorf("failed to look up provider node %s: %w", provider, err)
		}
		if node != nil && nodeData != nil && nodeData.BaseURL != "" {
			baseURL := nodeData.BaseURL
			if !strings.HasSuffix(baseURL, "/chat/completions") {
				if strings.HasSuffix(baseURL, "/v1") || strings.HasSuffix(baseURL, "/v1/") {
					baseURL = strings.TrimRight(baseURL, "/") + "/chat/completions"
				} else {
					baseURL = strings.TrimRight(baseURL, "/") + "/v1/chat/completions"
				}
			}
			baseCfg = &providers.ProviderConfig{
				BaseURL:    baseURL,
				AuthHeader: constants.HeaderAuthorization,
				AuthScheme: constants.AuthSchemeBearer,
			}
		}
	}

	if baseCfg == nil {
		return nil, fmt.Errorf("provider %q has no baseUrl in connection data and is not in KnownProviders", provider)
	}

	// Antigravity engines share one backend but two official client identities
	// (IDE vs CLI). The profile is a provider-wide setting: it says which official
	// client we imitate, which has nothing to do with the individual account, so
	// it is read once from settings rather than per connection. Resolving it into
	// a User-Agent here keeps every downstream caller (chat, quota, search)
	// presenting the same identity.
	if provider == "antigravity" {
		profile := providers.AntigravityProfileIDE
		if settings, err := h.Repo.GetSettings(); err == nil && settings != nil {
			profile = providers.NormalizeAntigravityClientProfile(settings.AntigravityClientProfile)
		}
		cloned := *baseCfg
		if cloned.StaticHeaders == nil {
			cloned.StaticHeaders = map[string]string{}
		} else {
			hdrs := make(map[string]string, len(cloned.StaticHeaders)+1)
			for k, v := range cloned.StaticHeaders {
				hdrs[k] = v
			}
			cloned.StaticHeaders = hdrs
		}
		cloned.StaticHeaders["User-Agent"] = providers.AntigravityUserAgent(profile)
		baseCfg = &cloned
	}

	// Check if this connection uses an Edge Relay Proxy Pool (Vercel, Cloudflare, Deno)
	if connData != nil {
		var relayURL string
		var noProxy string

		if connData.ProxyPoolID != "" {
			if pool, err := h.Repo.GetProxyPool(connData.ProxyPoolID); err == nil && pool != nil && pool.IsActive {
				if pool.Type == "vercel" || pool.Type == "cloudflare" || pool.Type == "deno" {
					relayURL = pool.NextURL()
					noProxy = pool.NoProxy
				}
			}
		}
		if relayURL == "" && connData.ProviderSpecificData != nil {
			if u, ok := connData.ProviderSpecificData["vercelRelayUrl"].(string); ok && u != "" {
				relayURL = u
				if np, ok := connData.ProviderSpecificData["connectionNoProxy"].(string); ok {
					noProxy = np
				}
			}
		}

		if relayURL != "" && !internalproxy.ShouldBypassNoProxy(baseCfg.BaseURL, noProxy) {
			cloned := *baseCfg
			cloned.StaticHeaders = internalproxy.BuildEdgeRelayHeaders(baseCfg.BaseURL, cloned.StaticHeaders)
			cloned.BaseURL = relayURL
			return &cloned, nil
		}
	}

	return baseCfg, nil
}

// ExtractAPIKey gets the API key from a connection's data.
func ExtractAPIKey(connData *ConnectionData) string {
	return extractAPIKey(connData)
}

func extractAPIKey(connData *ConnectionData) string {
	if connData.APIKey != "" {
		return connData.APIKey
	}
	return connData.AccessToken
}

// NormalizeProviderToken normalizes credentials for providers with specific token requirements
// (e.g. Cline OAuth tokens require workos: prefix, whereas API keys ride plain Bearer).
func NormalizeProviderToken(provider, token string) string {
	if (provider == "cline" || provider == "clinepass") && token != "" {
		t := strings.TrimSpace(token)
		if !strings.HasPrefix(t, "workos:") && !strings.HasPrefix(t, "sk_") {
			return "workos:" + t
		}
		return t
	}
	return token
}

// GetClientForConnection returns an http.Client configured with ProxyPool transport if set.
func (h *ChatHandler) GetClientForConnection(connData *ConnectionData) *http.Client {
	return h.getClientForConnection(connData)
}

func (h *ChatHandler) getClientForConnection(connData *ConnectionData) *http.Client {
	if connData == nil {
		return h.Client
	}

	var proxyURLStr string
	var proxyType string
	var strictProxy bool

	// 1. Resolve from ProxyPool
	if connData.ProxyPoolID != "" {
		pool, err := h.Repo.GetProxyPool(connData.ProxyPoolID)
		if err == nil && pool != nil && pool.IsActive {
			proxyURLStr = pool.NextURL()
			proxyType = pool.Type
			strictProxy = pool.StrictProxy
		}
	}

	// 2. Fallback to legacy connection proxy
	if proxyURLStr == "" {
		proxyEnabled := connData.ConnectionProxyEnabled
		proxyURL := connData.ConnectionProxyURL
		if !proxyEnabled && connData.ProviderSpecificData != nil {
			if en, ok := connData.ProviderSpecificData["connectionProxyEnabled"].(bool); ok {
				proxyEnabled = en
			}
			if u, ok := connData.ProviderSpecificData["connectionProxyUrl"].(string); ok {
				proxyURL = u
			}
			if sp, ok := connData.ProviderSpecificData["strictProxy"].(bool); ok {
				strictProxy = sp
			}
		}
		if proxyEnabled && proxyURL != "" {
			proxyURLStr = proxyURL
			proxyType = "http"
		}
	}

	if proxyURLStr == "" {
		return h.Client
	}

	parsedURL, err := url.Parse(proxyURLStr)
	if err != nil {
		log.Warn("proxy", "invalid proxy pool url", "pool", connData.ProxyPoolID, "url", proxyURLStr, "error", err)
		if strictProxy {
			log.Error("proxy", "strict proxy enabled but proxy url invalid", "url", proxyURLStr)
		}
		return h.Client
	}

	if proxyType == "http" || proxyType == "" {
		proxyClientsMu.RLock()
		client, ok := proxyClients[proxyURLStr]
		proxyClientsMu.RUnlock()
		if ok {
			return client
		}

		proxyClientsMu.Lock()
		defer proxyClientsMu.Unlock()
		if client, ok = proxyClients[proxyURLStr]; ok {
			return client
		}

		baseTransport := http.DefaultTransport.(*http.Transport).Clone()
		baseTransport.Proxy = http.ProxyURL(parsedURL)
		client = &http.Client{
			Transport: baseTransport,
			Timeout:   h.Client.Timeout,
		}
		proxyClients[proxyURLStr] = client
		return client
	}

	// For Edge Relays (vercel, cloudflare, deno), standard client is used because
	// URL rewriting and x-relay headers are handled at request time.
	return h.Client
}

// canonicalLockModel normalizes model names for providers sharing a backend
// quota/capacity pool (such as Antigravity gemini-3.8-flash-low/high -> gemini-3.8-flash-tiered).
func canonicalLockModel(provider, model string) string {
	if model == "" {
		return ""
	}
	if provider == "antigravity" {
		return translator.NormalizeAntigravityModel(model)
	}
	return model
}

// ApplyConnectionStrategy rotates candidate connections according to the provider's configured strategy.
func (h *ChatHandler) ApplyConnectionStrategy(provider string, conns []*models.ProviderConnection, strat db.ProviderStrategy) []*models.ProviderConnection {
	return h.applyConnectionStrategy(provider, conns, strat)
}

func (h *ChatHandler) applyConnectionStrategy(provider string, conns []*models.ProviderConnection, strat db.ProviderStrategy) []*models.ProviderConnection {
	if len(conns) <= 1 {
		return conns
	}

	// Account rotation is a separate switch from proxy-pool rotation. When the
	// operator turns on Round Robin for a provider but leaves rotateStrategy at
	// its default, the pool setting must not decide how accounts rotate —
	// "sticky" for pools would otherwise pin every request to the same account.
	strategy := strings.ToLower(strings.TrimSpace(strat.RotateStrategy))
	stickyLimit := strat.StickyLimit
	if strat.WantsAccountRoundRobin() {
		strategy = "round-robin"
		stickyLimit = strat.AccountStickyLimit()
	}

	switch strategy {
	case "round-robin", "roundrobin":
		if stickyLimit <= 0 {
			stickyLimit = 1
		}
		return h.rotateConnectionsSticky(provider, conns, stickyLimit)

	case "sticky":
		if stickyLimit <= 0 {
			stickyLimit = 1
		}
		return h.rotateConnectionsSticky(provider, conns, stickyLimit)

	case "random":
		offset := rand.IntN(len(conns))
		rotated := make([]*models.ProviderConnection, len(conns))
		for i := range conns {
			rotated[i] = conns[(offset+i)%len(conns)]
		}
		return rotated

	default:
		// "none", "fallback", or empty: keep DB priority order
		return conns
	}
}

// rotateConnectionsSticky orders conns so the account that should serve next is
// first. The choice follows VansRouter's auth.js: sort by lastUsedAt, stay on
// the most recent account while it has served fewer than stickyLimit requests,
// otherwise hand over to the least recently used one.
//
// The state lives in the providerConnections row, not in memory, so a restart
// does not reset the rotation to the highest-priority account and starve the
// rest. lastUsedAt is written here rather than by the caller so the ordering and
// the bookkeeping cannot disagree.
func (h *ChatHandler) rotateConnectionsSticky(provider string, conns []*models.ProviderConnection, stickyLimit int) []*models.ProviderConnection {
	if len(conns) <= 1 {
		return conns
	}
	if stickyLimit <= 0 {
		stickyLimit = 1
	}

	// Without a Repo there is nowhere to persist lastUsedAt — the nop handler and
	// some tests run this way — so fall back to rotating by index in memory.
	// That order resets on restart, which is why it is not the default path.
	if h.Repo == nil {
		return h.rotateConnectionsInMemory(provider, conns, stickyLimit)
	}

	// Sort by recency. Never-used connections sort last so they claim a turn
	// before anyone who has already served.
	byOldest := make([]*models.ProviderConnection, len(conns))
	copy(byOldest, conns)
	sort.SliceStable(byOldest, func(i, j int) bool {
		a, b := byOldest[i], byOldest[j]
		if a.LastUsedAt == "" && b.LastUsedAt == "" {
			return priorityOf(a) < priorityOf(b)
		}
		if a.LastUsedAt == "" {
			return true
		}
		if b.LastUsedAt == "" {
			return false
		}
		if a.LastUsedAt != b.LastUsedAt {
			return a.LastUsedAt < b.LastUsedAt
		}
		return priorityOf(a) < priorityOf(b)
	})

	// Stay put while the current account still has budget in its run. "Current"
	// is the most recently used; if that one just handed over, it is the oldest
	// that goes next.
	var chosen *models.ProviderConnection
	mostRecent := byOldest[len(byOldest)-1]
	if mostRecent != nil && mostRecent.LastUsedAt != "" && mostRecent.ConsecutiveUseCount < stickyLimit {
		chosen = mostRecent
	} else {
		chosen = byOldest[0]
	}

	if chosen != nil && h.Repo != nil {
		// One write records both the timestamp and the run length: SQLite bumps
		// consecutiveUseCount, and a handover resets it so the new account starts
		// its own run. Doing the arithmetic here as well would double-count, and
		// the in-memory copy is a stale snapshot by the next request anyway.
		handover := chosen != mostRecent || chosen.LastUsedAt == ""
		if err := h.Repo.UpdateConnectionLastUsed(chosen.ID, handover); err != nil {
			log.Warn("round-robin", "not recorded; rotation will repeat this account",
				"provider", provider, "connection", chosen.ID, "error", err)
		}
		// Keep the caller's copy consistent with what was just written, so a
		// second call in the same request does not repeat the same account.
		chosen.LastUsedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if handover {
			chosen.ConsecutiveUseCount = 1
		} else {
			chosen.ConsecutiveUseCount++
		}
	}

	// Put the chosen connection first and keep the rest in their existing order,
	// so a caller that walks the slice falls back predictably.
	rotated := make([]*models.ProviderConnection, 0, len(conns))
	if chosen != nil {
		rotated = append(rotated, chosen)
	}
	for _, c := range conns {
		if chosen == nil || c.ID != chosen.ID {
			rotated = append(rotated, c)
		}
	}
	return rotated
}

// rotateConnectionsInMemory advances a per-provider index, used only when there
// is no Repo to persist to. The state is lost on restart.
func (h *ChatHandler) rotateConnectionsInMemory(provider string, conns []*models.ProviderConnection, stickyLimit int) []*models.ProviderConnection {
	h.stickyMu.Lock()
	defer h.stickyMu.Unlock()
	if h.stickyState == nil {
		h.stickyState = make(map[string]*comboStickyState)
	}

	key := "conn:" + provider
	state, exists := h.stickyState[key]
	if !exists {
		state = &comboStickyState{Index: 0, ConsecutiveUseCount: 0}
		h.stickyState[key] = state
	}

	servingIndex := state.Index % len(conns)
	state.ConsecutiveUseCount++
	if state.ConsecutiveUseCount >= stickyLimit {
		state.Index = (servingIndex + 1) % len(conns)
		state.ConsecutiveUseCount = 0
	}
	state.ServingIndex = servingIndex

	rotated := make([]*models.ProviderConnection, len(conns))
	for i := range conns {
		rotated[i] = conns[(servingIndex+i)%len(conns)]
	}
	return rotated
}

// priorityOf reads a connection's priority, treating an unset value as lowest.
func priorityOf(c *models.ProviderConnection) int {
	if c == nil || c.Priority == nil {
		return 999999
	}
	return *c.Priority
}

// ResetConnectionState clears rotation state for a provider (or all providers if provider="").
func (h *ChatHandler) ResetConnectionState(provider string) {
	h.stickyMu.Lock()
	defer h.stickyMu.Unlock()
	if h.stickyState == nil {
		return
	}
	if provider == "" {
		for k := range h.stickyState {
			if strings.HasPrefix(k, "conn:") {
				delete(h.stickyState, k)
			}
		}
		return
	}
	delete(h.stickyState, "conn:"+provider)
}
