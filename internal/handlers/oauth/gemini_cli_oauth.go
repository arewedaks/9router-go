package oauth

// Gemini CLI OAuth (Google consumer client).
//
// Gemini CLI authenticates with Google using a standard authorization-code flow
// with a client_secret (NOT PKCE). This matches the VansRouter/open-sse
// implementation. The OAuth client is the public installed-app client bundled
// with the Gemini CLI binary.
//
// Flow:
//  1. GET /api/oauth/gemini-cli/authorize  — returns authUrl + state.
//  2. User approves in a browser tab.
//  3. POST /api/oauth/gemini-cli/exchange  — exchanges code → tokens → saves
//     connection.  Accepts the full callback URL or a bare code.
//  4. GET /oauth/gemini-cli/callback       — loopback shortcut for local
//     installs; completes the exchange automatically.

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

const (
	geminiCLIAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	geminiCLITokenURL     = "https://oauth2.googleapis.com/token"
	geminiCLIUserInfoURL  = "https://www.googleapis.com/oauth2/v1/userinfo"

	// loadCodeAssist probes the Cloud Code project bound to this account.
	geminiCLILoadCodeAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"

	// Default loopback matches the route mounted at /oauth/gemini-cli/callback.
	geminiCLIDefaultRedirectURI = "http://localhost:20128/oauth/gemini-cli/callback"

	geminiCLIFlowTTL = 10 * time.Minute
)

// geminiCLIScopes matches the VansRouter/open-sse registry for gemini-cli.
// Exactly 3 scopes: cloud-platform, userinfo.email, userinfo.profile.
// openid is deliberately absent.
var geminiCLIScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

type geminiCLIPendingFlow struct {
	redirectURI string
	createdAt   time.Time
}

var (
	geminiCLIFlowsMu sync.Mutex
	geminiCLIFlows   = map[string]geminiCLIPendingFlow{}
)

func storeGeminiCLIFlow(state string, f geminiCLIPendingFlow) {
	geminiCLIFlowsMu.Lock()
	defer geminiCLIFlowsMu.Unlock()
	for k, v := range geminiCLIFlows {
		if time.Since(v.createdAt) > geminiCLIFlowTTL {
			delete(geminiCLIFlows, k)
		}
	}
	geminiCLIFlows[state] = f
}

func takeGeminiCLIFlow(state string) (geminiCLIPendingFlow, bool) {
	geminiCLIFlowsMu.Lock()
	defer geminiCLIFlowsMu.Unlock()
	f, ok := geminiCLIFlows[state]
	if ok {
		delete(geminiCLIFlows, state)
	}
	return f, ok
}

// HandleGeminiCLIAuthorize starts the Gemini CLI OAuth flow.
// GET /api/oauth/gemini-cli/authorize?redirectUri=...
//
// Unlike Antigravity this is a plain authorization-code flow (no PKCE):
// the Gemini CLI OAuth client is a "web" client that uses client_secret on
// the token endpoint.
func (h *OAuthHandler) HandleGeminiCLIAuthorize(w http.ResponseWriter, r *http.Request) {
	cfg, ok := providers.KnownOAuthConfigs["gemini-cli"]
	if !ok || cfg.ClientID == "" {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "gemini-cli OAuth client not configured")
		return
	}

	redirectURI := strings.TrimSpace(r.URL.Query().Get("redirectUri"))
	if redirectURI == "" {
		redirectURI = loopbackRedirectURI(r, "/oauth/gemini-cli/callback")
	}

	state := randomString(32)

	storeGeminiCLIFlow(state, geminiCLIPendingFlow{
		redirectURI: redirectURI,
		createdAt:   time.Now(),
	})

	q := url.Values{
		"client_id":     {cfg.ClientID},
		"response_type": {"code"},
		"redirect_uri":  {redirectURI},
		"scope":         {strings.Join(geminiCLIScopes, " ")},
		"state":         {state},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
	}
	authURL := geminiCLIAuthorizeURL + "?" + q.Encode()

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"authUrl":    authURL,
		"state":      state,
		"redirectUri": redirectURI,
	})
}

// HandleGeminiCLIExchange completes the Gemini CLI OAuth flow.
// POST /api/oauth/gemini-cli/exchange
// Body: {"code"|"callbackUrl", "state", "redirectUri", "name"}
func (h *OAuthHandler) HandleGeminiCLIExchange(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Code        string `json:"code"`
		CallbackURL string `json:"callbackUrl"`
		State       string `json:"state"`
		RedirectURI string `json:"redirectUri"`
		Name        string `json:"name"`
		Replace     bool   `json:"replace"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Accept bare code or full callback URL.
	code := strings.TrimSpace(req.Code)
	if code == "" && req.CallbackURL != "" {
		parsed, err := url.Parse(strings.TrimSpace(req.CallbackURL))
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid callbackUrl")
			return
		}
		code = parsed.Query().Get("code")
		if req.State == "" {
			req.State = parsed.Query().Get("state")
		}
		if code == "" {
			if e := parsed.Query().Get("error"); e != "" {
				handlerutil.WriteJSONError(w, http.StatusBadRequest, "authorization failed: "+e)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "no code in callbackUrl")
			return
		}
	}
	if code == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code")
		return
	}

	// Recover the redirect URI from the pending flow.
	redirectURI := strings.TrimSpace(req.RedirectURI)
	if req.State != "" {
		if flow, ok := takeGeminiCLIFlow(req.State); ok {
			if redirectURI == "" {
				redirectURI = flow.redirectURI
			}
		}
	}
	if redirectURI == "" {
		// The authorize call stored the real URI; this only covers a request that
		// reached exchange without a state, where the configured default is the
		// only thing left to try.
		redirectURI = geminiCLIDefaultRedirectURI
	}

	cfg := providers.KnownOAuthConfigs["gemini-cli"]
	tokens, err := exchangeGeminiCLICode(cfg, code, redirectURI)
	if err != nil {
		log.Warn("oauth", "gemini-cli token exchange failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "token exchange failed: "+err.Error())
		return
	}
	if tokens.AccessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "token exchange returned no access token")
		return
	}

	name, email, projectID, err := h.saveGeminiCLIConnection(tokens, req.Name, req.Replace)
	if err != nil {
		var dup *DuplicateError
		if errors.As(err, &dup) {
			DuplicateConnectionErrorToHTTP(w, dup)
			return
		}
		log.Error("oauth", "save gemini-cli connection failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := map[string]any{
		"provider": "gemini-cli",
		"name":     name,
	}
	if email != "" {
		resp["email"] = email
	}
	if projectID != "" {
		resp["projectId"] = projectID
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// HandleGeminiCLICallback is the loopback redirect target for local installs.
// GET /oauth/gemini-cli/callback?code=...&state=...
func (h *OAuthHandler) HandleGeminiCLICallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		writeGeminiCLICallbackPage(w, http.StatusBadRequest, "Authorization declined", errParam)
		return
	}
	if code == "" || state == "" {
		writeGeminiCLICallbackPage(w, http.StatusBadRequest, "Bad callback", "Missing code or state parameter.")
		return
	}

	flow, ok := takeGeminiCLIFlow(state)
	if !ok {
		writeGeminiCLICallbackPage(w, http.StatusBadRequest, "Unknown state",
			"This link has already been used or has expired. Return to the dashboard to try again.")
		return
	}

	cfg := providers.KnownOAuthConfigs["gemini-cli"]
	tokens, err := exchangeGeminiCLICode(cfg, code, flow.redirectURI)
	if err != nil {
		log.Warn("oauth", "gemini-cli callback exchange failed", "error", err)
		writeGeminiCLICallbackPage(w, http.StatusBadGateway, "Token exchange failed", err.Error())
		return
	}

	name, email, _, err := h.saveGeminiCLIConnection(tokens, "", false)
	if err != nil {
		log.Error("oauth", "gemini-cli callback save failed", "error", err)
		writeGeminiCLICallbackPage(w, http.StatusInternalServerError, "Save failed", err.Error())
		return
	}

	detail := name
	if email != "" && email != name {
		detail = email + " (" + name + ")"
	}
	writeGeminiCLICallbackPage(w, http.StatusOK, "Signed in", "Account added: "+detail+". You can close this tab.")
}

func writeGeminiCLICallbackPage(w http.ResponseWriter, status int, title, detail string) {
	writeCallbackPage(w, status, title, detail, "gemini-cli")
}

// saveGeminiCLIConnection persists the account and returns (name, email, projectID, err).
func (h *OAuthHandler) saveGeminiCLIConnection(tokens *geminiCLITokenPayload, requestedName string, replace bool) (name, email, projectID string, err error) {
	email = fetchGeminiCLIEmail(tokens.AccessToken)
	projectID = probeGeminiCLIProject(tokens.AccessToken)

	name = strings.TrimSpace(requestedName)
	if name == "" {
		name = email
	}
	if name == "" {
		name = "Gemini CLI account"
	}

	connID := "gemini-cli-oauth-" + randomString(12)
	data := map[string]any{
		"accessToken": tokens.AccessToken,
	}
	if tokens.RefreshToken != "" {
		data["refreshToken"] = tokens.RefreshToken
	}
	if tokens.ExpiresIn > 0 {
		data["expiresAt"] = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		data["expiresIn"] = tokens.ExpiresIn
	}
	if tokens.Scope != "" {
		data["scope"] = tokens.Scope
	}
	if projectID != "" {
		data["projectId"] = projectID
	}

	candidate := IdentityForProvider("gemini-cli", tokens.AccessToken, email)
	dup, derr := h.FindDuplicateConnection("gemini-cli", candidate)
	if derr != nil {
		return "", email, projectID, fmt.Errorf("duplicate check: %w", derr)
	}
	if dup != nil {
		if !replace {
			logDuplicateSuppressed("gemini-cli", dup)
			return "", email, projectID, dup
		}
		if err := h.replaceConnectionTokens(dup.ExistingConnID, name, data); err != nil {
			return "", email, projectID, fmt.Errorf("replace connection: %w", err)
		}
		log.Info("oauth", "duplicate account re-login refreshed existing connection",
			"provider", "gemini-cli", "conn", dup.ExistingConnID)
		return name, email, projectID, nil
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return "", "", "", fmt.Errorf("encode connection data: %w", err)
	}

	now := currentTimestamp()
	priority, perr := h.Repo.NextConnectionPriority("gemini-cli")
	if perr != nil {
		return "", "", "", fmt.Errorf("rank connection: %w", perr)
	}
	var emailArg any
	if email != "" {
		emailArg = email
	}
	if _, err := h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt) VALUES (?, 'gemini-cli', 'oauth', ?, ?, ?, 1, ?, ?, ?)`,
		connID, name, emailArg, priority, string(raw), now, now,
	); err != nil {
		return "", "", "", fmt.Errorf("save connection: %w", err)
	}
	return name, email, projectID, nil
}

type geminiCLITokenPayload struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Scope        string
}

// exchangeGeminiCLICode swaps an authorization code for tokens using
// client_secret (no PKCE verifier).
func exchangeGeminiCLICode(cfg providers.OAuthClientConfig, code, redirectURI string) (*geminiCLITokenPayload, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}

	req, err := http.NewRequest(http.MethodPost, geminiCLITokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var decoded struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return &geminiCLITokenPayload{
		AccessToken:  decoded.AccessToken,
		RefreshToken: decoded.RefreshToken,
		ExpiresIn:    decoded.ExpiresIn,
		Scope:        decoded.Scope,
	}, nil
}

func fetchGeminiCLIEmail(accessToken string) string {
	req, err := http.NewRequest(http.MethodGet, geminiCLIUserInfoURL+"?alt=json", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()

	var info struct {
		Email string `json:"email"`
	}
	if raw, err := io.ReadAll(resp.Body); err == nil {
		_ = json.Unmarshal(raw, &info)
	}
	return info.Email
}

// probeGeminiCLIProject calls loadCodeAssist to discover the Cloud Code
// project bound to this account. Non-fatal — a missing project is normal
// and is discovered lazily during chat.
func probeGeminiCLIProject(accessToken string) string {
	payload := map[string]any{
		"metadata": map[string]any{"ideType": 9, "platform": 3, "pluginType": 2},
		"mode":     1,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ""
	}

	req, err := http.NewRequest(http.MethodPost, geminiCLILoadCodeAssistURL, strings.NewReader(string(body)))
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return ""
	}

	// extractAntigravityProjectID handles both string and {id:...} shapes.
	return extractAntigravityProjectID(data["cloudaicompanionProject"])
}
