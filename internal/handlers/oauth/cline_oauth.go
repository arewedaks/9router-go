package oauth

import (
	"bytes"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
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

// Cline OAuth (WorkOS authorization-code flow via a loopback callback).
//
// Cline does not use a device flow and does not use PKCE. The official client
// runs a plain authorization-code handshake that bounces through Cline's own
// callback and then hands the tokens to whatever `callback_url` it was given:
//
//  1. The operator opens
//     https://api.cline.bot/api/v1/auth/authorize?client_type=extension&callback_url=<loopback>
//     (plus redirect_uri, which the endpoint also expects).
//  2. api.cline.bot answers 302 to WorkOS with
//     redirect_uri=https://api.cline.bot/api/v1/auth/callback — Cline's own
//     callback, NOT ours. WorkOS therefore only ever needs to trust Cline.
//  3. After the user signs in, Cline redirects to the `callback_url` we
//     supplied, carrying the credential. The value arrives as a base64-encoded
//     JSON blob (accessToken / refreshToken / expiresAt / email / …), not as a
//     bare opaque code, so the usual "exchange code at the token endpoint" step
//     is not needed — Cline already did it.
//  4. If that decode fails we fall back to POSTing the raw code to
//     /api/v1/auth/token, which is the documented exchange endpoint.
//
// Everything stays stateless on our side: the browser holds the callback URL and
// passes it back to /exchange, so no callback server has to be reachable. That
// keeps the flow usable from a headless/Termux server, matching the reasoning
// already applied to Antigravity (paste-back) and CodeBuddy (device code).

const (
	clineAuthorizeURL = "https://api.cline.bot/api/v1/auth/authorize"
	clineTokenURL     = "https://api.cline.bot/api/v1/auth/token"

	// clineDefaultRedirectURI is the loopback address handed to Cline as
	// `callback_url`. Cline redirects the browser here after sign-in; the
	// dashboard does not need to be listening because the operator copies the
	// address bar back into the form.
	clineDefaultRedirectURI = "http://localhost:20128/oauth/cline/callback"
)

// clineTokenPayload is the credential set Cline returns, either embedded in the
// callback payload or via the token-exchange fallback.
type clineTokenPayload struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    string
	ExpiresIn    int
	Email        string
	FirstName    string
	LastName     string
}

// HandleClineAuthorize starts the Cline OAuth flow.
// GET /api/oauth/cline/authorize?redirectUri=...
//
// Cline's authorize endpoint is not a standard OAuth2 URL builder: it takes
// client_type/callback_url and then does its own WorkOS redirect, so there is no
// state or PKCE verifier to round-trip. The response hands the UI the URL to
// open plus the redirect URI to expect back.
func (h *OAuthHandler) HandleClineAuthorize(w http.ResponseWriter, r *http.Request) {
	redirectURI := strings.TrimSpace(r.URL.Query().Get("redirectUri"))
	if redirectURI == "" {
		redirectURI = loopbackRedirectURI(r, "/oauth/cline/callback")
	}

	q := url.Values{
		"client_type":  {"extension"},
		"callback_url": {redirectURI},
		"redirect_uri": {redirectURI},
	}
	authURL := clineAuthorizeURL + "?" + q.Encode()

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"authUrl":     authURL,
		"redirectUri": redirectURI,
		// Cline does not round-trip a state value; the field is returned as an
		// empty string so the UI shape stays uniform with the other providers.
		"state": "",
	})
}

// HandleClineExchange completes the Cline OAuth flow.
// POST /api/oauth/cline/exchange
// Body: {"code"|"callbackUrl", "redirectUri", "name"}
//
// The credential may arrive as a base64 JSON blob (the normal Cline callback) or
// as an opaque code to exchange. Both are handled.
func (h *OAuthHandler) HandleClineExchange(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Code        string `json:"code"`
		CallbackURL string `json:"callbackUrl"`
		RedirectURI string `json:"redirectUri"`
		Name        string `json:"name"`
		Replace     bool   `json:"replace"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// The operator usually pastes the whole redirect URL; accept either form.
	code := strings.TrimSpace(req.Code)
	if code == "" && strings.TrimSpace(req.CallbackURL) != "" {
		code = clineCodeFromCallbackURL(req.CallbackURL)
	}
	if code == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code or callbackUrl")
		return
	}

	redirectURI := strings.TrimSpace(req.RedirectURI)
	if redirectURI == "" {
		redirectURI = clineDefaultRedirectURI
	}

	tokens, err := resolveClineTokens(code, redirectURI)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "cline returned no access token")
		return
	}

	name, err := h.saveClineConnection(tokens, req.Name, req.Replace)
	if err != nil {
		var dup *DuplicateError
		if errors.As(err, &dup) {
			DuplicateConnectionErrorToHTTP(w, dup)
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"connection": name,
		"email":      tokens.Email,
	})
}

// clineCodeFromCallbackURL extracts the credential from a pasted callback URL.
// Cline puts it in `code`, but older builds have used `access_token`-style
// parameters, so both spellings are accepted.
func clineCodeFromCallbackURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		// Not an absolute URL — treat the whole input as the raw code. url.Parse
		// accepts a bare word as a relative path, so the scheme/host check is what
		// actually distinguishes "pasted URL" from "pasted code".
		return strings.TrimSpace(raw)
	}
	for _, key := range []string{"code", "token", "accessToken", "access_token"} {
		if v := strings.TrimSpace(u.Query().Get(key)); v != "" {
			return v
		}
	}
	// Some builds deliver the payload in the fragment.
	if u.Fragment != "" {
		if vals, err := url.ParseQuery(u.Fragment); err == nil {
			for _, key := range []string{"code", "token", "accessToken", "access_token"} {
				if v := strings.TrimSpace(vals.Get(key)); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

// resolveClineTokens turns the credential Cline handed us into a token payload.
//
// Cline embeds the tokens as base64-encoded JSON in the callback value, so the
// common path is a local decode with no network call. When that fails (e.g. an
// opaque code from a build that did not embed anything) we fall back to the
// documented token-exchange endpoint.
func resolveClineTokens(code, redirectURI string) (*clineTokenPayload, error) {
	if tokens := decodeClineEmbeddedToken(code); tokens != nil {
		return tokens, nil
	}

	tokens, err := exchangeClineCode(code, redirectURI)
	if err != nil {
		// Surface the decode failure reason too, since a user-facing "invalid or
		// expired authorization code" usually means the pasted URL is stale.
		return nil, fmt.Errorf("%w (the callback URL may be expired; start the sign-in again)", err)
	}
	return tokens, nil
}

// decodeClineEmbeddedToken decodes the base64-JSON credential Cline embeds in
// the callback value. It returns nil when the value is not such a blob, so the
// caller can fall back to the token endpoint.
//
// The decode mirrors the reference implementation, including its handling of
// URL-encoded input, missing base64 padding, and trailing non-JSON bytes.
func decodeClineEmbeddedToken(raw string) *clineTokenPayload {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	// The pasted URL value may still be percent-encoded.
	if decoded, err := url.QueryUnescape(value); err == nil {
		value = decoded
	}
	if value == "" {
		return nil
	}

	// base64 decoding is lenient about padding only if we restore it.
	if pad := len(value) % 4; pad != 0 {
		value += strings.Repeat("=", 4-pad)
	}

	decoded, err := decodeBase64Loose(value)
	if err != nil {
		return nil
	}
	// Cline may append trailing bytes after the JSON object; keep up to the last
	// closing brace, as the reference does.
	if last := bytes.LastIndexByte(decoded, '}'); last >= 0 {
		decoded = decoded[:last+1]
	} else {
		return nil
	}

	var payload struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    string `json:"expiresAt"`
		ExpiresIn    int    `json:"expiresIn"`
		Email        string `json:"email"`
		FirstName    string `json:"firstName"`
		LastName     string `json:"lastName"`
	}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return nil
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil
	}
	return &clineTokenPayload{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    payload.ExpiresAt,
		ExpiresIn:    payload.ExpiresIn,
		Email:        payload.Email,
		FirstName:    payload.FirstName,
		LastName:     payload.LastName,
	}
}

// exchangeClineCode performs the documented token exchange. It is the fallback
// path for credentials that are not an embedded base64 blob.
func exchangeClineCode(code, redirectURI string) (*clineTokenPayload, error) {
	reqBody, err := json.Marshal(map[string]string{
		"grant_type":   "authorization_code",
		"code":         code,
		"client_type":  "extension",
		"redirect_uri": redirectURI,
	})
	if err != nil {
		return nil, fmt.Errorf("encode token request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, clineTokenURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range providers.ClineClientIdentityHeaders() {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cline token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cline token exchange returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	// Cline wraps the payload in {success, data}; accept both shapes.
	var parsed struct {
		Success bool             `json:"success"`
		Error   any              `json:"error"`
		Data    clineTokenFields `json:"data"`
		clineTokenFields
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if !parsed.Success && parsed.Error != nil && fmt.Sprint(parsed.Error) != "" && fmt.Sprint(parsed.Error) != "<nil>" {
		return nil, fmt.Errorf("cline token exchange failed: %v", parsed.Error)
	}

	fields := parsed.Data
	if strings.TrimSpace(fields.AccessToken) == "" {
		fields = parsed.clineTokenFields
	}
	if strings.TrimSpace(fields.AccessToken) == "" {
		return nil, fmt.Errorf("cline token exchange returned no access token")
	}

	email := fields.Email
	if email == "" {
		email = fields.UserInfo.Email
	}
	return &clineTokenPayload{
		AccessToken:  fields.AccessToken,
		RefreshToken: fields.RefreshToken,
		ExpiresAt:    fields.ExpiresAt,
		ExpiresIn:    fields.ExpiresIn,
		Email:        email,
	}, nil
}

// decodeBase64Loose decodes a base64 value that may use either the standard or
// URL-safe alphabet. Cline's payload is standard base64, but a value copied out
// of a URL can arrive URL-safe, and the two differ on `+`/`-` and `/`/`_`.
func decodeBase64Loose(value string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.RawURLEncoding.DecodeString(value)
}

// clineTokenFields is the token shape shared by the embedded blob and the
// exchange response.
type clineTokenFields struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt"`
	ExpiresIn    int    `json:"expiresIn"`
	Email        string `json:"email"`
	FirstName    string `json:"firstName"`
	LastName     string `json:"lastName"`
	UserInfo     struct {
		Email string `json:"email"`
	} `json:"userInfo"`
}

// saveClineConnection persists a Cline OAuth connection.
//
// The access token is stored WITHOUT the `workos:` prefix: the chat path applies
// that prefix on every request via providers.ClineAccessToken, and storing the
// raw token keeps the refresh contract (which sends the bare token) correct.
//
// A re-login of an already-connected account is rejected unless replace=true, in
// which case the existing row's tokens are refreshed in place (ID and priority
// preserved) rather than creating a second row for the same quota.
func (h *OAuthHandler) saveClineConnection(tokens *clineTokenPayload, requestedName string, replace bool) (string, error) {
	email := strings.TrimSpace(tokens.Email)
	name := strings.TrimSpace(requestedName)
	if name == "" {
		if full := strings.TrimSpace(strings.TrimSpace(tokens.FirstName) + " " + strings.TrimSpace(tokens.LastName)); full != "" {
			name = full
		}
	}
	if name == "" {
		name = email
	}
	if name == "" {
		name = "Cline account"
	}

	data := map[string]any{
		"accessToken": tokens.AccessToken,
	}
	if tokens.RefreshToken != "" {
		data["refreshToken"] = tokens.RefreshToken
	}
	// expiresAt drives the on-demand refresh decision in the chat path. Prefer
	// the absolute timestamp Cline returns; fall back to a computed window.
	if strings.TrimSpace(tokens.ExpiresAt) != "" {
		data["expiresAt"] = tokens.ExpiresAt
	}
	if tokens.ExpiresIn > 0 {
		data["expiresIn"] = tokens.ExpiresIn
		if data["expiresAt"] == nil {
			data["expiresAt"] = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		}
	}
	if email != "" {
		data["email"] = email
	}

	// providerSpecificData carries the display-only identity, matching the shape
	// the reference persists so the account row shows a real name.
	psd := map[string]any{"autoSync": true}
	if tokens.FirstName != "" {
		psd["firstName"] = tokens.FirstName
	}
	if tokens.LastName != "" {
		psd["lastName"] = tokens.LastName
	}
	// The email is stored at the top level (see `data["email"]`) because that is
	// the key extractAccountInfo reads for the account label; duplicating it under
	// providerSpecificData would just be a second source of truth.
	data["providerSpecificData"] = psd

	// Reject a re-login of an already-connected account unless the caller asked to
	// replace it: two rows for one account would make the fallback logic exhaust
	// the same quota twice.
	candidate := IdentityForProvider("cline", tokens.AccessToken, email)
	dup, derr := h.FindDuplicateConnection("cline", candidate)
	if derr != nil {
		return "", fmt.Errorf("duplicate check: %w", derr)
	}
	if dup != nil {
		if !replace {
			logDuplicateSuppressed("cline", dup)
			return "", dup
		}
		if err := h.replaceConnectionTokens(dup.ExistingConnID, name, data); err != nil {
			return "", fmt.Errorf("replace connection: %w", err)
		}
		log.Info("oauth", "duplicate account re-login refreshed existing connection",
			"provider", "cline", "conn", dup.ExistingConnID, "identity", dup.Identity.Label())
		return name, nil
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("encode connection data: %w", err)
	}

	connID := "cline-oauth-" + randomString(12)
	now := currentTimestamp()
	if _, err := h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, 'cline', 'oauth', ?, 1, ?, ?, ?)`,
		connID, name, string(raw), now, now,
	); err != nil {
		return "", fmt.Errorf("save connection: %w", err)
	}
	return name, nil
}
