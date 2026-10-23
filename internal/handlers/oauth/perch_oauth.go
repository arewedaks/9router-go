package oauth

// Perch OAuth (Supabase Auth, loopback paste-back).
//
// Perch does not run its own OAuth server: the CLI asks Perch for the identity
// provider configuration and then hands the browser to Supabase. Measured from
// a live `perch login google` (2026-10-23):
//
//	GET https://app.perchai.app/api/perch-terminal/cli-auth/config
//	→ {"appUrl":"https://app.perchai.app",
//	   "supabaseUrl":"https://<project>.supabase.co",
//	   "supabaseAnonKey":"sb_publishable_…",
//	   "providers":["google","github"],
//	   "redirectHost":"127.0.0.1"}
//
// So the handshake is Supabase's authorization-code flow with PKCE:
//
//  1. GET {supabaseUrl}/auth/v1/authorize?provider=google&redirect_to=<loopback>
//     &code_challenge=…&code_challenge_method=s256
//  2. The user signs in; Supabase bounces to the loopback with ?code=…
//  3. POST {supabaseUrl}/auth/v1/token?grant_type=pkce with the code and the
//     verifier, apikey: <anon key>
//  4. The returned access_token is the JWT the model-call endpoint accepts.
//
// Nothing here is Perch-private: Supabase publishes the endpoints, the anon key
// is a public client credential (the CLI ships it), and the flow is standard
// PKCE. The config is fetched live rather than hardcoded so a key rotation or a
// project move does not strand the operator.

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
)

const (
	perchAppURL     = "https://app.perchai.app"
	perchAuthConfig = perchAppURL + "/api/perch-terminal/cli-auth/config"

	// perchLoopback is the redirect handed to Supabase. The dashboard does not
	// have to be listening there: the operator copies the address bar back, the
	// same paste-back shape used for Antigravity and Cline.
	perchLoopback = "http://127.0.0.1:20128/oauth/perch/callback"
)

// perchAuthConfigPayload mirrors the fields the CLI reads.
type perchAuthConfigPayload struct {
	OK              bool     `json:"ok"`
	AppURL          string   `json:"appUrl"`
	SupabaseURL     string   `json:"supabaseUrl"`
	SupabaseAnonKey string   `json:"supabaseAnonKey"`
	Providers       []string `json:"providers"`
}

// fetchPerchAuthConfig reads the live Supabase coordinates. Done per login rather
// than cached: the anon key is a rotating public credential and the project URL
// moved at least once during this integration.
func fetchPerchAuthConfig(ctx context.Context, client *http.Client) (*perchAuthConfigPayload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, perchAuthConfig, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "perchai-cli/unknown")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perch: fetch auth config: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("perch: auth config returned HTTP %d", resp.StatusCode)
	}
	var cfg perchAuthConfigPayload
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("perch: decode auth config: %w", err)
	}
	if cfg.SupabaseURL == "" || cfg.SupabaseAnonKey == "" {
		return nil, fmt.Errorf("perch: auth config missing supabase coordinates")
	}
	return &cfg, nil
}

// pkcePair returns a verifier and its S256 challenge.
func pkcePair() (string, string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// HandlePerchAuthorize returns the Supabase authorize URL the operator opens.
//
// Stateless: the verifier and state travel back with the caller (the dashboard
// keeps them for the exchange step), so no server-side session is needed and the
// flow still works when the dashboard is behind a tunnel.
func (h *OAuthHandler) HandlePerchAuthorize(w http.ResponseWriter, r *http.Request) {
	providerID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	if providerID == "" {
		providerID = "google"
	}
	if providerID != "google" && providerID != "github" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "provider must be google or github")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	cfg, err := fetchPerchAuthConfig(ctx, perchHTTPClient())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	verifier, challenge, err := pkcePair()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "could not generate PKCE pair")
		return
	}
	var st [16]byte
	_, _ = rand.Read(st[:])
	state := base64.RawURLEncoding.EncodeToString(st[:])

	q := url.Values{}
	q.Set("provider", providerID)
	q.Set("redirect_to", perchLoopback)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "s256")
	q.Set("state", state)
	authorizeURL := strings.TrimRight(cfg.SupabaseURL, "/") + "/auth/v1/authorize?" + q.Encode()

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"authorizeUrl": authorizeURL,
		"redirectUri":  perchLoopback,
		"provider":     providerID,
		// The dashboard round-trips these into /exchange.
		"codeVerifier": verifier,
		"state":        state,
		"supabaseUrl":  cfg.SupabaseURL,
		"hint": "Open authorizeUrl, sign in, then paste the full address the browser " +
			"was redirected to (or just its ?code=…) back into the exchange form.",
	})
}

// HandlePerchExchange trades the callback code for a Supabase session and stores
// the connection.
func (h *OAuthHandler) HandlePerchExchange(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "could not read body")
		return
	}
	var req struct {
		// RedirectURL is what the operator pasted: a full URL or a bare code.
		RedirectURL  string `json:"redirectUrl"`
		Code         string `json:"code"`
		CodeVerifier string `json:"codeVerifier"`
		SupabaseURL  string `json:"supabaseUrl"`
		Name         string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	code := strings.TrimSpace(req.Code)
	if code == "" {
		code = extractPerchCode(req.RedirectURL)
	}
	if code == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			"paste the full redirect URL (it carries ?code=…) or the code itself")
		return
	}
	if req.CodeVerifier == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "codeVerifier is required: start again from /authorize")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	supabase := strings.TrimSpace(req.SupabaseURL)
	anonKey := ""
	if supabase == "" {
		cfg, err := fetchPerchAuthConfig(ctx, perchHTTPClient())
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		supabase, anonKey = cfg.SupabaseURL, cfg.SupabaseAnonKey
	} else {
		// A caller-supplied project URL still needs the matching anon key.
		cfg, err := fetchPerchAuthConfig(ctx, perchHTTPClient())
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		anonKey = cfg.SupabaseAnonKey
	}

	tokenURL := strings.TrimRight(supabase, "/") + "/auth/v1/token?grant_type=pkce"
	payload, _ := json.Marshal(map[string]string{
		"auth_code":     code,
		"code_verifier": req.CodeVerifier,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(payload))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("apikey", anonKey)
	httpReq.Header.Set("Authorization", "Bearer "+anonKey)

	resp, err := perchHTTPClient().Do(httpReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "perch: token exchange failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode != http.StatusOK {
		handlerutil.WriteJSONError(w, http.StatusBadGateway,
			fmt.Sprintf("perch: supabase returned HTTP %d: %s", resp.StatusCode, truncateForMsg(string(respBody))))
		return
	}

	var session struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		User         struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(respBody, &session); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "perch: decode session: "+err.Error())
		return
	}
	if session.AccessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "perch: session carried no access token")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		if session.User.Email != "" {
			name = session.User.Email
		} else {
			name = "Perch account"
		}
	}

	data := map[string]any{
		"accessToken":  session.AccessToken,
		"refreshToken": session.RefreshToken,
		"email":        session.User.Email,
		"expiresIn":    session.ExpiresIn,
	}
	if session.ExpiresIn > 0 {
		data["expiresAt"] = time.Now().UTC().Add(time.Duration(session.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	// The account/workspace ids are what Perch's gate reads to decide a request
	// came from its own client. They are captured once here because the model
	// call needs them on every turn, and looking them up per request would add a
	// round trip to every turn. Best effort: a failure leaves the lane working
	// but un-attributed, which is what it was before this field existed.
	if uid, wid := fetchPerchAttributionIDs(ctx, session.AccessToken); uid != "" && wid != "" {
		data["perchUserId"] = uid
		data["perchWorkspaceId"] = wid
	}
	raw, err := json.Marshal(data)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "encode connection data")
		return
	}
	connID := "perch-oauth-" + shortID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, 'perch', 'oauth', ?, 1, ?, ?, ?)`,
		connID, name, string(raw), now, now,
	); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "store connection: "+err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"connection": connID,
		"email":      session.User.Email,
	})
}

// fetchPerchAttributionIDs reads the account and workspace ids the CLI attaches
// to every model call. They come from the account endpoint the CLI itself uses:
//
//	GET https://app.perchai.app/api/perchai/account
//	→ {"profile":{"id":…}, "session":{"workspaceId":…}}
//
// Returning empty strings is not an error: the connection still works without
// attribution, it just looks less like the official client.
func fetchPerchAttributionIDs(ctx context.Context, accessToken string) (userID, workspaceID string) {
	if strings.TrimSpace(accessToken) == "" {
		return "", ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, perchAppURL+"/api/perchai/account", nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "perchai-cli/unknown")
	resp, err := perchHTTPClient().Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var acct struct {
		Profile struct {
			ID string `json:"id"`
		} `json:"profile"`
		Session struct {
			UserID      string `json:"userId"`
			WorkspaceID string `json:"workspaceId"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &acct); err != nil {
		return "", ""
	}
	uid := acct.Session.UserID
	if uid == "" {
		uid = acct.Profile.ID
	}
	return uid, acct.Session.WorkspaceID
}

// extractPerchCode accepts either a bare code or the whole redirect URL and
// returns the `code` parameter. Operators paste the address bar, which also
// carries state and sometimes extra Supabase parameters.
func extractPerchCode(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil && u.RawQuery != "" {
		if c := u.Query().Get("code"); c != "" {
			return c
		}
	}
	if strings.HasPrefix(s, "code=") {
		return strings.TrimPrefix(s, "code=")
	}
	return s
}


func shortID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func truncateForMsg(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// perchHTTPClient is a plain client with a deadline: every call in this flow is
// one small JSON round-trip against Supabase or the Perch config endpoint.
func perchHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}
