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
	"strings"
	"sync"
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
				// Inject virtual connection for no-auth provider with optional proxy pool strategy from settings
				connData := &ConnectionData{
					AccessToken: "public",
				}
				settings, err := h.Repo.GetSettings()
				if err == nil && settings != nil && settings.ProviderStrategies != nil {
					if strat, ok := settings.ProviderStrategies[provider]; ok {
						if strat.ProxyPoolID != "" && strat.ProxyPoolID != "__none__" {
							connData.ProxyPoolID = strat.ProxyPoolID
						}
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

		// Apply provider connection routing strategy (round-robin, sticky, random) if configured
		if len(connections) > 1 && h.Repo != nil {
			if settings, sErr := h.Repo.GetSettings(); sErr == nil && settings != nil && settings.ProviderStrategies != nil {
				if strat, ok := settings.ProviderStrategies[provider]; ok && strat.RotateStrategy != "" && strat.RotateStrategy != "none" {
					connections = h.applyConnectionStrategy(provider, connections, strat)
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
	// (IDE vs CLI). Resolve the connection's selected profile into a User-Agent
	// header so every downstream caller (chat, quota, search) presents the same
	// identity without each one re-reading connection data.
	if provider == "antigravity" && connData != nil {
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
		cloned.StaticHeaders["User-Agent"] = providers.AntigravityUserAgentForData(connData.ProviderSpecificData)
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

	strategy := strings.ToLower(strings.TrimSpace(strat.RotateStrategy))
	switch strategy {
	case "round-robin", "roundrobin":
		stickyLimit := 1
		if strat.StickyLimit > 0 {
			stickyLimit = strat.StickyLimit
		}
		return h.rotateConnectionsSticky(provider, conns, stickyLimit)

	case "sticky":
		stickyLimit := strat.StickyLimit
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

func (h *ChatHandler) rotateConnectionsSticky(provider string, conns []*models.ProviderConnection, stickyLimit int) []*models.ProviderConnection {
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
