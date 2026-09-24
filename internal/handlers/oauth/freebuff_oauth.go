package oauth

// Freebuff / Codebuff CLI login (fingerprint device flow — NOT OAuth2).
//
// Ported from VansRouter's src/lib/oauth/providers/freebuff.js. The official
// Codebuff CLI authenticates with a browser handshake rather than a redirect:
//
//  1. POST {loginHost}/api/auth/cli/code {fingerprintId}
//     → { fingerprintId, fingerprintHash, loginUrl, expiresAt }
//  2. Operator opens loginUrl and signs in.
//  3. GET {loginHost}/api/auth/cli/status?fingerprintId=..&fingerprintHash=..&expiresAt=..
//     → { user: { id, email, name, authToken } } once authorised.
//
// The resulting user.authToken is the Bearer credential for the OpenAI-
// compatible endpoint on the SAME host.
//
// The host is www.codebuff.com, and getting it wrong is silent: freebuff.com is
// a separate consumer product with its own OAuth application
// (GitHub client_id Ov23liBmPZjygPRVpLQs against Ov23liw1KEfqvhsQV2Mc here) and
// no /api/v1 at all — it answers 404 where this one answers 401 "Invalid
// Codebuff API key". Its /api/auth/cli/code still returns 200 with a loginUrl,
// so a flow pointed there looks healthy, sends the operator to a real sign-in
// page, and then polls forever: the auth_code belongs to an account on the
// other service, so this host's status RPC never binds it. Verified against the
// released CLI (codebuff 1.0.688), whose base URL is
// https://www.codebuff.com — the reverse of what this file used to claim.
//
// The handshake is stateless server-side: the fingerprint triple rides in the
// dashboard's poll request, so this works headless.

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

const (
	// freebuffLoginHost is the login-flow host, matching the released CLI's base
	// URL. The server builds loginUrl from the host it was called on, so a wrong
	// host yields a real sign-in page for the WRONG service and a poll that never
	// resolves.
	freebuffLoginHost = "https://www.codebuff.com"

	freebuffLoginCodePath   = "/api/auth/cli/code"
	freebuffLoginStatusPath = "/api/auth/cli/status"

	// freebuffUserAgent names the client on handshake calls. The released CLI
	// sends no User-Agent here and the backend does not validate one, so this is
	// only for intermediaries that reject a missing header. Kept at the released
	// version rather than an invented one.
	freebuffUserAgent = "codebuff-cli/1.0.688"

	// Mirrors the CLI's 5-minute polling deadline (oauthTimeoutMs).
	freebuffLoginTimeout = 5 * time.Minute
)

// Overridable so tests can point the handshake at an httptest server.
var freebuffLoginBase = freebuffLoginHost

// HandleFreebuffAuthorize starts the Freebuff device login.
// GET /api/oauth/freebuff/authorize
//
// Returns { authUrl, fingerprintId, fingerprintHash, expiresAt } — the
// dashboard opens authUrl and polls /exchange with the same triple.
func (h *OAuthHandler) HandleFreebuffAuthorize(w http.ResponseWriter, r *http.Request) {
	// The released CLI names its fingerprint "codebuff-cli-<8>" (or an
	// "enhanced-<sha256>" derived from machine ids). The backend accepted a bare
	// random string too, but sending the shape the client actually sends avoids
	// depending on that leniency.
	fingerprintID := "codebuff-cli-" + randomString(8)

	body, err := json.Marshal(map[string]any{"fingerprintId": fingerprintID})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}

	req, err := http.NewRequest(http.MethodPost, freebuffLoginBase+freebuffLoginCodePath, strings.NewReader(string(body)))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", freebuffUserAgent)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Warn("oauth", "freebuff login code request failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "could not start Freebuff sign-in")
		return
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read login response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		log.Warn("oauth", "freebuff login code rejected", "status", resp.StatusCode, "body", truncateForOAuth(string(raw), 200))
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "Freebuff login code request failed")
		return
	}

	var parsed struct {
		FingerprintID   string `json:"fingerprintId"`
		FingerprintHash string `json:"fingerprintHash"`
		LoginURL        string `json:"loginUrl"`
		ExpiresAt       int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid login response")
		return
	}
	if parsed.LoginURL == "" || parsed.FingerprintHash == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "Freebuff returned no login URL")
		return
	}

	// The server echoes the requested id back; fall back to ours if it omits it.
	if parsed.FingerprintID == "" {
		parsed.FingerprintID = fingerprintID
	}

	// Pass the server's expiry through unchanged: the status RPC is asked about
	// the code this host issued, and the CLI forwards the value it was given.
	// The 5-minute CLI deadline bounds the POLL LOOP, not this field, so the
	// dashboard gets it separately as pollTimeoutMs.
	expiresAt := parsed.ExpiresAt
	if expiresAt == 0 {
		expiresAt = time.Now().Add(freebuffLoginTimeout).UnixMilli()
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"authUrl":         parsed.LoginURL,
		"fingerprintId":   parsed.FingerprintID,
		"fingerprintHash": parsed.FingerprintHash,
		"expiresAt":       expiresAt,
		"pollIntervalMs":  5000,
		"pollTimeoutMs":   int64(freebuffLoginTimeout / time.Millisecond),
	})
}

// HandleFreebuffExchange polls once for the token belonging to a fingerprint.
// POST /api/oauth/freebuff/exchange
// Body: {"fingerprintId","fingerprintHash","expiresAt","name"}
//
// Returns 200 with {"success":false,"pending":true} while the operator has not
// signed in yet, so the dashboard keeps polling; 200 with the saved connection
// once authorised.
func (h *OAuthHandler) HandleFreebuffExchange(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		FingerprintID   string `json:"fingerprintId"`
		FingerprintHash string `json:"fingerprintHash"`
		ExpiresAt       int64  `json:"expiresAt"`
		Name            string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.FingerprintID == "" || req.FingerprintHash == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing fingerprintId or fingerprintHash")
		return
	}

	q := url.Values{
		"fingerprintId":   {req.FingerprintID},
		"fingerprintHash": {req.FingerprintHash},
	}
	if req.ExpiresAt > 0 {
		q.Set("expiresAt", fmt.Sprintf("%d", req.ExpiresAt))
	}

	statusReq, err := http.NewRequest(http.MethodGet, freebuffLoginBase+freebuffLoginStatusPath+"?"+q.Encode(), nil)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	statusReq.Header.Set("Accept", "application/json")
	statusReq.Header.Set("User-Agent", freebuffUserAgent)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(statusReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "Freebuff status check failed")
		return
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read status response")
		return
	}

	// The status endpoint answers 401 {"error":"Authentication failed"} while the
	// device is still waiting, and 200 { user } once authorised. Mirror the CLI:
	// keep polling on anything that does not carry a user token.
	var parsed struct {
		User *struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			Name        string `json:"name"`
			AuthToken   string `json:"authToken"`
			Fingerprint string `json:"fingerprintId"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.User == nil || parsed.User.AuthToken == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"pending": true,
			"error":   "authorization_pending",
		})
		return
	}

	user := parsed.User
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = user.Email
	}
	if name == "" {
		name = user.Name
	}
	if name == "" {
		name = "Freebuff account"
	}

	connID := "freebuff-oauth-" + randomString(12)
	data := map[string]any{
		"accessToken": user.AuthToken,
		"providerSpecificData": map[string]any{
			"authMethod":    "device_code",
			"fingerprintId": user.Fingerprint,
			"userId":        user.ID,
		},
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode connection data")
		return
	}

	now := currentTimestamp()
	priority, perr := h.Repo.NextConnectionPriority("freebuff")
	if perr != nil {
		priority = 1
	}
	var emailArg any
	if user.Email != "" {
		emailArg = user.Email
	}
	if _, err := h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt) VALUES (?, 'freebuff', 'oauth', ?, ?, ?, 1, ?, ?, ?)`,
		connID, name, emailArg, priority, string(encoded), now, now,
	); err != nil {
		log.Error("oauth", "save freebuff connection failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id":       connID,
			"provider": "freebuff",
			"email":    user.Email,
		},
	})
}
