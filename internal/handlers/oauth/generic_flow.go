package oauth

import (
	"sync"
	"time"

	"9router/proxy/internal/providers"
)

// providersOAuthConfigs exposes the provider package's OAuth client configs.
//
// Kept as a function rather than a direct reference so the spec table can be
// read without threading an import through every entry, and so tests can swap
// it if needed.
func providersOAuthConfigs() map[string]providers.OAuthClientConfig {
	return providers.KnownOAuthConfigs
}

// genericFlow holds the per-state PKCE verifier and redirect URI between the
// authorize and exchange calls of a generic authorization-code flow.
//
// The dashboard is a single-operator surface, so a small in-memory map with a
// short TTL is enough; entries are consumed on exchange.
type genericFlow struct {
	provider    string
	verifier    string
	redirectURI string
	createdAt   time.Time
}

const genericFlowTTL = 10 * time.Minute

var (
	genericFlowsMu sync.Mutex
	genericFlows   = map[string]genericFlow{}
)

func storeGenericFlow(state string, f genericFlow) {
	genericFlowsMu.Lock()
	defer genericFlowsMu.Unlock()
	for k, v := range genericFlows {
		if time.Since(v.createdAt) > genericFlowTTL {
			delete(genericFlows, k)
		}
	}
	genericFlows[state] = f
}

func takeGenericFlow(state string) (genericFlow, bool) {
	genericFlowsMu.Lock()
	defer genericFlowsMu.Unlock()
	f, ok := genericFlows[state]
	if ok {
		delete(genericFlows, state)
	}
	return f, ok
}
