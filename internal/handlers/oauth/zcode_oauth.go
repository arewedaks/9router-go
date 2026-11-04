package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// ZCode device OAuth flow.
//
// ZCode (zcode.z.ai) serves the free Start Plan / Weekend Build quota for Z.ai accounts.
// Its flow:
//   1. Generate 32 random bytes (64-char hex) as pollToken.
//   2. POST {origin}/api/v1/oauth/cli/init with Bearer <pollToken> and {"provider":"zai"}.
//      Returns flow_id, authorize_url, poll_interval_sec, expires_at.
//   3. User opens authorize_url in browser and approves with their Z.ai account.
//   4. GET {origin}/api/v1/oauth/cli/poll/{flow_id} with Bearer <pollToken>.
//      Status "ready" returns the plan JWT, refresh_token, and user account metadata.

const (
	zcodeOAuthDefaultOrigin = "https://zcode.z.ai"
	zcodeFlowTTL            = 10 * time.Minute
)

func zcodeOAuthOrigin() string {
	if v := strings.TrimSpace(os.Getenv("ZCODE_OAUTH_ORIGIN")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return zcodeOAuthDefaultOrigin
}

func zcodeUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type zcodePendingFlow struct {
	pollToken string
	expires   time.Time
}

var (
	zcodeFlowsMu sync.Mutex
	zcodeFlows   = map[string]zcodePendingFlow{}
)

func rememberZcodeFlow(flowID string, f zcodePendingFlow) {
	zcodeFlowsMu.Lock()
	defer zcodeFlowsMu.Unlock()
	now := time.Now()
	for k, v := range zcodeFlows {
		if now.After(v.expires) {
			delete(zcodeFlows, k)
		}
	}
	zcodeFlows[flowID] = f
}

func lookupZcodeFlow(flowID string) (zcodePendingFlow, bool) {
	zcodeFlowsMu.Lock()
	defer zcodeFlowsMu.Unlock()
	f, ok := zcodeFlows[flowID]
	if !ok || time.Now().After(f.expires) {
		delete(zcodeFlows, flowID)
		return zcodePendingFlow{}, false
	}
	return f, true
}

func forgetZcodeFlow(flowID string) {
	zcodeFlowsMu.Lock()
	defer zcodeFlowsMu.Unlock()
	delete(zcodeFlows, flowID)
}

// HandleZcodeFlowInfo reports the OAuth flow shape to the dashboard panel.
// GET /api/oauth/zcode/flow
func (h *OAuthHandler) HandleZcodeFlowInfo(w http.ResponseWriter, r *http.Request) {
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":  "zcode",
		"kind":      "device_code",
		"startHint": "Sign in with your Z.ai account to consume the free ZCode Start Plan quota.",
	})
}

// HandleZcodeAuthorize begins a ZCode device flow.
// GET /api/oauth/zcode/authorize
func (h *OAuthHandler) HandleZcodeAuthorize(w http.ResponseWriter, r *http.Request) {
	pollBytes := make([]byte, 32)
	if _, err := rand.Read(pollBytes); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to generate poll token")
		return
	}
	pollToken := hex.EncodeToString(pollBytes)

	origin := zcodeOAuthOrigin()
	payload, _ := json.Marshal(map[string]any{"provider": "zai"})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, origin+"/api/v1/oauth/cli/init", bytes.NewReader(payload))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+pollToken)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Warn("oauth", "zcode device init failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "could not start ZCode sign-in: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Data *struct {
			FlowID       string  `json:"flow_id"`
			AuthorizeURL string  `json:"authorize_url"`
			ExpiresAt    float64 `json:"expires_at"`
			IntervalSec  float64 `json:"poll_interval_sec"`
			PollToken    string  `json:"poll_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Data == nil || resp.StatusCode != http.StatusOK {
		handlerutil.WriteJSONError(w, http.StatusBadGateway,
			fmt.Sprintf("ZCode device init failed (HTTP %d): %s", resp.StatusCode, truncateForOAuth(string(raw), 200)))
		return
	}
	d := out.Data
	if d.FlowID == "" || d.AuthorizeURL == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "ZCode init response missing flow_id or authorize_url")
		return
	}

	returnedPollToken := d.PollToken
	if returnedPollToken == "" {
		returnedPollToken = pollToken
	}

	intervalSec := int(d.IntervalSec)
	if intervalSec < 2 {
		intervalSec = 2
	}
	expiresIn := 300
	if d.ExpiresAt > 0 {
		rem := int(d.ExpiresAt - float64(time.Now().Unix()))
		if rem > 0 {
			expiresIn = rem
		}
	}

	rememberZcodeFlow(d.FlowID, zcodePendingFlow{
		pollToken: returnedPollToken,
		expires:   time.Now().Add(time.Duration(expiresIn) * time.Second),
	})

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":       "zcode",
		"deviceCode":     d.FlowID,
		"userCode":       "",
		"authUrl":        d.AuthorizeURL,
		"interval":       intervalSec,
		"expiresIn":      expiresIn,
		"pollIntervalMs": intervalSec * 1000,
	})
}

// HandleZcodeExchange polls once for the ZCode token.
// POST /api/oauth/zcode/exchange  {"deviceCode":"<flowID>", "name":"..."}
func (h *OAuthHandler) HandleZcodeExchange(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
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
	flowID := strings.TrimSpace(req.DeviceCode)
	if flowID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing deviceCode")
		return
	}

	flow, ok := lookupZcodeFlow(flowID)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "zcode: this sign-in expired, please start again")
		return
	}

	origin := zcodeOAuthOrigin()
	pollURL := origin + "/api/v1/oauth/cli/poll/" + flowID

	pollReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, pollURL, nil)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build poll request")
		return
	}
	pollReq.Header.Set("Accept", "application/json")
	pollReq.Header.Set("Authorization", "Bearer "+flow.pollToken)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(pollReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "poll request failed: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Data *struct {
			Status string         `json:"status"`
			Token  string         `json:"token"`
			User   map[string]any `json:"user"`
			Zai    *struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"zai"`
			Bigmodel *struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"bigmodel"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Data == nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid poll response: "+truncateForOAuth(string(raw), 200))
		return
	}

	d := out.Data
	switch d.Status {
	case "failed", "expired":
		forgetZcodeFlow(flowID)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "ZCode sign-in "+d.Status)
		return
	case "ready":
		// success, fall through
	default:
		// pending
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"pending": true,
		})
		return
	}

	jwt := strings.TrimSpace(d.Token)
	if jwt == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "ZCode ready but returned no plan JWT")
		return
	}

	var refreshToken string
	if d.Zai != nil {
		refreshToken = d.Zai.RefreshToken
	} else if d.Bigmodel != nil {
		refreshToken = d.Bigmodel.RefreshToken
	}

	var email, accountUUID string
	if d.User != nil {
		if e, ok := d.User["email"].(string); ok {
			email = strings.TrimSpace(e)
		}
		if u, ok := d.User["user_id"].(string); ok {
			accountUUID = strings.TrimSpace(u)
		}
	}

	forgetZcodeFlow(flowID)

	// Preserve device identity across re-logins for the same account.
	deviceID := zcodeUUID()
	sessionSeed := zcodeUUID()
	var existingConnID string
	if h.Repo != nil && h.Repo.RawDB() != nil && email != "" {
		if prevID, prevPSD := zcodeExistingIdentity(r.Context(), h.Repo.RawDB(), email); prevID != "" {
			existingConnID = prevID
			if id, ok := prevPSD["deviceId"].(string); ok && id != "" {
				deviceID = id
			}
			if seed, ok := prevPSD["sessionSeed"].(string); ok && seed != "" {
				sessionSeed = seed
			}
			if uuid, ok := prevPSD["accountUUID"].(string); ok && uuid != "" {
				accountUUID = uuid
			}
		}
	}

	psd := map[string]any{
		"authMethod":  "device",
		"deviceId":    deviceID,
		"sessionSeed": sessionSeed,
	}
	if accountUUID != "" {
		psd["accountUUID"] = accountUUID
	}

	connData := map[string]any{
		"apiKey":               jwt,
		"accessToken":          jwt,
		"providerSpecificData": psd,
	}
	if refreshToken != "" {
		connData["refreshToken"] = refreshToken
	}
	if email != "" {
		connData["email"] = email
	}

	encoded, err := json.Marshal(connData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode connection data")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = email
	}
	if name == "" {
		name = "ZCode account"
	}

	now := time.Now().UTC().Format(time.RFC3339)
	connID := existingConnID
	if existingConnID != "" && h.Repo != nil && h.Repo.RawDB() != nil {
		_, _ = h.Repo.RawDB().Exec(
			"UPDATE providerConnections SET name = ?, data = ?, updatedAt = ?, isActive = 1 WHERE id = ?",
			name, string(encoded), now, existingConnID,
		)
	} else {
		newID, insertErr := h.insertOAuthConnection("zcode", name, email, string(encoded))
		if insertErr != nil {
			log.Error("oauth", "save zcode connection failed", "error", insertErr)
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save connection: "+insertErr.Error())
			return
		}
		connID = newID
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id":       connID,
			"provider": "zcode",
			"email":    email,
		},
	})
}

// zcodeExistingIdentity finds an existing active connection row for provider 'zcode' matching email.
func zcodeExistingIdentity(ctx context.Context, db *sql.DB, email string) (string, map[string]any) {
	email = strings.TrimSpace(email)
	if db == nil || email == "" {
		return "", nil
	}
	rows, err := db.QueryContext(ctx,
		"SELECT id, data FROM providerConnections WHERE provider = 'zcode' AND isActive = 1 ORDER BY updatedAt DESC")
	if err != nil {
		return "", nil
	}
	defer func() { _ = rows.Close() }()
	var bestID string
	var bestPSD map[string]any
	for rows.Next() {
		var id, data string
		if rows.Scan(&id, &data) != nil {
			continue
		}
		var parsed struct {
			Email string         `json:"email"`
			PSD   map[string]any `json:"providerSpecificData"`
		}
		if json.Unmarshal([]byte(data), &parsed) != nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(parsed.Email), email) {
			continue
		}
		bestID = id
		bestPSD = parsed.PSD
		break
	}
	if err := rows.Err(); err != nil {
		return "", nil
	}
	return bestID, bestPSD
}
