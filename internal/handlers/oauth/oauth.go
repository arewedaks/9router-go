package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	mathRand "math/rand"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// OAuthHandler handles OAuth token import and social auth exchange endpoints.
type OAuthHandler struct {
	Repo *db.Repo
}

// NewOAuthHandler initializes an OAuthHandler.
func NewOAuthHandler(repo *db.Repo) *OAuthHandler {
	return &OAuthHandler{Repo: repo}
}

// HandleOAuthImport saves credentials from CLI token import (Codex, Cursor, GitLab, etc.).
// POST /api/oauth/{provider}/import
func (h *OAuthHandler) HandleOAuthImport(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken,omitempty"`
		APIKey       string `json:"apiKey,omitempty"`
		MachineID    string `json:"machineId,omitempty"`
		Name         string `json:"name,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	credential := req.AccessToken
	if credential == "" {
		credential = req.APIKey
	}
	if credential == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing accessToken or apiKey")
		return
	}

	connName := req.Name
	if connName == "" {
		connName = provider + " import"
	}

	connID := provider + "-import-" + randomString(12)

	// Build data JSON with provider-specific fields
	dataFields := map[string]any{
		"apiKey": credential,
	}
	if req.RefreshToken != "" {
		dataFields["refreshToken"] = req.RefreshToken
	}
	if req.MachineID != "" {
		dataFields["providerSpecificData"] = map[string]any{
			"machineId": req.MachineID,
		}
	}

	data, err := json.Marshal(dataFields)
	if err != nil {
		log.Error("oauth", "marshal import data failed", "provider", provider, "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to process connection data")
		return
	}

	now := currentTimestamp()
	_, err = h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'apikey', ?, 1, ?, ?, ?)`,
		connID, provider, connName, string(data), now, now,
	)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("save connection: %v", err))
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":         connID,
		"provider":   provider,
		"name":       connName,
		"connection": connID,
	})
}

// Kiro social sign-in endpoints (Kiro's own auth service, NOT Cognito).
// Ported from VansRouter's open-sse registry/kiro.js `oauth` block.
const (
	kiroSocialClientID           = "kiro-cli"
	kiroSocialDeviceAuthorizeURL = "https://prod.us-east-1.auth.desktop.kiro.dev/oauth/device/authorization"
	kiroSocialDevicePollURL      = "https://prod.us-east-1.auth.desktop.kiro.dev/oauth/device/poll"
)

// HandleOAuthKiroSocialAuthorize starts the Kiro social device flow.
// GET /api/oauth/kiro/social-authorize?provider=google|github
//
// Kiro uses an RFC 8628 device flow: we request a device code, show the user
// the verification URL, and poll until they approve. No browser redirect and no
// copy/paste — which is why this works on a headless server.
func (h *OAuthHandler) HandleOAuthKiroSocialAuthorize(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("provider")
	if p != "google" && p != "github" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid provider, use 'google' or 'github'")
		return
	}

	loginProvider := "Google"
	if p == "github" {
		loginProvider = "Github"
	}

	reqBody, _ := json.Marshal(map[string]any{
		"clientId":      kiroSocialClientID,
		"loginProvider": loginProvider,
	})
	resp, err := http.Post(kiroSocialDeviceAuthorizeURL, "application/json", strings.NewReader(string(reqBody)))
	if err != nil {
		log.Error("oauth", "kiro device authorization request failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "device authorization failed")
		return
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read device response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		log.Warn("oauth", "kiro device authorization rejected", "status", resp.StatusCode, "body", truncate(string(raw), 300))
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "device authorization failed: "+truncate(string(raw), 200))
		return
	}

	var data struct {
		VerificationURI         string `json:"verificationUri"`
		VerificationURIComplete string `json:"verificationUriComplete"`
		DeviceCode              string `json:"deviceCode"`
		UserCode                string `json:"userCode"`
		ExpiresInMilliseconds   int    `json:"expiresInMilliseconds"`
		IntervalInMilliseconds  int    `json:"intervalInMilliseconds"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid device response")
		return
	}

	authURL := data.VerificationURIComplete
	if authURL == "" {
		authURL = data.VerificationURI
	}
	expiresIn := data.ExpiresInMilliseconds / 1000
	if expiresIn == 0 {
		expiresIn = 300
	}
	interval := data.IntervalInMilliseconds / 1000
	if interval == 0 {
		interval = 5
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"authUrl":    authURL,
		"deviceCode": data.DeviceCode,
		"userCode":   data.UserCode,
		"expiresIn":  expiresIn,
		"interval":   interval,
		"provider":   p,
	})
}

// HandleOAuthKiroSocialExchange polls the Kiro device flow for tokens.
// POST /api/oauth/kiro/social-exchange  Body: {"deviceCode","provider"}
//
// The frontend calls this repeatedly until it returns success. Pending states
// come back as {"success":false,"pending":true} with HTTP 200 so the poll loop
// keeps going without treating it as an error.
func (h *OAuthHandler) HandleOAuthKiroSocialExchange(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		DeviceCode     string `json:"deviceCode"`
		Provider       string `json:"provider"`
		TargetProvider string `json:"targetProvider"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.DeviceCode == "" || (req.Provider != "google" && req.Provider != "github") {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing or invalid deviceCode or provider")
		return
	}

	pollBody, _ := json.Marshal(map[string]any{
		"deviceCode": req.DeviceCode,
		"clientId":   kiroSocialClientID,
	})
	resp, err := http.Post(kiroSocialDevicePollURL, "application/json", strings.NewReader(string(pollBody)))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "device poll failed")
		return
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read poll response")
		return
	}

	var data struct {
		Error        string `json:"error"`
		Status       string `json:"status"`
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
		ProfileArn   string `json:"profileArn"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid poll response")
		return
	}

	// RFC 8628 progress states: keep polling.
	progress := data.Error
	if progress == "" {
		progress = data.Status
	}
	if progress == "authorization_pending" || progress == "slow_down" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"pending": true,
			"error":   progress,
		})
		return
	}
	if data.Error != "" || resp.StatusCode < 200 || resp.StatusCode > 299 {
		status := resp.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadRequest
		}
		handlerutil.WriteJSONError(w, status, "authorization failed: "+data.Error)
		return
	}
	if data.AccessToken == "" && data.RefreshToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid token response")
		return
	}

	// Kiro signs in as `kiro` regardless of the upstream identity provider.
	targetProvider := "kiro"
	if req.TargetProvider == "kiro" || strings.HasPrefix(req.TargetProvider, "kiro-") {
		targetProvider = req.TargetProvider
	}

	email := jwtEmail(data.AccessToken)

	connID := targetProvider + "-oauth-" + randomString(12)
	providerSpecificData := map[string]any{
		"authMethod": "imported",
		"provider":   strings.ToUpper(req.Provider[:1]) + req.Provider[1:],
	}
	if data.ProfileArn != "" {
		providerSpecificData["profileArn"] = data.ProfileArn
	}
	dataMap := map[string]any{
		"accessToken":          data.AccessToken,
		"providerSpecificData": providerSpecificData,
		"testStatus":           "active",
	}
	if data.RefreshToken != "" {
		dataMap["refreshToken"] = data.RefreshToken
	}
	if data.ExpiresIn > 0 {
		dataMap["expiresAt"] = time.Now().Add(time.Duration(data.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}

	encoded, err := json.Marshal(dataMap)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode connection data")
		return
	}

	name := email
	if name == "" {
		name = "Kiro " + strings.ToUpper(req.Provider[:1]) + req.Provider[1:]
	}

	now := currentTimestamp()
	priority, perr := h.Repo.NextConnectionPriority(targetProvider)
	if perr != nil {
		priority = 1
	}
	var emailArg any
	if email != "" {
		emailArg = email
	}
	if _, err := h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'oauth', ?, ?, ?, 1, ?, ?, ?)`,
		connID, targetProvider, name, emailArg, priority, string(encoded), now, now,
	); err != nil {
		log.Error("oauth", "save Kiro social connection failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id":       connID,
			"provider": targetProvider,
			"email":    email,
		},
	})
}

// jwtEmail decodes an unverified JWT payload and returns its `email` claim.
// The signature is not checked: the token came straight from Kiro's token
// endpoint over TLS, and this value only labels the connection.
func jwtEmail(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Email
}

// HandleOAuthCodexBulkImport handles bulk Codex token import.
// POST /api/oauth/codex/bulk-import
func (h *OAuthHandler) HandleOAuthCodexBulkImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Tokens []struct {
			AccessToken string `json:"accessToken"`
			Name        string `json:"name,omitempty"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	var imported []string
	for _, t := range req.Tokens {
		if t.AccessToken == "" {
			continue
		}
		name := t.Name
		if name == "" {
			name = "Codex import"
		}
		connID := "codex-bulk-" + randomString(12)
		data, err := json.Marshal(map[string]string{"accessToken": t.AccessToken})
		if err != nil {
			log.Error("oauth", "marshal Codex bulk import failed", "error", err)
			continue
		}
		now := currentTimestamp()
		_, err = h.Repo.RawDB().Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, 'codex', 'oauth', ?, 1, ?, ?, ?)`,
			connID, name, string(data), now, now,
		)
		if err == nil {
			imported = append(imported, connID)
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"imported": imported,
		"count":    len(imported),
	})
}

func currentTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		rng := mathRand.New(mathRand.NewSource(time.Now().UnixNano()))
		for i := range b {
			b[i] = letters[rng.Intn(len(letters))]
		}
		return string(b)
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

func sha256Base64(input string) string {
	h := sha256.Sum256([]byte(input))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
