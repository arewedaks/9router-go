package oauth

import (
	"io"
	"net/http"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// OmniRush signs in with the device flow its own CLI uses: the operator approves
// this machine in the omnirush.ai console, the gateway hands back a device
// access token plus a rotating refresh token. There is no redirect, so the flow
// works from a remote server.
//
// The three routes are mounted explicitly (see router.go) because the generic
// table cannot express this shape: the authorize body is JSON with
// device_name/platform, the poll is JSON, and a pending poll answers 428 rather
// than a token.
const omniRushProviderID = "omnirush"

// isOmniRushProvider reports whether a request path names OmniRush or its alias.
func isOmniRushProvider(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	return p == omniRushProviderID || p == "omr" || p == "orush"
}

// HandleOmniRushFlow reports the flow kind to the dashboard panel.
//
// GET /api/oauth/omnirush/flow
func (h *OAuthHandler) HandleOmniRushFlow(w http.ResponseWriter, r *http.Request) {
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":  omniRushProviderID,
		"kind":      "device_code",
		"startHint": "OmniRush signs in with a device code: press Sign in, then approve this machine in the omnirush.ai console with the code below.",
	})
}

// HandleOmniRushAuthorize starts the device sign-in.
//
// GET /api/oauth/omnirush/authorize
func (h *OAuthHandler) HandleOmniRushAuthorize(w http.ResponseWriter, r *http.Request) {
	code, err := providers.OmniRushAuthorize(r.Context(), nil, deviceLabel(r), "linux")
	if err != nil {
		log.Warn("oauth", "omnirush authorize failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "could not start the OmniRush sign-in: "+err.Error())
		return
	}
	authURL := code.VerificationURIComplete
	if authURL == "" {
		authURL = code.VerificationURI
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":       omniRushProviderID,
		"deviceCode":     code.DeviceCode,
		"userCode":       code.UserCode,
		"authUrl":        authURL,
		"interval":       code.Interval,
		"expiresIn":      code.ExpiresIn,
		"pollIntervalMs": code.Interval * 1000,
	})
}

// HandleOmniRushExchange polls once; on approval it stores the Firebase-free
// device tokens under both keys the router reads.
//
// POST /api/oauth/omnirush/exchange  {"deviceCode":"…","name":"…"}
func (h *OAuthHandler) HandleOmniRushExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"deviceCode"`
		Name       string `json:"name"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.DeviceCode) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing deviceCode")
		return
	}

	res, err := providers.OmniRushPollToken(r.Context(), nil, req.DeviceCode)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "OmniRush refused the sign-in: "+err.Error())
		return
	}
	if res.Pending || res.SlowDown {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"pending": true,
			"error":   res.Detail,
		})
		return
	}

	data, err := json.Marshal(map[string]any{
		"accessToken":  res.AccessToken,
		"refreshToken": res.RefreshToken,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode connection data")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "OmniRush device"
	}
	connID, err := h.insertOAuthConnection(omniRushProviderID, name, "", string(data))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"connection": map[string]any{"id": connID, "provider": omniRushProviderID},
	})
}

// deviceLabel names this machine in the console's approval screen.
func deviceLabel(r *http.Request) string {
	host := "9router"
	if r != nil {
		if h := r.Host; h != "" {
			host = "9router@" + strings.Split(h, ":")[0]
		}
	}
	return host
}
