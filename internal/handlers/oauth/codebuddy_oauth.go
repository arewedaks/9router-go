package oauth

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// CodeBuddy OAuth (Tencent copilot.tencent.com / codebuddy.ai).
//
// CodeBuddy does not use a redirect-based OAuth flow. Its CLI ships a custom
// *device-auth* handshake (mirrored from VansRouter's
// open-sse/providers/registry/codebuddy-{cn,intl}.js, whose OAuth tables are the
// authoritative description of both regions):
//
//  1. POST {stateUrl}?platform=<platform>  ->  { code: 0, data: { state, authUrl } }
//     The platform MUST be a query param (VansRouter sends an empty `{}` body).
//  2. Operator opens authUrl in a browser and approves.
//  3. GET {tokenUrl}?state=<state> until { code: 0, data.accessToken }.
//     code 11217 means "still pending, keep polling". The poll is a GET with
//     state as a *query param* (not POST/body) — this is the detail that
//     distinguishes the real client from a naive port.
//
// There is no client_id/secret; the upstream client ships none, so nothing
// needs to be registered.
//
// **The two regions are genuinely different endpoints**, not one backend with
// two names — the CN browser/CLI talks to copilot.tencent.com with
// platform=CLI, while the international build talks to www.codebuddy.ai with
// platform=ide and its own User-Agent. Using the CN host for an intl account
// yields a token the .ai gateway rejects, so the region must be threaded
// through every call (see codebuddyConfigFor).
//
// The whole handshake is stateless from the server's point of view: the browser
// holds the state, so the dashboard passes it straight back to /exchange. That
// keeps this usable on a headless server where no loopback callback can land.

// codebuddyConfig describes one region's device-auth endpoints and identity.
// Mirrors `oauth: {...}` in VansRouter's registry entries.
type codebuddyConfig struct {
	// Provider is the canonical provider id stored on the connection.
	Provider string
	// BaseURL is the region host (also the X-Domain header value).
	BaseURL string
	// StateURL / TokenURL begin the flow and poll for the token.
	StateURL string
	TokenURL string
	// Platform is sent as `?platform=`; "CLI" for CN, "ide" for intl.
	Platform string
	// UserAgent is the client identity the region expects.
	UserAgent string
	// IDEType drives the X-IDE-Type / X-IDE-Name headers ("CLI" or "IDE").
	IDEType string
	// Domain is the X-Domain header (the region host).
	Domain string
}

const (
	codebuddyCNBaseURL   = "https://copilot.tencent.com"
	codebuddyIntlBaseURL = "https://www.codebuddy.ai"
	codebuddyPollTimeout = 10 * time.Minute
)

// Overridable so tests can point the handshake at an httptest server. Only the
// CN entry is a var because the tests exercise that region; intl shares the
// same request builder.
var (
	codebuddyCNStateURL = codebuddyCNBaseURL + "/v2/plugin/auth/state"
	codebuddyCNTokenURL = codebuddyCNBaseURL + "/v2/plugin/auth/token"
)

// codebuddyConfigFor resolves a provider id (canonical or alias) to its region
// config. Returns false for anything that is not a CodeBuddy provider.
func codebuddyConfigFor(providerID string) (codebuddyConfig, bool) {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "codebuddy-cn", "cbcn":
		return codebuddyConfig{
			Provider:  "codebuddy-cn",
			BaseURL:   codebuddyCNBaseURL,
			StateURL:  codebuddyCNStateURL,
			TokenURL:  codebuddyCNTokenURL,
			Platform:  "CLI",
			UserAgent: "CLI/2.63.2 CodeBuddy/2.63.2",
			IDEType:   "CLI",
			Domain:    "copilot.tencent.com",
		}, true
	case "codebuddy-intl", "cbai":
		return codebuddyConfig{
			Provider:  "codebuddy-intl",
			BaseURL:   codebuddyIntlBaseURL,
			StateURL:  codebuddyIntlBaseURL + "/v2/plugin/auth/state",
			TokenURL:  codebuddyIntlBaseURL + "/v2/plugin/auth/token",
			Platform:  "ide",
			UserAgent: "IDE/2.63.2 CodeBuddy/2.63.2",
			IDEType:   "IDE",
			Domain:    "www.codebuddy.ai",
		}, true
	default:
		return codebuddyConfig{}, false
	}
}

// codebuddyHostFor returns the canonical provider id for a CodeBuddy alias.
func codebuddyHostFor(providerID string) (string, bool) {
	cfg, ok := codebuddyConfigFor(providerID)
	if !ok {
		return "", false
	}
	return cfg.Provider, true
}

// isCodebuddyProviderID reports whether a provider id addresses CodeBuddy.
func isCodebuddyProviderID(providerID string) bool {
	_, ok := codebuddyConfigFor(providerID)
	return ok
}

// codebuddyRequestHeaders is the header set the region's client sends on both
// handshake calls. X-No-Authorization tells the gateway the call is
// intentionally unauthenticated (we are obtaining the credential).
func codebuddyRequestHeaders(cfg codebuddyConfig) map[string]string {
	return map[string]string{
		"Accept":               "application/json",
		"Content-Type":         "application/json",
		"User-Agent":           cfg.UserAgent,
		"X-Requested-With":     "XMLHttpRequest",
		"X-Domain":             cfg.Domain,
		"X-No-Authorization":   "true",
		"X-No-User-Id":         "true",
		"X-No-Enterprise-Id":   "true",
		"X-No-Department-Info": "true",
		"X-Product":            "SaaS",
		"X-IDE-Type":           cfg.IDEType,
		"X-IDE-Name":           cfg.IDEType,
		"x-codebuddy-request":  "1",
	}
}

// codebuddyStateResponse is the envelope returned by the state endpoint.
type codebuddyStateResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
		URL     string `json:"url"`
	} `json:"data"`
}

// codebuddyTokenResponse is the envelope returned by the poll endpoint. It is
// also reused (with different field names) for the refresh call.
type codebuddyTokenResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		TokenType    string `json:"tokenType"`
		ExpiresIn    int    `json:"expiresIn"`
		Email        string `json:"email"`
		Nickname     string `json:"nickname"`
		Name         string `json:"name"`
		UserID       string `json:"userId"`
	} `json:"data"`
}

// HandleCodebuddyAuthorize starts the device-auth handshake.
//
// GET /api/oauth/codebuddy/authorize?provider=codebuddy-cn
//
// It returns { state, authUrl }: the dashboard opens authUrl and then polls
// /exchange with the same state until the operator approves.
func (h *OAuthHandler) HandleCodebuddyAuthorize(w http.ResponseWriter, r *http.Request) {
	providerParam := r.URL.Query().Get("provider")
	cfg, ok := codebuddyConfigFor(providerParam)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "unsupported codebuddy provider: "+providerParam)
		return
	}

	state, authURL, err := requestCodebuddyState(cfg)
	if err != nil {
		log.Warn("oauth", "codebuddy state request failed", "provider", cfg.Provider, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "could not start CodeBuddy sign-in: "+err.Error())
		return
	}
	if authURL == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "CodeBuddy returned no auth URL")
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider": cfg.Provider,
		"state":    state,
		"authUrl":  authURL,
		// The client polls this interval to match the CLI's cadence (5s).
		"pollIntervalMs": 5000,
	})
}

// requestCodebuddyState performs step 1 of the handshake.
func requestCodebuddyState(cfg codebuddyConfig) (state, authURL string, err error) {
	// The platform is sent as a query param (VansRouter sends `{}` as the body).
	endpoint := cfg.StateURL + "?platform=" + url.QueryEscape(cfg.Platform)

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", "", err
	}
	for k, v := range codebuddyRequestHeaders(cfg) {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("state endpoint returned HTTP %d: %s", resp.StatusCode, truncateForOAuth(string(raw), 160))
	}

	var parsed codebuddyStateResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", fmt.Errorf("state response is not valid JSON: %w", err)
	}
	if parsed.Code != 0 || parsed.Data.State == "" {
		msg := parsed.Msg
		if msg == "" {
			msg = "no state in response"
		}
		return "", "", fmt.Errorf("state error: %s", msg)
	}

	authURL = parsed.Data.AuthURL
	if authURL == "" {
		authURL = parsed.Data.URL
	}
	return parsed.Data.State, authURL, nil
}

// HandleCodebuddyExchange polls (once) for the tokens belonging to a state.
//
// POST /api/oauth/codebuddy/exchange  {"provider":"codebuddy-cn","state":"...","name":"..."}
//
// Returns 202 while the operator has not approved yet, so the dashboard keeps
// polling; 200 once the account is saved.
func (h *OAuthHandler) HandleCodebuddyExchange(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Provider string `json:"provider"`
		State    string `json:"state"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	cfg, ok := codebuddyConfigFor(req.Provider)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "unsupported codebuddy provider: "+req.Provider)
		return
	}
	if strings.TrimSpace(req.State) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing state (start a sign-in first)")
		return
	}

	tokens, pending, err := pollCodebuddyToken(req.State, cfg)
	if err != nil {
		log.Warn("oauth", "codebuddy token poll failed", "provider", cfg.Provider, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "token poll failed: "+err.Error())
		return
	}
	if pending {
		// 202: the browser keeps polling. Not an error.
		handlerutil.WriteJSON(w, http.StatusAccepted, map[string]any{
			"pending": true,
			"message": "Waiting for you to approve the sign-in in the browser…",
		})
		return
	}
	if tokens.Data.AccessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "CodeBuddy returned no access token")
		return
	}

	name, err := h.saveCodebuddyConnection(cfg.Provider, tokens, req.Name)
	if err != nil {
		log.Error("oauth", "save codebuddy connection failed", "provider", cfg.Provider, "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := map[string]any{"provider": cfg.Provider, "name": name}
	if tokens.Data.Email != "" {
		resp["email"] = tokens.Data.Email
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// pollCodebuddyToken performs one poll iteration. pending=true means the
// operator has not approved yet (upstream code 11217 / RetryFetchToken).
func pollCodebuddyToken(state string, cfg codebuddyConfig) (tokens codebuddyTokenResponse, pending bool, err error) {
	endpoint := cfg.TokenURL + "?state=" + url.QueryEscape(state)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return tokens, false, err
	}
	// The poll is intentionally unauthenticated and carries the extra
	// X-No-Enterprise-Id / X-No-Department-Info headers the client sends.
	for k, v := range codebuddyRequestHeaders(cfg) {
		// GET has no body; Content-Type is harmless but drop it for cleanliness.
		if k == "Content-Type" {
			continue
		}
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
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

	// code 0 with a token  -> done
	if tokens.Code == 0 && tokens.Data.AccessToken != "" {
		return tokens, false, nil
	}
	// 11217 (RetryFetchToken) and anything else without a token is "keep waiting".
	// Upstream also returns code 0 with empty data while pending on some deploys,
	// so treat both as pending rather than a hard failure.
	if tokens.Code == 0 || tokens.Code == 11217 {
		return tokens, true, nil
	}
	return tokens, false, fmt.Errorf("token error: code %d %s", tokens.Code, tokens.Msg)
}

// saveCodebuddyConnection persists the account as a normal provider connection.
func (h *OAuthHandler) saveCodebuddyConnection(canonical string, tokens codebuddyTokenResponse, requestedName string) (string, error) {
	name := strings.TrimSpace(requestedName)
	if name == "" {
		name = strings.TrimSpace(tokens.Data.Email)
	}
	if name == "" {
		name = strings.TrimSpace(tokens.Data.Nickname)
	}
	if name == "" {
		name = strings.TrimSpace(tokens.Data.Name)
	}
	if name == "" {
		name = canonical + " account"
	}

	data := map[string]any{
		"accessToken": tokens.Data.AccessToken,
	}
	if tokens.Data.RefreshToken != "" {
		data["refreshToken"] = tokens.Data.RefreshToken
	}
	// Default to 24h when the upstream omits expiresIn (the CLI assumes the
	// token is long-lived; a wrong short value would trigger needless refreshes).
	expiresIn := tokens.Data.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 86400
	}
	data["expiresIn"] = expiresIn
	data["expiresAt"] = time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339)
	if tokens.Data.TokenType != "" {
		data["tokenType"] = tokens.Data.TokenType
	}
	if tokens.Data.UserID != "" {
		data["userId"] = tokens.Data.UserID
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("encode connection data: %w", err)
	}

	connID := canonical + "-oauth-" + randomString(12)
	now := currentTimestamp()
	if _, err := h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'oauth', ?, 1, ?, ?, ?)`,
		connID, canonical, name, string(raw), now, now,
	); err != nil {
		return "", fmt.Errorf("save connection: %w", err)
	}
	return name, nil
}

// truncateForOAuth shortens an upstream body for an error message without
// splitting a UTF-8 rune.
func truncateForOAuth(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
