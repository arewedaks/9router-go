package oauth

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

// Antigravity OAuth (Google consumer client).
//
// Antigravity is a desktop/CLI product from Google that talks to the Cloud Code
// Assist backend. It authenticates with a *public* installed-app OAuth client
// (the same client_id ships inside the Antigravity binaries), so we can embed
// the credentials and let operators add an account straight from the dashboard
// instead of hand-importing tokens.
//
// The flow mirrors OmniRoute's `createAntigravityOAuthProvider`:
//
//  1. Build a Google consent URL (no `openid` scope — requesting it routes the
//     consent screen into a hanging first-party native-app flow) carrying a
//     PKCE challenge.
//  2. Operator approves, Google redirects to the loopback URI with `?code=`.
//     On a headless server the loopback is unreachable, so we also accept the
//     pasted callback URL / raw code.
//  3. Exchange the code for tokens, then probe loadCodeAssist/onboardUser for
//     the Cloud Code project id and onboarding tier.
//  4. Persist a normal `antigravity` provider connection so the rest of the
//     router (chat, quota, model discovery) treats it like any other account.

const (
	antigravityAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	antigravityTokenURL     = "https://oauth2.googleapis.com/token"
	antigravityUserInfoURL  = "https://www.googleapis.com/oauth2/v1/userinfo"
	// Discovery/onboarding stay on the production host: the daily host rejects
	// these RPCs (parity with the fork's existing antigravity_project.go).
	antigravityLoadCodeAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	antigravityOnboardUserURL    = "https://cloudcode-pa.googleapis.com/v1internal:onboardUser"
)

// antigravityScopes matches the working 9router/OmniRoute flow. `openid` is
// deliberately absent: with PKCE it sends Google into the hanging
// `firstparty/nativeapp` consent path.
var antigravityScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// antigravityLCAMetadata is the IDE metadata block Google expects on the
// loadCodeAssist/onboardUser RPCs.
var antigravityLCAMetadata = map[string]any{
	"ideType":    9, // ANTIGRAVITY
	"platform":   2, // DARWIN_ARM64
	"pluginType": 2, // GEMINI
}

// antigravityDefaultRedirectURI is the loopback callback the dashboard uses by
// default. It matches the route mounted at /oauth/antigravity/callback.
const antigravityDefaultRedirectURI = "http://localhost:20128/oauth/antigravity/callback"

// antigravityPendingFlow holds the per-state PKCE verifier between authorize
// and exchange. The dashboard is a single-operator surface, so a small
// in-memory map with a short TTL is enough; entries are consumed on exchange.
type antigravityPendingFlow struct {
	verifier    string
	profile     string
	redirectURI string
	createdAt   time.Time
}

var (
	antigravityFlowMu    sync.Mutex
	antigravityFlowStore = map[string]antigravityPendingFlow{}
)

const antigravityFlowTTL = 10 * time.Minute

// antigravityClientProfile normalises the requested profile, defaulting to IDE
// so behaviour matches a connection added without an explicit choice.
func antigravityClientProfile(raw string) providers.AntigravityClientProfile {
	return providers.NormalizeAntigravityClientProfile(raw)
}

// antigravityOAuthUserAgent returns the User-Agent Google should see on the
// token endpoint for the chosen client profile. OmniRoute distinguishes the
// IDE and CLI fingerprints here; reuse the shared helper so the identity stays
// consistent with chat traffic.
func antigravityOAuthUserAgent(profile providers.AntigravityClientProfile) string {
	return providers.AntigravityUserAgent(profile)
}

func storeAntigravityFlow(state string, flow antigravityPendingFlow) {
	antigravityFlowMu.Lock()
	defer antigravityFlowMu.Unlock()
	if flow.createdAt.IsZero() {
		flow.createdAt = time.Now()
	}
	// Opportunistic GC so a long-lived process cannot accumulate stale states.
	for k, v := range antigravityFlowStore {
		if time.Since(v.createdAt) > antigravityFlowTTL {
			delete(antigravityFlowStore, k)
		}
	}
	antigravityFlowStore[state] = flow
}

func takeAntigravityFlow(state string) (antigravityPendingFlow, bool) {
	antigravityFlowMu.Lock()
	defer antigravityFlowMu.Unlock()
	flow, ok := antigravityFlowStore[state]
	if !ok {
		return antigravityPendingFlow{}, false
	}
	delete(antigravityFlowStore, state)
	if time.Since(flow.createdAt) > antigravityFlowTTL {
		return antigravityPendingFlow{}, false
	}
	return flow, true
}

// HandleAntigravityAuthorize starts the Antigravity OAuth flow.
// GET /api/oauth/antigravity/authorize?profile=ide|cli&redirectUri=...
//
// The response carries everything the UI needs to open the consent screen and
// to complete the exchange afterwards.
func (h *OAuthHandler) HandleAntigravityAuthorize(w http.ResponseWriter, r *http.Request) {
	cfg, ok := providers.KnownOAuthConfigs["antigravity"]
	if !ok || cfg.ClientID == "" {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "antigravity OAuth client not configured")
		return
	}

	profile := antigravityClientProfile(r.URL.Query().Get("profile"))

	// Antigravity's embedded OAuth client is an installed-app client, so the
	// only redirect it accepts is a loopback URI. Default to a port the
	// dashboard does not need to serve: the operator pastes the callback URL
	// back, which is the only flow that survives a headless/Termux server.
	redirectURI := strings.TrimSpace(r.URL.Query().Get("redirectUri"))
	if redirectURI == "" {
		redirectURI = antigravityDefaultRedirectURI
	}

	verifier := randomString(64)
	challenge := sha256Base64(verifier)
	state := randomString(32)

	storeAntigravityFlow(state, antigravityPendingFlow{
		verifier:    verifier,
		profile:     string(profile),
		redirectURI: redirectURI,
		createdAt:   time.Now(),
	})

	q := url.Values{
		"client_id":             {cfg.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(antigravityScopes, " ")},
		"state":                 {state},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	authURL := antigravityAuthorizeURL + "?" + q.Encode()

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"authUrl":      authURL,
		"state":        state,
		"codeVerifier": verifier,
		"redirectUri":  redirectURI,
		"profile":      string(profile),
	})
}

// HandleAntigravityExchange completes the Antigravity OAuth flow.
// POST /api/oauth/antigravity/exchange
// Body: {"code"|"callbackUrl", "state", "codeVerifier", "redirectUri", "profile", "name"}
//
// On success a provider connection is created and returned.
func (h *OAuthHandler) HandleAntigravityExchange(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Code         string `json:"code"`
		CallbackURL  string `json:"callbackUrl"`
		State        string `json:"state"`
		CodeVerifier string `json:"codeVerifier"`
		RedirectURI  string `json:"redirectUri"`
		Profile      string `json:"profile"`
		Name         string `json:"name"`
		Replace      bool   `json:"replace"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Accept either a bare code or the whole redirect URL the browser landed on.
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

	// Recover the PKCE verifier from the pending flow unless the client echoed
	// it back (the authorize response already returned it, so either works).
	verifier := req.CodeVerifier
	profile := antigravityClientProfile(req.Profile)
	if req.State != "" {
		if flow, ok := takeAntigravityFlow(req.State); ok {
			if verifier == "" {
				verifier = flow.verifier
			}
			if req.Profile == "" {
				profile = antigravityClientProfile(flow.profile)
			}
		}
	}
	if verifier == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing codeVerifier (unknown or expired state)")
		return
	}

	cfg := providers.KnownOAuthConfigs["antigravity"]
	redirectURI := strings.TrimSpace(req.RedirectURI)
	if redirectURI == "" {
		redirectURI = antigravityDefaultRedirectURI
	}

	tokens, err := exchangeAntigravityCode(cfg, profile, code, verifier, redirectURI)
	if err != nil {
		log.Warn("oauth", "antigravity token exchange failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "token exchange failed: "+err.Error())
		return
	}
	if tokens.AccessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "token exchange returned no access token")
		return
	}

	name, email, projectID, err := h.saveAntigravityConnection(tokens, profile, req.Name, req.Replace)
	if err != nil {
		var dup *DuplicateError
		if errors.As(err, &dup) {
			DuplicateConnectionErrorToHTTP(w, dup)
			return
		}
		log.Error("oauth", "save antigravity connection failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := map[string]any{
		"provider":      "antigravity",
		"name":          name,
		"clientProfile": string(profile),
	}
	if email != "" {
		resp["email"] = email
	}
	if projectID != "" {
		resp["projectId"] = projectID
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// HandleAntigravityCallback closes the browser redirect. When the operator's
// browser can reach this server (local install), Google lands here with the
// code and we finish the exchange without any copy/paste. When it cannot
// (remote/headless), the request never arrives and the dashboard's paste field
// is used instead — so failures here are only ever cosmetic.
//
// GET /oauth/antigravity/callback?code=...&state=...
func (h *OAuthHandler) HandleAntigravityCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		writeAntigravityCallbackPage(w, http.StatusBadRequest,
			"Authorization failed", "Google reported: "+e+". You can close this tab and try again.")
		return
	}
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		writeAntigravityCallbackPage(w, http.StatusBadRequest,
			"Missing parameters", "The callback did not include a code and state. Close this tab and start over.")
		return
	}

	flow, ok := takeAntigravityFlow(state)
	if !ok {
		writeAntigravityCallbackPage(w, http.StatusBadRequest,
			"Link expired", "This sign-in link is no longer valid (it may have already been used). Close this tab and start again "+
				"from the dashboard. If this is a remote server, paste the address-bar URL into the dashboard instead of opening it here.")
		return
	}

	cfg := providers.KnownOAuthConfigs["antigravity"]
	profile := antigravityClientProfile(flow.profile)
	redirectURI := flow.redirectURI
	if redirectURI == "" {
		redirectURI = antigravityDefaultRedirectURI
	}

	tokens, err := exchangeAntigravityCode(cfg, profile, code, flow.verifier, redirectURI)
	if err != nil {
		log.Warn("oauth", "antigravity callback exchange failed", "error", err)
		writeAntigravityCallbackPage(w, http.StatusBadGateway,
			"Exchange failed", "Google rejected the authorization code: "+err.Error())
		return
	}
	if tokens.AccessToken == "" {
		writeAntigravityCallbackPage(w, http.StatusBadGateway,
			"Exchange failed", "Google returned no access token.")
		return
	}

	name, _, _, err := h.saveAntigravityConnection(tokens, profile, "", false)
	if err != nil {
		log.Error("oauth", "antigravity callback save failed", "error", err)
		writeAntigravityCallbackPage(w, http.StatusInternalServerError,
			"Save failed", "Signed in, but the connection could not be stored: "+err.Error())
		return
	}

	writeAntigravityCallbackPage(w, http.StatusOK,
		"Account connected",
		fmt.Sprintf("Added %q. You can close this tab and return to the dashboard.", name))
}

// writeAntigravityCallbackPage renders a tiny self-contained status page so the
// browser tab the operator lands on explains what happened.
func writeAntigravityCallbackPage(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>%s</title>`+
		`<style>body{background:#090d13;color:#e6edf3;font-family:-apple-system,Segoe UI,Roboto,sans-serif;`+
		`display:flex;align-items:center;justify-content:center;height:100vh;margin:0}`+
		`.c{max-width:440px;padding:28px;background:#121820;border:1px solid #232f3e;border-radius:12px;text-align:center}`+
		`h1{font-size:1.05rem;margin:0 0 10px}p{color:#8b949e;font-size:0.85rem;line-height:1.5;margin:0}`+
		`</style></head><body><div class="c"><h1>%s</h1><p>%s</p></div></body></html>`,
		htmlEscape(title), htmlEscape(title), htmlEscape(detail))
}

// htmlEscape is a minimal escaper for the callback status page.
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// saveAntigravityConnection persists a freshly authorized account and returns
// the stored connection name plus the enrichment values (email, project id)
// callers may want to echo. Shared by the paste-back exchange and the loopback
// callback so both paths write identical rows.
func (h *OAuthHandler) saveAntigravityConnection(tokens *antigravityTokenPayload, profile providers.AntigravityClientProfile, requestedName string, replace bool) (name, email, projectID string, err error) {
	// Best-effort enrichment: email for the label, project id for chat. Neither
	// is fatal — the connection works and the project is discovered lazily.
	email = fetchAntigravityEmail(tokens.AccessToken)
	projectID = probeAntigravityProject(tokens.AccessToken)

	name = strings.TrimSpace(requestedName)
	if name == "" {
		name = email
	}
	if name == "" {
		name = "Antigravity account"
	}

	connID := "antigravity-oauth-" + randomString(12)
	data := map[string]any{
		"accessToken": tokens.AccessToken,
		"providerSpecificData": map[string]any{
			"clientProfile": string(profile),
		},
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

	// Reject a re-login of an already-connected account unless the caller asked to
	// replace it. Antigravity identity is the Google account email.
	candidate := IdentityForProvider("antigravity", tokens.AccessToken, email)
	dup, derr := h.FindDuplicateConnection("antigravity", candidate)
	if derr != nil {
		return "", email, projectID, fmt.Errorf("duplicate check: %w", derr)
	}
	if dup != nil {
		if !replace {
			logDuplicateSuppressed("antigravity", dup)
			return "", email, projectID, dup
		}
		if err := h.replaceConnectionTokens(dup.ExistingConnID, name, data); err != nil {
			return "", email, projectID, fmt.Errorf("replace connection: %w", err)
		}
		log.Info("oauth", "duplicate account re-login refreshed existing connection",
			"provider", "antigravity", "conn", dup.ExistingConnID, "identity", dup.Identity.Label())
		return name, email, projectID, nil
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return "", "", "", fmt.Errorf("encode connection data: %w", err)
	}

	now := currentTimestamp()
	if _, err := h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, 'antigravity', 'oauth', ?, 1, ?, ?, ?)`,
		connID, name, string(raw), now, now,
	); err != nil {
		return "", "", "", fmt.Errorf("save connection: %w", err)
	}
	return name, email, projectID, nil
}

// antigravityTokenPayload is the subset of Google's token response we persist.
type antigravityTokenPayload struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Scope        string
}

// exchangeAntigravityCode swaps an authorization code for tokens.
func exchangeAntigravityCode(cfg providers.OAuthClientConfig, profile providers.AntigravityClientProfile, code, verifier, redirectURI string) (*antigravityTokenPayload, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {cfg.ClientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}

	req, err := http.NewRequest(http.MethodPost, antigravityTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", antigravityOAuthUserAgent(profile))

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
	return &antigravityTokenPayload{
		AccessToken:  decoded.AccessToken,
		RefreshToken: decoded.RefreshToken,
		ExpiresIn:    decoded.ExpiresIn,
		Scope:        decoded.Scope,
	}, nil
}

// fetchAntigravityEmail reads the account email for the connection label.
// Failure is non-fatal — it only costs a nicer default name.
func fetchAntigravityEmail(accessToken string) string {
	req, err := http.NewRequest(http.MethodGet, antigravityUserInfoURL+"?alt=json", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.UnmarshalRead(resp.Body, &info); err != nil {
		return ""
	}
	return info.Email
}

// probeAntigravityProject runs loadCodeAssist (falling back to onboardUser) to
// find the Cloud Code project bound to this account. It never returns an error:
// a missing project is a normal state that the chat path already handles.
func probeAntigravityProject(accessToken string) string {
	client := &http.Client{Timeout: 15 * time.Second}

	data, err := antigravityLCACall(client, antigravityLoadCodeAssistURL, accessToken, map[string]any{
		"metadata": antigravityLCAMetadata,
	})
	if err != nil {
		log.Warn("oauth", "antigravity loadCodeAssist failed", "error", err)
		return ""
	}
	if pid := extractAntigravityProjectID(data["cloudaicompanionProject"]); pid != "" {
		return pid
	}

	// No project yet — the account likely needs onboarding. One bounded attempt
	// only: hammering these RPCs is what triggers Google's anti-abuse limits.
	tierID := antigravityOnboardTierID(data)
	onboardData, err := antigravityLCACall(client, antigravityOnboardUserURL, accessToken, map[string]any{
		"tierId":   tierID,
		"metadata": antigravityLCAMetadata,
	})
	if err != nil {
		log.Warn("oauth", "antigravity onboardUser failed", "error", err)
		return ""
	}
	if pid := extractAntigravityProjectID(onboardData["cloudaicompanionProject"]); pid != "" {
		return pid
	}

	// Re-probe once: onboarding can accept before the project materialises.
	if data, err := antigravityLCACall(client, antigravityLoadCodeAssistURL, accessToken, map[string]any{
		"metadata": antigravityLCAMetadata,
	}); err == nil {
		if pid := extractAntigravityProjectID(data["cloudaicompanionProject"]); pid != "" {
			return pid
		}
	}
	return ""
}

// antigravityLCACall POSTs one Cloud Code Assist RPC and decodes the JSON body.
func antigravityLCACall(client *http.Client, endpoint, accessToken string, payload map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "google-api-nodejs-client/10.3.0")
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.21.1")
	if meta, err := json.Marshal(antigravityLCAMetadata); err == nil {
		req.Header.Set("Client-Metadata", string(meta))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// extractAntigravityProjectID accepts the string or {id:...} shapes Google uses.
func extractAntigravityProjectID(val any) string {
	switch v := val.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if id, _ := v["id"].(string); id != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

// antigravityOnboardTierID mirrors OmniRoute's tier selection order:
// paidTier → currentTier → default allowedTier → legacy-tier.
func antigravityOnboardTierID(data map[string]any) string {
	if id := tierFieldID(data["paidTier"]); id != "" {
		return id
	}
	if id := tierFieldID(data["currentTier"]); id != "" {
		return id
	}
	if allowed, ok := data["allowedTiers"].([]any); ok {
		for _, t := range allowed {
			m, ok := t.(map[string]any)
			if !ok {
				continue
			}
			if isDefault, _ := m["isDefault"].(bool); isDefault {
				if id, _ := m["id"].(string); id != "" {
					return strings.TrimSpace(id)
				}
			}
		}
	}
	if id := tierFieldID(data["currentTier"]); id != "" {
		return id
	}
	return "legacy-tier"
}

func tierFieldID(val any) string {
	m, ok := val.(map[string]any)
	if !ok {
		return ""
	}
	id, _ := m["id"].(string)
	return strings.TrimSpace(id)
}

// truncate caps a payload for log/error messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// antigravityPKCEChallenge is exported for tests that want to assert the
// challenge derivation matches the verifier.
func antigravityPKCEChallenge(verifier string) string {
	return sha256Base64(verifier)
}
