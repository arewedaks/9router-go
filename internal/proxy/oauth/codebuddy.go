package oauth

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

func init() {
	Register("codebuddy-cn", RefreshCodebuddy)
	Register("codebuddy-intl", RefreshCodebuddy)
}

// codebuddyRegion captures the endpoint + identity for one CodeBuddy region.
// Mirrors `oauth: {...}` in VansRouter's registry/codebuddy-{cn,intl}.js, which
// is the authoritative description of both deployments.
type codebuddyRegion struct {
	RefreshURL string
	UserAgent  string
	Domain     string
}

// codebuddyRegions maps a canonical provider id to its region endpoints.
//
// The two regions are separate hostnames (copilot.tencent.com vs
// www.codebuddy.ai) with different client identities, so a refresh must target
// the host that issued the token; sending a .ai refresh token to the Tencent
// endpoint (or vice versa) is rejected.
var codebuddyRegions = map[string]codebuddyRegion{
	"codebuddy-cn": {
		RefreshURL: "https://copilot.tencent.com/v2/plugin/auth/token/refresh",
		UserAgent:  "CLI/2.63.2 CodeBuddy/2.63.2",
		Domain:     "copilot.tencent.com",
	},
	"codebuddy-intl": {
		RefreshURL: "https://www.codebuddy.ai/v2/plugin/auth/token/refresh",
		UserAgent:  "IDE/2.63.2 CodeBuddy/2.63.2",
		Domain:     "www.codebuddy.ai",
	},
}

// codebuddyRefreshResponse is the envelope returned by the refresh endpoint. It
// shares the { code, msg, data } shape of the device-auth poll.
type codebuddyRefreshResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		TokenType    string `json:"tokenType"`
		ExpiresIn    int    `json:"expiresIn"`
	} `json:"data"`
}

// RefreshCodebuddy renews a CodeBuddy access token.
//
// CodeBuddy is not a standards-compliant OAuth2 refresh: it POSTs an empty `{}`
// body and carries the refresh token in the **X-Refresh-Token header** (plus
// X-Auth-Refresh-Source: plugin), mirroring the official client — see
// VansRouter's open-sse/services/tokenRefresh/providers.js
// (refreshCodebuddyToken). It ships no client_id/secret, so it needs this
// custom refresher rather than the shared StandardRefresher (which would post a
// form body with no header and fail).
func RefreshCodebuddy(ctx context.Context, p *Params) (*TokenResult, error) {
	prov := p.Provider
	if prov == "" {
		prov = "codebuddy-cn"
	}
	if p.RefreshToken == "" {
		return nil, fmt.Errorf("%s: refresh_token is required", prov)
	}

	region, ok := codebuddyRegions[prov]
	if !ok {
		// Alias or unknown id: fall back to CN and let the host config win.
		region = codebuddyRegions["codebuddy-cn"]
	}
	refreshURL := region.RefreshURL
	if cfg, ok := providers.KnownOAuthConfigs[prov]; ok && cfg.TokenURL != "" {
		refreshURL = cfg.TokenURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("%s: create refresh request: %w", prov, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", region.UserAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Domain", region.Domain)
	req.Header.Set("X-Refresh-Token", p.RefreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
	req.Header.Set("X-Product", "SaaS")

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: refresh request: %w", prov, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: read refresh response: %w", prov, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: refresh HTTP %d: %.200s", prov, resp.StatusCode, string(raw))
	}

	var parsed codebuddyRefreshResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%s: refresh response not JSON: %w", prov, err)
	}
	if parsed.Data.AccessToken == "" {
		return nil, fmt.Errorf("%s: refresh returned no access token (code %d %s)", prov, parsed.Code, parsed.Msg)
	}

	// The gateway may omit expiresIn; the client treats the token as
	// long-lived, so default to 24h rather than 0 (which the store would read as
	// "already expired" and re-refresh on every request).
	expiresIn := parsed.Data.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 86400
	}
	newRefresh := parsed.Data.RefreshToken
	if newRefresh == "" {
		newRefresh = p.RefreshToken // keep the old one when upstream omits it
	}
	return &TokenResult{
		AccessToken:  parsed.Data.AccessToken,
		RefreshToken: newRefresh,
		ExpiresIn:    expiresIn,
	}, nil
}

// codebuddyHostOf is a tiny helper used by tests to assert the region host.
func codebuddyHostOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return u.Host
}
