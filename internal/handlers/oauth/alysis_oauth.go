package oauth

// Alysis Code login (RFC 8628 device flow, no localhost listener).
//
// The official CLI (`alysis login`) asks the account backend for a short user
// code, sends the operator to https://alysiscode.com/activate to approve it,
// and polls until the backend hands back the long-lived gateway key (`slk_…`).
// That key is a static bearer, not a refreshable OAuth token, so the connection
// stores it as accessToken and the executor forwards it unchanged.
//
//	POST {api}/device-code   {"client_name","installation_id"}
//	  → {device_code, user_code, verification_url_complete, interval, expires_in}
//	POST {api}/device-token  {"device_code"}
//	  → {status: pending|approved|denied|expired|not_found|already_claimed, key?}
//
// Both calls need no client secret and no prior credential, so the flow works
// from the router with nothing preconfigured.
//
// The dashboard's generic OAuth panel drives this: /authorize must answer with
// deviceCode (that is what tells it this is a device flow rather than a
// redirect), and /exchange must answer {pending:true} until approval, then
// {success:true}.

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
)

const (
	// AlysisCodeAPIBase is the public device-login and gateway API host. It is
	// not a credential: the service authenticates device keys server-side.
	alysisCodeAPIBase = "https://vzigujbcjjmpntxhmyvr.supabase.co/functions/v1"

	// AlysisCodeSite serves the /activate approval page.
	alysisCodeSite = "https://alysiscode.com"
)

// Overridable so tests can point the handshake at an httptest server.
var alysisCodeBase = alysisCodeAPIBase

// alysisDeviceGrant is what /authorize returns. Field names match the keys the
// generic dashboard panel reads; expiresIn/interval drive its poll loop.
type alysisDeviceGrant struct {
	DeviceCode string `json:"deviceCode"`
	UserCode   string `json:"userCode"`
	AuthURL    string `json:"authUrl"`
	Interval   int    `json:"interval"`
	ExpiresIn  int    `json:"expiresIn"`
}

// HandleAlysisAuthorize starts a device login.
// GET /api/oauth/alysis/authorize
func (h *OAuthHandler) HandleAlysisAuthorize(w http.ResponseWriter, r *http.Request) {
	installationID := randomString(8) + "-" + randomString(4) + "-" + randomString(4)
	body, err := json.Marshal(map[string]any{
		"client_name":     "9router",
		"installation_id": installationID,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "alysis: encode request")
		return
	}

	var upstream struct {
		DeviceCode     string `json:"device_code"`
		UserCode       string `json:"user_code"`
		Verification   string `json:"verification_url"`
		VerificationOK string `json:"verification_url_complete"`
		Interval       int    `json:"interval"`
		ExpiresIn      int    `json:"expires_in"`
	}
	if err := alysisCodePost(r, "/device-code", body, &upstream); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "alysis: "+err.Error())
		return
	}
	if upstream.DeviceCode == "" || upstream.UserCode == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "alysis: login service did not return a device code")
		return
	}

	// Only the path and query of the server's URL are trusted; the origin is
	// pinned here so a misconfigured backend cannot redirect sign-in elsewhere.
	authURL := alysisCodeSite + "/activate?code=" + upstream.UserCode
	if url := firstNonEmpty(upstream.VerificationOK, upstream.Verification); url != "" {
		if i := strings.Index(url, "/activate"); i >= 0 {
			authURL = alysisCodeSite + url[i:]
		}
	}

	if upstream.Interval <= 0 {
		upstream.Interval = 5
	}
	if upstream.ExpiresIn <= 0 {
		upstream.ExpiresIn = 900
	}

	handlerutil.WriteJSON(w, http.StatusOK, alysisDeviceGrant{
		DeviceCode: upstream.DeviceCode,
		UserCode:   upstream.UserCode,
		AuthURL:    authURL,
		Interval:   upstream.Interval,
		ExpiresIn:  upstream.ExpiresIn,
	})
}

// HandleAlysisExchange polls the device login once.
// POST /api/oauth/alysis/exchange  Body: {"deviceCode":"…"}
//
// Called repeatedly by the dashboard; a pending approval is a 200 with
// {pending:true} so the panel keeps waiting instead of showing an error.
func (h *OAuthHandler) HandleAlysisExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"deviceCode"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil || json.Unmarshal(body, &req) != nil || strings.TrimSpace(req.DeviceCode) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing or invalid deviceCode")
		return
	}

	payload, err := json.Marshal(map[string]string{"device_code": strings.TrimSpace(req.DeviceCode)})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "alysis: encode request")
		return
	}

	var upstream struct {
		Status string `json:"status"`
		Key    string `json:"key"`
		Email  string `json:"email"`
	}
	if err := alysisCodePost(r, "/device-token", payload, &upstream); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "alysis: "+err.Error())
		return
	}

	switch strings.ToLower(strings.TrimSpace(upstream.Status)) {
	case "approved":
		// fall through to storing the key
	case "pending", "":
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"pending": true})
		return
	case "denied":
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "alysis: sign-in was rejected on the website")
		return
	case "slow_down":
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"pending": true, "error": "slow_down"})
		return
	case "expired", "not_found", "already_claimed":
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "alysis: this sign-in code expired, start again")
		return
	default:
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "alysis: unexpected login status "+upstream.Status)
		return
	}

	key := strings.TrimSpace(upstream.Key)
	if key == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "alysis: login service did not return a key")
		return
	}

	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		if upstream.Email != "" {
			name = upstream.Email
		} else {
			name = "Alysis account"
		}
	}

	raw, err := json.Marshal(map[string]any{"accessToken": key})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "alysis: encode connection data")
		return
	}
	connID := "alysis-oauth-" + shortID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, 'alysis', 'oauth', ?, 1, ?, ?, ?)`,
		connID, name, string(raw), now, now,
	); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "alysis: store connection: "+err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"connection": connID,
		"email":      upstream.Email,
	})
}

// alysisCodePost POSTs JSON to an Alysis device endpoint and decodes the reply.
// The returned error is safe to surface: it never contains the body verbatim
// beyond a short truncation, and never the request payload.
func alysisCodePost(r *http.Request, path string, payload []byte, out any) error {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, alysisCodeBase+path, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("login service unreachable: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login service returned HTTP %d: %s", resp.StatusCode, truncateForMsg(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}
	return nil
}
