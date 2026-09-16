package oauth

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

func init() {
	Register("github", RefreshGitHub)
}

// githubCopilotTokenURL is GitHub's internal endpoint that exchanges a GitHub
// access token (PAT / OAuth token from the device flow) for a short-lived
// Copilot bearer token, which is what api.githubcopilot.com actually accepts.
const githubCopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// githubCopilotTokenResponse is the small envelope returned by the endpoint.
type githubCopilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

// RefreshGitHub renews the Copilot bearer token.
//
// This is NOT an OAuth2 refresh: GitHub Copilot is a public device-flow client
// with no client_secret, and the chat credential is a Copilot token derived from
// the long-lived GitHub access token. So a "refresh" here re-derives the Copilot
// token from the stored GitHub access token, mirroring OmniRoute's
// refreshCopilotToken (open-sse/services/tokenRefresh/providers/copilot.ts).
//
// Two details matter and are both easy to get wrong:
//
//  1. The Authorization scheme is `token <access_token>`, NOT `Bearer`.
//  2. The User-Agent is the legacy "GithubCopilot/1.0"; the refresh host rejects
//     the newer CLI UA used on the inference path.
//
// The response carries `expires_at` (unix seconds) instead of `expires_in`, so
// the remaining lifetime is computed against the current clock. Because
// GitHubCopilotMachineID is computed per request header block elsewhere, nothing
// here depends on process state.
func RefreshGitHub(ctx context.Context, p *Params) (*TokenResult, error) {
	// The long-lived credential is the *GitHub* access token. Callers may pass it
	// as either AccessToken or RefreshToken depending on how the connection was
	// stored, so accept both.
	githubToken := strings.TrimSpace(p.AccessToken)
	if githubToken == "" {
		githubToken = strings.TrimSpace(p.RefreshToken)
	}
	if githubToken == "" {
		return nil, fmt.Errorf("github: no GitHub access token available to derive a Copilot token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubCopilotTokenURL, nil)
	if err != nil {
		return nil, fmt.Errorf("github: create refresh request: %w", err)
	}
	// Scheme is deliberately `token`, not `Bearer` — see the doc comment.
	req.Header.Set("Authorization", "token "+githubToken)
	for k, v := range providers.GitHubCopilotRefreshHeaders("token " + githubToken) {
		req.Header.Set(k, v)
	}
	// The helper above already set Authorization; keep it authoritative.
	req.Header.Set("Authorization", "token "+githubToken)

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: refresh request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("github: read refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: refresh HTTP %d: %.200s", resp.StatusCode, string(raw))
	}

	var parsed githubCopilotTokenResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("github: refresh response not JSON: %w", err)
	}
	if strings.TrimSpace(parsed.Token) == "" {
		return nil, fmt.Errorf("github: refresh returned no Copilot token")
	}

	// expires_at is an absolute unix timestamp. Convert it to a relative
	// expires_in the store can persist. Treat the token as long-lived if the
	// field is missing (0), so the store does not immediately re-refresh.
	expiresIn := 0
	if parsed.ExpiresAt > 0 {
		if remaining := time.Until(time.Unix(parsed.ExpiresAt, 0)); remaining > 0 {
			expiresIn = int(remaining.Seconds())
		} else {
			// Upstream clock skew can put expires_at slightly in the past; floor
			// to a short positive value rather than 0.
			expiresIn = 60
		}
	}

	return &TokenResult{
		// The Copilot token is the credential callers use for chat.
		AccessToken: parsed.Token,
		// The GitHub token remains the long-lived refresh input.
		RefreshToken: githubToken,
		ExpiresIn:    expiresIn,
	}, nil
}
