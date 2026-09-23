package dashboard

import (
	"context"
	"net/http"
	"strings"
	"time"

	json "encoding/json/v2"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/quotatracker"
)

// quotaFetchTimeout bounds a quota lookup. The billing endpoints answer in well
// under a second; a hung one must not hold a dashboard request open.
const quotaFetchTimeout = 20 * time.Second

// quotaConnectionResult is one connection's quota, bound by id so the UI
// attaches it to the exact row rather than a list position.
type quotaConnectionResult struct {
	ConnectionID string                      `json:"connectionId"`
	Provider     string                      `json:"provider"`
	Name         string                      `json:"name,omitempty"`
	Plan         string                      `json:"plan,omitempty"`
	Quotas       map[string]quotatracker.Quota `json:"quotas,omitempty"`
	Message      string                      `json:"message,omitempty"`
}

// HandleProviderQuota returns the live credit/quota balances for every
// connection of a provider.
//
// GET /api/dashboard/providers/{id}/quota
//
// A provider with no quota handler reports one message per connection rather
// than an HTTP error: "not implemented" is information the UI renders inline,
// not a failed request. The response is 200 for a known provider so the
// dashboard can show the reason where the balances would go.
func (h *Handler) HandleProviderQuota(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(chi.URLParam(r, "id"))
	if raw == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "provider id is required")
		return
	}
	canonical := providers.ResolveAlias(raw)

	// activeOnly=false: an exhausted account is exactly the one an operator
	// opens this panel to inspect, so it must not be filtered out.
	conns, err := h.repo.GetProviderConnections(canonical, false)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to load connections: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), quotaFetchTimeout)
	defer cancel()

	// Sequential on purpose: quota endpoints are per-account and rate-limited,
	// and this is an operator-initiated read of a handful of rows, not a hot
	// path. Probing them in parallel is how an account gets throttled.
	client := &http.Client{Timeout: quotaFetchTimeout}
	out := make([]quotaConnectionResult, 0, len(conns))
	for _, c := range conns {
		entry := quotaConnectionResult{
			ConnectionID: c.ID,
			Provider:     c.Provider,
			Name:         connectionDisplayName(c),
		}

		creds, ok := quotaCredentialsFor(c)
		if !ok {
			entry.Message = "Credential not available for quota lookup."
			out = append(out, entry)
			continue
		}

		res, err := quotatracker.Fetch(ctx, client, creds)
		if err != nil {
			entry.Message = err.Error()
			out = append(out, entry)
			continue
		}
		entry.Plan = res.Plan
		entry.Quotas = res.Quotas
		entry.Message = res.Message
		out = append(out, entry)
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":    canonical,
		"connections": out,
	})
}

// quotaCredentialsFor assembles what a quota handler needs from a stored
// connection: the bearer token, the provider's static headers (Tencent rejects
// the billing call without the client fingerprint) and the quota endpoint.
func quotaCredentialsFor(c *models.ProviderConnection) (quotatracker.Credentials, bool) {
	var data struct {
		APIKey      string `json:"apiKey"`
		AccessToken string `json:"accessToken"`
	}
	if c.Data != "" {
		_ = json.Unmarshal([]byte(c.Data), &data)
	}
	token := data.AccessToken
	if token == "" {
		token = data.APIKey
	}
	if token == "" {
		return quotatracker.Credentials{}, false
	}

	creds := quotatracker.Credentials{
		Provider:    c.Provider,
		AccessToken: token,
	}
	if cfg, ok := providers.KnownProviders[c.Provider]; ok {
		creds.StaticHeader = cfg.StaticHeaders
		creds.UsageURL = cfg.UsageURL
	}
	return creds, true
}

// connectionDisplayName picks the most identifying label available for a
// connection, matching how the provider page labels its rows.
func connectionDisplayName(c *models.ProviderConnection) string {
	if c.Email != nil && *c.Email != "" {
		return *c.Email
	}
	if c.Name != nil && *c.Name != "" {
		return *c.Name
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}
