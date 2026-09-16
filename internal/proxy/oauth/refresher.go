// Package oauth provides per-provider OAuth token refresh.
// Register custom refresh functions for providers that need non-standard OAuth flows.
package oauth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

// TokenResult holds the result of a token refresh.
type TokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // seconds
	Scope        string
	ProjectID    string // provider-specific extra field

	// ExtraFields are provider-specific keys merged verbatim into the stored
	// connection blob. A provider that caches a derived credential alongside the
	// refreshed token — GitHub Copilot stores the Copilot bearer under
	// providerSpecificData.copilotToken — sets it here so callers of the derived
	// token see the new value too. Without this the stored accessToken would be
	// fresh while the cached derived token stayed stale, and any reader of the
	// derived field would keep seeing the old value.
	ExtraFields map[string]interface{}

	// ProviderSpecificData is deep-merged into the connection's
	// providerSpecificData object (siblings are preserved), so a provider can
	// refresh a nested cached credential without clobbering unrelated keys like
	// the GitHub identity fields.
	ProviderSpecificData map[string]interface{}
}

// Params holds all inputs for a refresh call.
type Params struct {
	Client       *http.Client
	Provider     string
	RefreshToken string
	AccessToken  string // current (possibly expired) token
}

// Refresher refreshes an OAuth token for a specific provider.
type Refresher func(ctx context.Context, p *Params) (*TokenResult, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Refresher{}
)

// Register adds a refresher for the given provider.
func Register(provider string, fn Refresher) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[provider] = fn
}

// Get returns the refresher for the given provider, or nil.
func Get(provider string) Refresher {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[provider]
}

// Refresh calls the provider's refresher, or falls back to standard OAuth2.
func Refresh(ctx context.Context, p *Params) (*TokenResult, error) {
	if fn := Get(p.Provider); fn != nil {
		return fn(ctx, p)
	}
	return nil, fmt.Errorf("no OAuth refresher for: %s", p.Provider)
}
