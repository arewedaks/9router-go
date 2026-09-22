package oauth

// Kilo Code device flow.
//
// Kilo Code does not use RFC 8628. Its flow is:
//
//  1. POST {initiateUrl} with no body → { code, verificationUrl, expiresIn }
//  2. GET  {pollUrlBase}/{code} → the state is carried in the HTTP status:
//       202 = still waiting, 403 = denied, 410 = expired,
//       200 = { status: "approved", token, userEmail }
//  3. GET  {apiBaseUrl}/api/profile with the token → the org id, which the chat
//     path sends as X-Kilocode-OrganizationID.
//
// The spec table's device-code shape cannot express a status-code poll or a
// body-less initiate, so this lives in its own handler rather than forcing the
// generic one to grow two special cases. Ported from VansRouter's
// src/lib/oauth/providers.js kilocode entry and open-sse/providers/registry/kilocode.js.

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

const (
	kilocodeInitiateURL = "https://api.kilo.ai/api/device-auth/codes"
	kilocodePollURLBase = "https://api.kilo.ai/api/device-auth/codes"
	kilocodeAPIBaseURL  = "https://api.kilo.ai"
)

// HandleKilocodeAuthorize starts the Kilo Code device flow.
// GET /api/oauth/kilocode/authorize
func (h *OAuthHandler) HandleKilocodeAuthorize(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequest(http.MethodPost, kilocodeInitiateURL, nil)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	raw, status, err := doRequest(req)
	if err != nil {
		log.Warn("oauth", "kilocode device init failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "could not start Kilo Code sign-in")
		return
	}
	if status == http.StatusTooManyRequests {
		handlerutil.WriteJSONError(w, http.StatusTooManyRequests,
			"Too many pending Kilo Code authorization requests. Please try again later.")
		return
	}
	if status != http.StatusOK {
		handlerutil.WriteJSONError(w, http.StatusBadGateway,
			fmt.Sprintf("Kilo Code device auth failed (HTTP %d): %s", status, truncateForOAuth(string(raw), 200)))
		return
	}

	var parsed struct {
		Code            string `json:"code"`
		VerificationURL string `json:"verificationUrl"`
		ExpiresIn       int    `json:"expiresIn"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid device auth response")
		return
	}
	if parsed.Code == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "Kilo Code returned no device code")
		return
	}

	expiresIn := parsed.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 300
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":   "kilocode",
		"deviceCode": parsed.Code,
		"userCode":   parsed.Code,
		"authUrl":    parsed.VerificationURL,
		"expiresIn":  expiresIn,
		// The upstream is polled every 3s (VansRouter's interval for this flow).
		"interval":       3,
		"pollIntervalMs": 3000,
	})
}

// HandleKilocodeExchange polls once for the token.
// POST /api/oauth/kilocode/exchange  {"deviceCode":"..."}
func (h *OAuthHandler) HandleKilocodeExchange(w http.ResponseWriter, r *http.Request) {
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

	pollURL := kilocodePollURLBase + "/" + req.DeviceCode
	raw, status, err := doRequest(mustRequest(http.MethodGet, pollURL, nil))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "poll failed")
		return
	}

	// The status code IS the state here; there is no error field to read.
	switch status {
	case http.StatusAccepted:
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false, "pending": true, "error": "authorization_pending",
		})
		return
	case http.StatusForbidden:
		handlerutil.WriteJSONError(w, http.StatusForbidden, "authorization denied by user")
		return
	case http.StatusGone:
		handlerutil.WriteJSONError(w, http.StatusGone, "authorization code expired")
		return
	}
	if status != http.StatusOK {
		handlerutil.WriteJSONError(w, http.StatusBadGateway,
			fmt.Sprintf("poll failed (HTTP %d): %s", status, truncateForOAuth(string(raw), 200)))
		return
	}

	var data struct {
		Status    string `json:"status"`
		Token     string `json:"token"`
		UserEmail string `json:"userEmail"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid poll response")
		return
	}
	if data.Status != "approved" || data.Token == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false, "pending": true, "error": "authorization_pending",
		})
		return
	}

	// The org id rides on every chat request, so it is fetched now rather than
	// lazily; a failure here is not fatal, it only costs that header.
	orgID := fetchKilocodeOrgID(data.Token)

	connData := map[string]any{"accessToken": data.Token}
	if orgID != "" {
		connData["providerSpecificData"] = map[string]any{"orgId": orgID}
	}
	encoded, err := json.Marshal(connData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode connection data")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = data.UserEmail
	}
	if name == "" {
		name = "Kilo Code account"
	}

	connID, err := h.insertOAuthConnection("kilocode", name, data.UserEmail, string(encoded))
	if err != nil {
		log.Error("oauth", "save kilocode connection failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id": connID, "provider": "kilocode", "email": data.UserEmail,
		},
	})
}

// fetchKilocodeOrgID reads the account's first organization id. Best-effort:
// the header is an optimisation for the upstream, not a credential.
func fetchKilocodeOrgID(token string) string {
	req, err := http.NewRequest(http.MethodGet, kilocodeAPIBaseURL+"/api/profile", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)

	raw, status, err := doRequest(req)
	if err != nil || status != http.StatusOK {
		return ""
	}
	var profile struct {
		Organizations []struct {
			ID string `json:"id"`
		} `json:"organizations"`
	}
	if err := json.Unmarshal(raw, &profile); err != nil || len(profile.Organizations) == 0 {
		return ""
	}
	return profile.Organizations[0].ID
}

// mustRequest builds a request, returning a request that fails on send if the
// inputs were invalid. Only used where the URL is a constant.
func mustRequest(method, url string, body io.Reader) *http.Request {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		// An unbuildable request is a programming error; return one that will
		// surface as a send error rather than panicking mid-request.
		bad, _ := http.NewRequest(method, "http://invalid.invalid/", nil)
		return bad
	}
	return req
}
