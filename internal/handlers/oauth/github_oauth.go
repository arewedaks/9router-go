package oauth

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// GitHub Copilot OAuth (device-code flow).
//
// Copilot does not use a redirect/callback OAuth flow in its CLI. The official
// client runs GitHub's OAuth **device flow** (RFC 8628):
//
//  1. POST github.com/login/device/code  ->  {device_code, user_code,
//     verification_uri, expires_in, interval}. The operator is shown the
//     user_code and opens verification_uri to enter it.
//  2. POST github.com/login/oauth/access_token with
//     grant_type=urn:ietf:params:oauth:grant-type:device_code, polling until
//     GitHub returns an access_token (or an `authorization_pending` error).
//  3. GET api.github.com/copilot_internal/v2/token with `Authorization: token
//     <github_access_token>` -> the short-lived Copilot bearer token that
//     api.githubcopilot.com actually accepts.
//  4. GET api.github.com/user -> the account's login/name/email for the
//     connection label.
//
// The whole handshake is stateless on our side: the browser holds the
// device_code, so the dashboard passes it back to /exchange. Nothing needs a
// loopback callback, which keeps this usable on a headless server (same
// reasoning as the CodeBuddy device-auth handler).
//
// GitHub Copilot is a public device-flow client: it has a client_id but NO
// client_secret. There is nothing to register.

const (
	// githubDeviceCodeURL starts the device flow.
	githubDeviceCodeURL = "https://github.com/login/device/code"
	// githubAccessTokenURL polls for (and later refreshes) the GitHub token.
	githubAccessTokenURL = "https://github.com/login/oauth/access_token"
	// githubCopilotTokenURL derives the Copilot bearer token.
	githubCopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
	// githubUserURL returns the authenticated account.
	githubUserURL = "https://api.github.com/user"

	// githubDefaultClientID is Copilot's public client id. OmniRoute exposes it
	// as GITHUB_OAUTH_CLIENT_ID; we keep the same env override for parity.
	githubDefaultClientID = "Iv1.b507a08c87ecfe98"
	// githubDeviceScope is the minimal scope the reference requests.
	githubDeviceScope = "read:user"

	githubPollInterval = 5 * time.Second
	githubPollTimeout  = 10 * time.Minute
)

// githubClientID resolves Copilot's public device-flow client id. Onboarding is
// per-provider configuration, so the registry entry is the single source of
// truth (it already honours GITHUB_OAUTH_CLIENT_ID); the constant is only a
// fallback for a registry that somehow lacks the entry.
func githubClientID() string {
	if cfg, ok := providers.KnownOAuthConfigs["github"]; ok && strings.TrimSpace(cfg.ClientID) != "" {
		return cfg.ClientID
	}
	return githubDefaultClientID
}

// githubDeviceCodeResponse is the start-of-flow envelope.
type githubDeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Error           string `json:"error"`
	ErrorDesc       string `json:"error_description"`
}

// githubTokenResponse is the poll envelope. GitHub returns either an
// access_token or an `error` (notably "authorization_pending" while the operator
// has not yet approved).
type githubTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// githubCopilotToken is the Copilot bearer derived from the GitHub token.
type githubCopilotToken struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

// githubUser is the subset of /user we persist.
type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// HandleGitHubAuthorize starts the GitHub device flow.
//
// GET /api/oauth/github/authorize
//
// Returns {deviceCode, userCode, verificationUri, interval, expiresIn}: the
// dashboard shows userCode and opens verificationUri, then polls /exchange.
func (h *OAuthHandler) HandleGitHubAuthorize(w http.ResponseWriter, r *http.Request) {
	device, err := requestGitHubDeviceCode(githubClientID())
	if err != nil {
		log.Warn("oauth", "github device code request failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "could not start GitHub sign-in: "+err.Error())
		return
	}
	if device.DeviceCode == "" || device.UserCode == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "GitHub returned no device code")
		return
	}

	interval := device.Interval
	if interval <= 0 {
		interval = int(githubPollInterval.Seconds())
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":        "github",
		"deviceCode":      device.DeviceCode,
		"userCode":        device.UserCode,
		"verificationUri": device.VerificationURI,
		"intervalSec":     interval,
		"pollIntervalMs":  interval * 1000,
		"expiresIn":       device.ExpiresIn,
	})
}

// requestGitHubDeviceCode performs step 1.
func requestGitHubDeviceCode(clientID string) (githubDeviceCodeResponse, error) {
	var out githubDeviceCodeResponse
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", githubDeviceScope)

	req, err := http.NewRequest(http.MethodPost, githubDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, err
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("device code endpoint returned HTTP %d: %s", resp.StatusCode, truncateForOAuth(string(raw), 160))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("device code response is not valid JSON: %w", err)
	}
	if out.Error != "" {
		return out, fmt.Errorf("device code error: %s %s", out.Error, out.ErrorDesc)
	}
	return out, nil
}

// HandleGitHubExchange polls (once) for the token belonging to a device code,
// then derives and saves the Copilot credential.
//
// POST /api/oauth/github/exchange  {"deviceCode":"...","name":"..."}
//
// Returns 202 while the operator has not approved yet; 200 once saved.
func (h *OAuthHandler) HandleGitHubExchange(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		DeviceCode string `json:"deviceCode"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.DeviceCode) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing deviceCode (start a sign-in first)")
		return
	}

	tokens, pending, err := pollGitHubAccessToken(githubClientID(), req.DeviceCode)
	if err != nil {
		log.Warn("oauth", "github token poll failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "token poll failed: "+err.Error())
		return
	}
	if pending {
		handlerutil.WriteJSON(w, http.StatusAccepted, map[string]any{
			"pending": true,
			"message": "Waiting for you to approve the GitHub sign-in…",
		})
		return
	}
	if tokens.AccessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "GitHub returned no access token")
		return
	}

	name, err := h.saveGitHubConnection(tokens, req.Name)
	if err != nil {
		log.Error("oauth", "save github connection failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"provider": "github", "name": name})
}

// pollGitHubAccessToken performs one poll iteration. pending=true means GitHub
// reported authorization_pending (or slow_down), so the caller keeps polling.
func pollGitHubAccessToken(clientID, deviceCode string) (tokens githubTokenResponse, pending bool, err error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequest(http.MethodPost, githubAccessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokens, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return tokens, false, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokens, false, err
	}
	if resp.StatusCode != http.StatusOK {
		return tokens, false, fmt.Errorf("token endpoint returned HTTP %d: %s", resp.StatusCode, truncateForOAuth(string(raw), 160))
	}
	if err := json.Unmarshal(raw, &tokens); err != nil {
		return tokens, false, fmt.Errorf("token response is not valid JSON: %w", err)
	}

	if tokens.AccessToken != "" {
		return tokens, false, nil
	}
	return classifyGitHubPoll(tokens)
}

// classifyGitHubPoll turns a poll response into (tokens, pending, error).
//
// Split out so the state machine is unit-testable without a network hop:
// GitHub reports "authorization_pending" (and occasionally "slow_down") while
// the operator has not approved yet, which is a keep-waiting signal, not a
// failure. Everything else without a token is a real error.
func classifyGitHubPoll(tokens githubTokenResponse) (githubTokenResponse, bool, error) {
	if tokens.AccessToken != "" {
		return tokens, false, nil
	}
	switch tokens.Error {
	case "authorization_pending", "slow_down", "":
		return tokens, true, nil
	default:
		return tokens, false, fmt.Errorf("token error: %s %s", tokens.Error, tokens.ErrorDescription)
	}
}

// isGitHubAlias reports whether a provider id addresses GitHub Copilot. Kept
// here so the OAuth package can guard its own handler without importing the
// dashboard package (which would be a cycle).
func isGitHubAlias(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "github", "gh", "copilot":
		return true
	default:
		return false
	}
}

// saveGitHubConnection derives the Copilot token + user info and persists the
// account as a normal provider connection.
func (h *OAuthHandler) saveGitHubConnection(tokens githubTokenResponse, requestedName string) (string, error) {
	copilot, user := fetchGitHubIdentity(tokens.AccessToken)

	data := map[string]any{
		// The GitHub token is the long-lived credential used to re-derive the
		// Copilot token on refresh.
		"accessToken":  tokens.AccessToken,
		"refreshToken": tokens.AccessToken,
	}
	// expiresAt drives OAuthConnectionData.IsExpired(), which is what the chat
	// path uses to decide whether to refresh automatically. It describes the
	// *Copilot* token's lifetime (that is the credential requests actually carry);
	// leave it empty when upstream gave no expiry so the store refreshes lazily
	// instead of trusting a fabricated window.
	if copilot.ExpiresAt > 0 {
		data["expiresAt"] = time.Unix(copilot.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	psd := map[string]any{
		// Copilot-specific extras live under providerSpecificData, matching the
		// shape the reference persists (autoSync + copilotToken + identity).
		"autoSync": true,
	}
	if copilot.Token != "" {
		psd["copilotToken"] = copilot.Token
		if copilot.ExpiresAt > 0 {
			psd["copilotTokenExpiresAt"] = copilot.ExpiresAt
		}
	}
	if user.Login != "" {
		psd["githubLogin"] = user.Login
	}
	if user.Name != "" {
		psd["githubName"] = user.Name
	}
	if user.Email != "" {
		psd["githubEmail"] = user.Email
	}
	if user.ID != 0 {
		psd["githubUserId"] = user.ID
	}
	data["providerSpecificData"] = psd

	name := strings.TrimSpace(requestedName)
	if name == "" {
		name = strings.TrimSpace(user.Login)
	}
	if name == "" {
		name = strings.TrimSpace(user.Name)
	}
	if name == "" {
		name = "github account"
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("encode connection data: %w", err)
	}

	connID := "github-oauth-" + randomString(12)
	now := currentTimestamp()
	if _, err := h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, 'github', 'oauth', ?, 1, ?, ?, ?)`,
		connID, name, string(raw), now, now,
	); err != nil {
		return "", fmt.Errorf("save connection: %w", err)
	}
	return name, nil
}

// fetchGitHubIdentity derives the Copilot token and reads the account profile.
// Both calls are best-effort: a failure still yields a usable connection (chat
// can derive the Copilot token lazily), matching the reference's tolerant
// postExchange.
func fetchGitHubIdentity(githubAccessToken string) (githubCopilotToken, githubUser) {
	client := &http.Client{Timeout: 20 * time.Second}

	var copilot githubCopilotToken
	if req, err := http.NewRequest(http.MethodGet, githubCopilotTokenURL, nil); err == nil {
		// Authorization scheme is `token`, NOT `Bearer` — see RefreshGitHub.
		for k, v := range providers.GitHubCopilotRefreshHeaders("token " + githubAccessToken) {
			req.Header.Set(k, v)
		}
		req.Header.Set("Authorization", "token "+githubAccessToken)
		if resp, err := client.Do(req); err == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				_ = json.Unmarshal(raw, &copilot)
			}
		}
	}

	var user githubUser
	if req, err := http.NewRequest(http.MethodGet, githubUserURL, nil); err == nil {
		req.Header.Set("Authorization", "Bearer "+githubAccessToken)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", providers.GitHubCopilotAPIVersion)
		req.Header.Set("User-Agent", providers.GitHubCopilotChatUserAgent())
		if resp, err := client.Do(req); err == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				_ = json.Unmarshal(raw, &user)
			}
		}
	}

	return copilot, user
}
