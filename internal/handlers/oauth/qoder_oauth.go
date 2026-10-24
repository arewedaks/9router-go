package oauth

// Qoder sign-in (device-token flow).
//
// Qoder has no RFC 8628 endpoint: the client mints the identity itself and the
// browser page binds to it. The handshake is
//
//	1. generate PKCE verifier + S256 challenge, a nonce and a machine id
//	2. send the operator to
//	   https://qoder.com/device/selectAccounts?challenge=…&challenge_method=S256&machine_id=…&nonce=…
//	3. poll https://openapi.qoder.sh/api/v1/deviceToken/poll?nonce=…&verifier=…&challenge_method=S256
//	   → 202/404 while the browser flow is unfinished, 200 with {token, …} once approved
//
// The poll identifies the login by the nonce + verifier pair, so the server has
// to remember both between /authorize and /exchange; they never leave this
// process. That is why the flow is not in the generic spec table, which keys
// everything off a client id and a server-issued device code.
//
// Tokens live ~30 days and the refresh endpoint answers 403 for this flow, so
// the connection stores the token as accessToken with no refresh hook and the
// operator signs in again when it expires.
//
// The dashboard's generic OAuth panel drives this: /authorize answers with
// deviceCode (telling the panel it is a device flow), /exchange answers
// {pending:true} until approval, then {success:true}.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/handlerutil"
)

const (
	// qoderAuthorizePage is the approval page the operator is sent to.
	qoderAuthorizePage = "https://qoder.com/device/selectAccounts"
	// qoderPollEndpoint answers 200 with the token once the page is approved.
	qoderPollEndpoint = "https://openapi.qoder.sh/api/v1/deviceToken/poll"
	// qoderUserInfoEndpoint names the fresh account, best effort.
	qoderUserInfoEndpoint = "https://openapi.qoder.sh/api/v1/userinfo"

	// qoderFlowTTL bounds how long a started sign-in stays pollable: the page
	// itself expires after five minutes.
	qoderFlowTTL = 10 * time.Minute
)

// Overridable so tests can point the handshake at an httptest server.
var (
	qoderPollURL     = qoderPollEndpoint
	qoderUserInfoURL = qoderUserInfoEndpoint
)

// qoderPendingFlow is a started sign-in, kept only in memory: it holds the PKCE
// verifier the poll endpoint authenticates with.
type qoderPendingFlow struct {
	verifier  string
	machineID string
	expires   time.Time
}

var (
	qoderFlowsMu sync.Mutex
	qoderFlows   = map[string]qoderPendingFlow{}
)

func rememberQoderFlow(nonce string, f qoderPendingFlow) {
	qoderFlowsMu.Lock()
	defer qoderFlowsMu.Unlock()
	now := time.Now()
	for k, v := range qoderFlows { // drop finished sign-ins on the way in
		if now.After(v.expires) {
			delete(qoderFlows, k)
		}
	}
	qoderFlows[nonce] = f
}

func lookupQoderFlow(nonce string) (qoderPendingFlow, bool) {
	qoderFlowsMu.Lock()
	defer qoderFlowsMu.Unlock()
	f, ok := qoderFlows[nonce]
	if !ok || time.Now().After(f.expires) {
		delete(qoderFlows, nonce)
		return qoderPendingFlow{}, false
	}
	return f, true
}

// forgetQoderFlow drops a completed sign-in so a replayed poll cannot store the
// same account twice.
func forgetQoderFlow(nonce string) {
	qoderFlowsMu.Lock()
	defer qoderFlowsMu.Unlock()
	delete(qoderFlows, nonce)
}

// HandleQoderFlowInfo tells the dashboard which shape the panel takes.
// GET /api/oauth/qoder/flow
func (h *OAuthHandler) HandleQoderFlowInfo(w http.ResponseWriter, r *http.Request) {
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider": "qoder",
		"kind":     "device_code",
		"hint":     "Sign in with your Qoder account — the tab that opens finishes the sign-in on its own.",
	})
}

// HandleQoderAuthorize starts a sign-in and returns the page to open.
// GET /api/oauth/qoder/authorize
func (h *OAuthHandler) HandleQoderAuthorize(w http.ResponseWriter, r *http.Request) {
	verifier, err := pkceVerifier()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "qoder: generate verifier")
		return
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	nonce := uuid.NewString()
	machineID := uuid.NewString()

	rememberQoderFlow(nonce, qoderPendingFlow{
		verifier:  verifier,
		machineID: machineID,
		expires:   time.Now().Add(qoderFlowTTL),
	})

	params := url.Values{
		"challenge":        {challenge},
		"challenge_method": {"S256"},
		"machine_id":       {machineID},
		"nonce":            {nonce},
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"deviceCode": nonce,
		// Qoder's page needs no typed code — the URL carries the whole
		// handshake — so the panel shows only the link.
		"userCode":  "",
		"authUrl":   qoderAuthorizePage + "?" + params.Encode(),
		"interval":  3,
		"expiresIn": int(qoderFlowTTL.Seconds()),
	})
}

// HandleQoderExchange polls the sign-in once.
// POST /api/oauth/qoder/exchange  Body: {"deviceCode":"<nonce>"}
//
// Pending is a 200 with {pending:true} so the panel keeps waiting.
func (h *OAuthHandler) HandleQoderExchange(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	var req struct {
		DeviceCode string `json:"deviceCode"`
	}
	if err != nil || json.Unmarshal(body, &req) != nil || strings.TrimSpace(req.DeviceCode) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing or invalid deviceCode")
		return
	}
	nonce := strings.TrimSpace(req.DeviceCode)

	flow, ok := lookupQoderFlow(nonce)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "qoder: this sign-in expired, start again")
		return
	}

	token, userID, pending, err := pollQoderToken(r, nonce, flow.verifier)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "qoder: "+err.Error())
		return
	}
	if pending {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"pending": true})
		return
	}

	// Both are best effort: a naming failure must not throw away a token the
	// operator just approved.
	name, email := qoderProfile(r, token)
	forgetQoderFlow(nonce)
	if name == "" {
		name = firstNonEmpty(email, "Qoder account")
	}

	data := map[string]any{
		"accessToken": token,
		"providerSpecificData": map[string]any{
			"authMethod": "device",
			"machineId":  flow.machineID,
			"userId":     userID,
		},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "qoder: encode connection data")
		return
	}

	connID := "qoder-oauth-" + shortID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, 'qoder', 'oauth', ?, 1, ?, ?, ?)`,
		connID, name, string(raw), now, now,
	); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "qoder: store connection: "+err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"connection": connID,
		"email":      email,
	})
}

// pollQoderToken asks the poll endpoint once. pending reports "not approved
// yet"; the error text is safe to surface (never the token or the verifier).
func pollQoderToken(r *http.Request, nonce, verifier string) (token, userID string, pending bool, err error) {
	q := url.Values{
		"nonce":            {nonce},
		"verifier":         {verifier},
		"challenge_method": {"S256"},
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, qoderPollURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", "", false, fmt.Errorf("build poll request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// The upstream gates this endpoint on the client it expects to see.
	req.Header.Set("User-Agent", "Go-http-client/2.0")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", "", false, fmt.Errorf("sign-in service unreachable: %w", err)
	}
	defer resp.Body.Close()

	// 202 and 404 both mean "the browser flow has not finished yet".
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNotFound {
		return "", "", true, nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return "", "", false, fmt.Errorf("sign-in service returned HTTP %d: %s", resp.StatusCode, truncateForMsg(string(raw)))
	}

	var reply struct {
		Token  string `json:"token"`
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return "", "", false, fmt.Errorf("decode sign-in response: %w", err)
	}
	if reply.Token == "" {
		return "", "", false, fmt.Errorf("sign-in service returned no token")
	}
	return reply.Token, reply.UserID, false, nil
}

// qoderProfile names the account behind a fresh token. Failures are silent:
// the connection is already usable.
func qoderProfile(r *http.Request, token string) (name, email string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, qoderUserInfoURL, nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Go-http-client/2.0")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var profile struct {
		Name     string `json:"name"`
		Username string `json:"username"`
		Nickname string `json:"nickname"`
		Email    string `json:"email"`
	}
	if json.Unmarshal(body, &profile) != nil {
		return "", ""
	}
	return firstNonEmpty(profile.Name, profile.Nickname, profile.Username), strings.TrimSpace(profile.Email)
}

// pkceVerifier returns a 43-char base64url verifier (32 random bytes), the
// shape Qoder's CLI sends.
func pkceVerifier() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
