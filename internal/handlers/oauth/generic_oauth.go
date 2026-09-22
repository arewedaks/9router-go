package oauth

// Generic OAuth flows, driven by a per-provider spec table.
//
// VansRouter keeps every flow in one PROVIDERS map and dispatches through a
// single catch-all route. This mirrors that shape: a spec table describes each
// provider's endpoints and flow kind, and three handlers implement the kinds
// once instead of once per provider.
//
//   - flowDeviceCode: request a device code, poll until the operator approves
//     (github, kiro, kimi, qoder, kilocode, grok-cli)
//   - flowAuthCode:   build a consent URL, exchange the returned code
//     (claude, gitlab, iflow, xai)
//   - flowImport:     the operator pastes a credential the CLI already holds
//     (cursor, codex)
//
// Specs are transcribed from VansRouter's open-sse/providers/registry/*.js
// `oauth:` blocks.

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
)

// flowKind selects which handler drives a provider.
type flowKind string

const (
	flowDeviceCode flowKind = "device_code"
	flowAuthCode   flowKind = "authorization_code"
	flowImport     flowKind = "import_token"
)

// oauthSpec describes one provider's sign-in flow.
type oauthSpec struct {
	Provider string
	Kind     flowKind

	// Device-code endpoints.
	DeviceCodeURL string
	TokenURL      string
	// DeviceCodeBody carries extra form fields (client_id is added from
	// ClientID). Values may be empty when the provider needs none.
	DeviceCodeBody map[string]string
	// PollBody carries the fields sent on every poll, on top of grant_type and
	// device_code. Kimi needs client_id here too.
	PollBody map[string]string
	// PollIsForm selects an x-www-form-urlencoded poll (RFC 8628 providers);
	// false uses JSON (Kiro, Kilocode).
	PollIsForm bool
	// Headers are extra request headers for every upstream call.
	Headers map[string]string

	// Authorization-code endpoints.
	AuthorizeURL string
	Scopes       []string
	// ExtraAuthParams adds provider-specific query params (iflow's loginMethod
	// and type, for example).
	ExtraAuthParams map[string]string
	// PKCE selects S256 code_challenge handling.
	PKCE bool
	// ClientSecret, when set, is sent on the token exchange.
	ClientSecret string

	// Import describes what the operator pastes for flowImport.
	ImportHint string
}

// ClientID is the OAuth client id. Kept separate from the spec so it can come
// from KnownOAuthConfigs (which already supports an env override per provider).
func (s oauthSpec) clientID() string {
	if cfg, ok := knownOAuthConfigs[s.Provider]; ok {
		return cfg.ClientID
	}
	return ""
}

// tokenURLFor resolves the token endpoint, preferring the spec and falling back
// to the refresh config so the two never drift.
func (s oauthSpec) tokenURLFor() string {
	if s.TokenURL != "" {
		return s.TokenURL
	}
	if cfg, ok := knownOAuthConfigs[s.Provider]; ok {
		return cfg.TokenURL
	}
	return ""
}

// oauthSpecs is the provider table. Only providers whose dashboard sign-in
// needs an OAuth flow appear here; the rest authenticate with a pasted key.
var oauthSpecs = map[string]oauthSpec{
	// ---- device code ----
	//
	// Only RFC 8628-shaped flows live here. Two providers are deliberately
	// absent because their flows do not fit this shape and a spec they cannot
	// execute would be worse than none:
	//
	//   qoder    — no device-code endpoint at all; the PKCE verifier, nonce and
	//              machine id are generated locally and become the poll identity
	//   kilocode — POST to initiate then GET {pollUrl}/{deviceCode}, with the
	//              state carried in the HTTP status (202/403/410/200)
	//
	// zed is absent for a third reason: it is not OAuth at all (RSA keypair
	// callback, `<user_id> <token>` auth header).
	//
	// All three keep the generic paste-credential form until they get a
	// dedicated handler.

	// Kimi (Moonshot) device flow. RFC 8628 over auth.kimi.com, but the pending
	// state arrives as HTTP 200 with {error:"authorization_pending"} rather than
	// the RFC's 400, so the poll cannot key on the status alone.
	"kimi": {
		Provider:      "kimi",
		Kind:          flowDeviceCode,
		DeviceCodeURL: "https://auth.kimi.com/api/oauth/device_authorization",
		TokenURL:      "https://auth.kimi.com/api/oauth/token",
		PollIsForm:    true,
		PollBody:      map[string]string{"client_id": "17e5f671-d194-4dfb-9706-5516cb48c098"},
	},
	"kimi-coding": {
		Provider:      "kimi",
		Kind:          flowDeviceCode,
		DeviceCodeURL: "https://auth.kimi.com/api/oauth/device_authorization",
		TokenURL:      "https://auth.kimi.com/api/oauth/token",
		PollIsForm:    true,
		PollBody:      map[string]string{"client_id": "17e5f671-d194-4dfb-9706-5516cb48c098"},
	},

	// Grok CLI / Grok Build: device flow on auth.x.ai with the CLI's referrer
	// and UA, then inference on cli-chat-proxy.grok.com. Same client_id as the
	// plain xai provider but wider scopes.
	"grok-cli": {
		Provider:      "grok-cli",
		Kind:          flowDeviceCode,
		DeviceCodeURL: "https://auth.x.ai/oauth2/device/code",
		TokenURL:      "https://auth.x.ai/oauth2/token",
		PollIsForm:    true,
		DeviceCodeBody: map[string]string{
			"scope":    "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write",
			"referrer": "grok-build",
		},
		Headers: map[string]string{
			"User-Agent": "grok-pager/0.2.93 grok-shell/0.2.93 (linux; x86_64)",
		},
	},

	// ---- authorization code ----

	// Claude Code subscription (Pro/Max). PKCE, JSON token exchange, and the
	// code arrives as `<code>#<state>` which must be split before exchange.
	"claude": {
		Provider:     "claude",
		Kind:         flowAuthCode,
		AuthorizeURL: "https://claude.ai/oauth/authorize",
		TokenURL:     "https://api.anthropic.com/v1/oauth/token",
		Scopes:       []string{"org:create_api_key", "user:profile", "user:inference"},
		PKCE:         true,
	},

	// iFlow: plain authorization code with a client secret, plus two fixed
	// query params that select the phone login method.
	"iflow": {
		Provider:     "iflow",
		Kind:         flowAuthCode,
		AuthorizeURL: "https://iflow.cn/oauth",
		TokenURL:     "https://iflow.cn/oauth/token",
		ClientSecret: "4Z3YjXycVsQvyGF1etiNlIBB4RsqSDtW",
		ExtraAuthParams: map[string]string{
			"loginMethod": "phone",
			"type":        "phone",
		},
	},

	// GitLab Duo: PKCE against the user's GitLab host, which may be
	// self-hosted, so the base URL comes from the caller.
	"gitlab": {
		Provider:     "gitlab",
		Kind:         flowAuthCode,
		AuthorizeURL: "https://gitlab.com/oauth/authorize",
		TokenURL:     "https://gitlab.com/oauth/token",
		Scopes:       []string{"api", "read_user"},
		PKCE:         true,
	},

	// xAI: PKCE with a fixed loopback redirect, since the client is registered
	// with that exact URI. Discovery is skipped — the static endpoints are what
	// discovery returns today.
	"xai": {
		Provider:     "xai",
		Kind:         flowAuthCode,
		AuthorizeURL: "https://auth.x.ai/oauth2/authorize",
		TokenURL:     "https://auth.x.ai/oauth2/token",
		Scopes:       []string{"openid", "profile", "email", "offline_access", "grok-cli:access", "api:access"},
		PKCE:         true,
	},

	// ---- import ----

	// Cursor has no OAuth: the IDE stores its token locally and the operator
	// pastes it, together with the machine id the API expects alongside it.
	"cursor": {
		Provider:   "cursor",
		Kind:       flowImport,
		ImportHint: "Paste the access token and machine id from your Cursor IDE (Help → About shows neither; they live in the IDE's local storage).",
	},

	// Codex: the CLI's OAuth lands on a fixed loopback port this server cannot
	// serve, so the practical path is pasting the CLI's access token.
	"codex": {
		Provider:   "codex",
		Kind:       flowImport,
		ImportHint: "Paste the access token from your Codex CLI login (~/.codex/auth.json).",
	},
}

// specFor resolves a provider id or alias to its spec.
func specFor(provider string) (oauthSpec, bool) {
	s, ok := oauthSpecs[provider]
	return s, ok
}

// knownOAuthConfigs is an alias for providers.KnownOAuthConfigs, kept local so
// the spec table reads without importing the providers package at every use.
var knownOAuthConfigs = providersOAuthConfigs()

// HandleGenericFlowInfo reports which flow kind a provider uses, without
// starting one.
//
// GET /api/oauth/{provider}/flow
//
// The dashboard needs this to choose between a sign-in button and a paste form.
// Probing /authorize instead would mint a device code (or open a consent URL)
// just to learn the kind, and a device code is a one-shot resource.
func (h *OAuthHandler) HandleGenericFlowInfo(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	spec, ok := specFor(provider)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "no OAuth flow for provider: "+provider)
		return
	}
	resp := map[string]any{
		"provider": spec.Provider,
		"kind":     string(spec.Kind),
	}
	if spec.ImportHint != "" {
		resp["hint"] = spec.ImportHint
	}
	// A provider whose flow needs an operator-registered OAuth application has
	// no usable built-in client id, so the panel must ask for one before the
	// Sign-in button can work.
	if spec.Kind == flowAuthCode && spec.clientID() == "" {
		resp["needsApp"] = true
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// HandleGenericAuthorize starts a flow and returns what the UI needs.
//
// GET /api/oauth/{provider}/authorize?redirectUri=...
//
// Device-code providers get a device code; authorization-code providers get a
// consent URL. Import providers have no authorize step and are rejected.
func (h *OAuthHandler) HandleGenericAuthorize(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	spec, ok := specFor(provider)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "no OAuth flow for provider: "+provider)
		return
	}

	switch spec.Kind {
	case flowDeviceCode:
		h.startDeviceCode(w, r, spec)
	case flowAuthCode:
		h.startAuthCode(w, r, spec)
	default:
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			"provider "+provider+" does not use a redirect flow; paste its credential instead")
	}
}

// HandleGenericExchange completes a flow.
//
// POST /api/oauth/{provider}/exchange
//
// Device-code providers poll with {"deviceCode"}; authorization-code providers
// send {"code"|"callbackUrl","codeVerifier","redirectUri"}; import providers
// send {"accessToken","machineId"}.
func (h *OAuthHandler) HandleGenericExchange(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	spec, ok := specFor(provider)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "no OAuth flow for provider: "+provider)
		return
	}

	switch spec.Kind {
	case flowDeviceCode:
		h.pollDeviceCode(w, r, spec)
	case flowAuthCode:
		h.exchangeAuthCode(w, r, spec)
	case flowImport:
		h.importToken(w, r, spec)
	}
}

// ---- device code ----

// startDeviceCode requests a device code and returns the verification URL.
func (h *OAuthHandler) startDeviceCode(w http.ResponseWriter, r *http.Request, spec oauthSpec) {
	clientID := spec.clientID()
	if clientID == "" {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, spec.Provider+" OAuth client not configured")
		return
	}

	form := url.Values{"client_id": {clientID}}
	for k, v := range spec.DeviceCodeBody {
		form.Set(k, v)
	}

	req, err := http.NewRequest(http.MethodPost, spec.DeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	for k, v := range spec.Headers {
		req.Header.Set(k, v)
	}

	raw, status, err := doRequest(req)
	if err != nil {
		log.Warn("oauth", "device code request failed", "provider", spec.Provider, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "could not start sign-in")
		return
	}
	if status != http.StatusOK {
		handlerutil.WriteJSONError(w, http.StatusBadGateway,
			fmt.Sprintf("%s device code request failed (HTTP %d): %s", spec.Provider, status, truncateForOAuth(string(raw), 200)))
		return
	}

	var parsed struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid device code response")
		return
	}
	if parsed.DeviceCode == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, spec.Provider+" returned no device code")
		return
	}

	authURL := parsed.VerificationURIComplete
	if authURL == "" {
		authURL = parsed.VerificationURI
	}
	interval := parsed.Interval
	if interval <= 0 {
		interval = 5
	}
	expiresIn := parsed.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 300
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":    spec.Provider,
		"deviceCode":  parsed.DeviceCode,
		"userCode":    parsed.UserCode,
		"authUrl":     authURL,
		"interval":    interval,
		"expiresIn":   expiresIn,
		"pollIntervalMs": interval * 1000,
	})
}

// pollDeviceCode performs one poll, returning 200 + {"pending":true} while the
// operator has not approved.
func (h *OAuthHandler) pollDeviceCode(w http.ResponseWriter, r *http.Request, spec oauthSpec) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var req struct {
		DeviceCode string `json:"deviceCode"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.DeviceCode == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing deviceCode")
		return
	}

	clientID := spec.clientID()
	var payload string
	contentType := "application/json"
	if spec.PollIsForm {
		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {req.DeviceCode},
			"client_id":   {clientID},
		}
		for k, v := range spec.PollBody {
			form.Set(k, v)
		}
		payload = form.Encode()
		contentType = "application/x-www-form-urlencoded"
	} else {
		b, _ := json.Marshal(map[string]any{"deviceCode": req.DeviceCode, "clientId": clientID})
		payload = string(b)
	}

	pollReq, err := http.NewRequest(http.MethodPost, spec.tokenURLFor(), strings.NewReader(payload))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	pollReq.Header.Set("Content-Type", contentType)
	pollReq.Header.Set("Accept", "application/json")
	for k, v := range spec.Headers {
		pollReq.Header.Set(k, v)
	}

	raw, status, err := doRequest(pollReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "poll failed")
		return
	}

	// Providers signal "still waiting" in three different ways: a JSON error
	// field, a status string, or the HTTP status itself. All three are checked
	// so one shape does not need a per-provider handler.
	var probe struct {
		Error       string `json:"error"`
		Status      string `json:"status"`
		AccessToken string `json:"access_token"`
		// Kiro returns camelCase.
		AccessTokenCamel string `json:"accessToken"`
	}
	_ = json.Unmarshal(raw, &probe)

	progress := probe.Error
	if progress == "" {
		progress = probe.Status
	}
	if isPendingProgress(progress) || status == http.StatusAccepted {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"pending": true,
			"error":   progress,
		})
		return
	}

	token := probe.AccessToken
	if token == "" {
		token = probe.AccessTokenCamel
	}
	if token == "" {
		msg := probe.Error
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d: %s", status, truncateForOAuth(string(raw), 200))
		}
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "authorization failed: "+msg)
		return
	}

	h.saveGenericOAuthConnection(w, spec, raw, token, req.Name)
}

// isPendingProgress reports whether an upstream string means "keep polling".
func isPendingProgress(s string) bool {
	switch s {
	case "authorization_pending", "slow_down", "pending":
		return true
	}
	return false
}

// ---- authorization code ----

// startAuthCode builds the consent URL.
func (h *OAuthHandler) startAuthCode(w http.ResponseWriter, r *http.Request, spec oauthSpec) {
	// GitLab (and any self-hosted deployment) requires the operator to register
	// their own OAuth application, so the client id and secret arrive with the
	// request rather than from a built-in config.
	clientID := strings.TrimSpace(r.URL.Query().Get("clientId"))
	if clientID == "" {
		clientID = spec.clientID()
	}
	if clientID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			spec.Provider+" needs an OAuth application: pass clientId (and clientSecret) from the app you registered with the provider")
		return
	}

	// GitLab and other self-hosted deployments pass their own base URL, which
	// rewrites both endpoints.
	authorizeURL := spec.AuthorizeURL
	tokenURL := spec.tokenURLFor()
	if base := strings.TrimSpace(r.URL.Query().Get("baseUrl")); base != "" {
		base = strings.TrimRight(base, "/")
		authorizeURL = base + "/oauth/authorize"
		tokenURL = base + "/oauth/token"
	}

	redirectURI := strings.TrimSpace(r.URL.Query().Get("redirectUri"))
	if redirectURI == "" {
		redirectURI = "http://localhost:8080/callback"
	}

	state := randomString(32)
	q := url.Values{
		"client_id":     {clientID},
		"response_type": {"code"},
		"redirect_uri":  {redirectURI},
		"state":         {state},
	}
	if len(spec.Scopes) > 0 {
		q.Set("scope", strings.Join(spec.Scopes, " "))
	}
	for k, v := range spec.ExtraAuthParams {
		q.Set(k, v)
	}

	verifier := ""
	if spec.PKCE {
		verifier = randomString(64)
		q.Set("code_challenge", sha256Base64(verifier))
		q.Set("code_challenge_method", "S256")
	}

	storeGenericFlow(state, genericFlow{
		provider:    spec.Provider,
		verifier:    verifier,
		redirectURI: redirectURI,
		createdAt:   time.Now(),
	})

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":     spec.Provider,
		"authUrl":      authorizeURL + "?" + q.Encode(),
		"state":        state,
		"codeVerifier": verifier,
		"redirectUri":  redirectURI,
		"tokenUrl":     tokenURL,
	})
}

// exchangeAuthCode swaps the returned code for tokens.
func (h *OAuthHandler) exchangeAuthCode(w http.ResponseWriter, r *http.Request, spec oauthSpec) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var req struct {
		Code         string `json:"code"`
		CallbackURL  string `json:"callbackUrl"`
		State        string `json:"state"`
		CodeVerifier string `json:"codeVerifier"`
		RedirectURI  string `json:"redirectUri"`
		BaseURL      string `json:"baseUrl"`
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		Name         string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	code := strings.TrimSpace(req.Code)
	// Claude returns `<code>#<state>`; the state half is not part of the code.
	stateFromCode := ""
	if before, after, found := strings.Cut(code, "#"); found {
		code, stateFromCode = before, after
	}
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
	if req.State == "" {
		req.State = stateFromCode
	}

	// Recover the PKCE verifier and redirect URI from the pending flow.
	verifier := req.CodeVerifier
	redirectURI := strings.TrimSpace(req.RedirectURI)
	if req.State != "" {
		if flow, ok := takeGenericFlow(req.State); ok {
			if verifier == "" {
				verifier = flow.verifier
			}
			if redirectURI == "" {
				redirectURI = flow.redirectURI
			}
		}
	}
	if redirectURI == "" {
		redirectURI = "http://localhost:8080/callback"
	}
	if spec.PKCE && verifier == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing codeVerifier (unknown or expired state)")
		return
	}

	tokenURL := spec.tokenURLFor()
	if base := strings.TrimSpace(req.BaseURL); base != "" {
		tokenURL = strings.TrimRight(base, "/") + "/oauth/token"
	}

	// The client id/secret from the request win: a self-hosted deployment
	// registers its own OAuth application, so what the operator supplied is the
	// only correct pair.
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		clientID = spec.clientID()
	}
	clientSecret := strings.TrimSpace(req.ClientSecret)
	if clientSecret == "" {
		clientSecret = spec.ClientSecret
	}
	if clientID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing clientId")
		return
	}
	var payload string
	contentType := "application/x-www-form-urlencoded"
	if spec.Provider == "claude" {
		// Anthropic's token endpoint takes JSON, not form encoding.
		b, _ := json.Marshal(map[string]any{
			"grant_type":    "authorization_code",
			"code":          code,
			"state":         req.State,
			"client_id":     clientID,
			"redirect_uri":  redirectURI,
			"code_verifier": verifier,
		})
		payload = string(b)
		contentType = "application/json"
	} else {
		form := url.Values{
			"grant_type":   {"authorization_code"},
			"code":         {code},
			"redirect_uri": {redirectURI},
			"client_id":    {clientID},
		}
		if clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
		if verifier != "" {
			form.Set("code_verifier", verifier)
		}
		payload = form.Encode()
	}

	tokenReq, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(payload))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	tokenReq.Header.Set("Content-Type", contentType)
	tokenReq.Header.Set("Accept", "application/json")
	for k, v := range spec.Headers {
		tokenReq.Header.Set(k, v)
	}

	raw, status, err := doRequest(tokenReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "token exchange failed")
		return
	}
	if status != http.StatusOK {
		handlerutil.WriteJSONError(w, http.StatusBadGateway,
			fmt.Sprintf("token exchange failed (HTTP %d): %s", status, truncateForOAuth(string(raw), 200)))
		return
	}

	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &tokens); err != nil || tokens.AccessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "token exchange returned no access token")
		return
	}

	h.saveGenericOAuthConnection(w, spec, raw, tokens.AccessToken, req.Name)
}

// ---- import ----

// importToken saves a credential the operator pasted from a CLI.
func (h *OAuthHandler) importToken(w http.ResponseWriter, r *http.Request, spec oauthSpec) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var req struct {
		AccessToken  string `json:"accessToken"`
		APIKey       string `json:"apiKey"`
		RefreshToken string `json:"refreshToken"`
		MachineID    string `json:"machineId"`
		Name         string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	token := req.AccessToken
	if token == "" {
		token = req.APIKey
	}
	if token == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing accessToken")
		return
	}

	// Cursor's API needs a machine id alongside the token; without it the
	// connection cannot authenticate, so reject rather than save something
	// broken.
	if spec.Provider == "cursor" && strings.TrimSpace(req.MachineID) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing machineId (required by Cursor)")
		return
	}

	data := map[string]any{"accessToken": token}
	if req.RefreshToken != "" {
		data["refreshToken"] = req.RefreshToken
	}
	if req.MachineID != "" {
		data["providerSpecificData"] = map[string]any{"machineId": req.MachineID}
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode connection data")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = spec.Provider + " import"
	}

	connID, err := h.insertOAuthConnection(spec.Provider, name, "", string(encoded))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"connection": map[string]any{"id": connID, "provider": spec.Provider},
	})
}

// ---- shared ----

// saveGenericOAuthConnection persists a connection from a token response,
// preserving every field the response carried so provider-specific data (Kimi's
// refresh expiry, Kiro's profileArn) survives.
func (h *OAuthHandler) saveGenericOAuthConnection(w http.ResponseWriter, spec oauthSpec, rawTokenResponse []byte, accessToken, requestedName string) {
	var tokens map[string]any
	_ = json.Unmarshal(rawTokenResponse, &tokens)

	data := map[string]any{"accessToken": accessToken}
	for _, key := range []string{"refresh_token", "refreshToken", "id_token", "idToken", "scope"} {
		if v, ok := tokens[key]; ok {
			data[key] = v
		}
	}
	// Normalise snake_case to the camelCase the router reads.
	if v, ok := tokens["refresh_token"]; ok {
		data["refreshToken"] = v
	}
	if v, ok := tokens["id_token"]; ok {
		data["idToken"] = v
	}
	if v, ok := tokens["expires_in"]; ok {
		if secs, ok := toInt(v); ok && secs > 0 {
			data["expiresAt"] = time.Now().Add(time.Duration(secs) * time.Second).UTC().Format(time.RFC3339)
			data["expiresIn"] = secs
		}
	}
	if v, ok := tokens["expiresIn"]; ok {
		if secs, ok := toInt(v); ok && secs > 0 {
			data["expiresAt"] = time.Now().Add(time.Duration(secs) * time.Second).UTC().Format(time.RFC3339)
			data["expiresIn"] = secs
		}
	}

	email := jwtEmail(accessToken)
	if idToken, ok := data["idToken"].(string); ok && idToken != "" {
		if e := jwtEmail(idToken); e != "" {
			email = e
		}
	}

	name := strings.TrimSpace(requestedName)
	if name == "" {
		name = email
	}
	if name == "" {
		name = spec.Provider + " account"
	}

	encoded, err := json.Marshal(data)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode connection data")
		return
	}

	connID, err := h.insertOAuthConnection(spec.Provider, name, email, string(encoded))
	if err != nil {
		log.Error("oauth", "save generic connection failed", "provider", spec.Provider, "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}

	resp := map[string]any{
		"success":    true,
		"connection": map[string]any{"id": connID, "provider": spec.Provider, "email": email},
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// insertOAuthConnection writes one provider connection row and returns its id.
func (h *OAuthHandler) insertOAuthConnection(provider, name, email, data string) (string, error) {
	connID := provider + "-oauth-" + randomString(12)
	priority, perr := h.Repo.NextConnectionPriority(provider)
	if perr != nil {
		priority = 1
	}
	var emailArg any
	if email != "" {
		emailArg = email
	}
	now := currentTimestamp()
	if _, err := h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'oauth', ?, ?, ?, 1, ?, ?, ?)`,
		connID, provider, name, emailArg, priority, data, now, now,
	); err != nil {
		return "", err
	}
	return connID, nil
}

// doRequest performs a request with a bounded timeout and reads the body.
func doRequest(req *http.Request) ([]byte, int, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// toInt coerces a decoded JSON number to int.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}
