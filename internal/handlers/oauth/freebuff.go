package oauth

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

var freebuffAuthBaseURL = "https://freebuff.com"

// shortHash returns a 12-character hex SHA-256 hash of the input.
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:12]
}

// HandleFreebuffInitiate initiates the Freebuff device authorization flow by
// generating a secure auth code, fingerprint hash, and login URL.
// POST /api/oauth/freebuff/initiate
func (h *OAuthHandler) HandleFreebuffInitiate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Generate 16 cryptographically random bytes for the auth code
	rawBytes := make([]byte, 16)
	if _, err := rand.Read(rawBytes); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to generate random auth code")
		return
	}
	authCode := base64.RawURLEncoding.EncodeToString(rawBytes)

	// Compute SHA-256 fingerprint hash of the auth code
	hash := sha256.Sum256([]byte(authCode))
	fingerprintHash := hex.EncodeToString(hash[:])

	// Generate random UUID v4 for the fingerprint ID
	fingerprintID := uuid.New().String()

	// Set expiration to 15 minutes in the future
	expiresAt := time.Now().UTC().Add(15 * time.Minute)

	loginURL := fmt.Sprintf("%s/login?auth_code=%s", freebuffAuthBaseURL, authCode)

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"loginUrl":        loginURL,
		"authCode":        authCode,
		"fingerprintId":   fingerprintID,
		"fingerprintHash": fingerprintHash,
		"expiresAt":       expiresAt.Format(time.RFC3339),
	})
}

// HandleFreebuffPoll polls Freebuff authorization status and creates/updates provider connection in DB.
// Calls https://freebuff.com/api/auth/cli/status with method POST.
// POST /api/oauth/freebuff/poll
func (h *OAuthHandler) HandleFreebuffPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		FingerprintID        string `json:"fingerprintId"`
		FingerprintIDSnake   string `json:"fingerprint_id"`
		FingerprintHash      string `json:"fingerprintHash"`
		FingerprintHashSnake string `json:"fingerprint_hash"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	fpID := req.FingerprintID
	if fpID == "" {
		fpID = req.FingerprintIDSnake
	}
	fpHash := req.FingerprintHash
	if fpHash == "" {
		fpHash = req.FingerprintHashSnake
	}

	if fpID == "" || fpHash == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing fingerprintId or fingerprintHash")
		return
	}

	targetURL := freebuffAuthBaseURL + "/api/auth/cli/status"
	upstreamPayload, err := json.Marshal(map[string]string{
		"fingerprintId":   fpID,
		"fingerprintHash": fpHash,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode status request")
		return
	}

	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(upstreamPayload))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create status request failed: %v", err))
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("User-Agent", "9router/oauth")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(upReq)
	if err != nil {
		log.Error("oauth", "freebuff poll status request failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("freebuff status failed: %v", err))
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read freebuff status response")
		return
	}

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"status": "expired",
		})
		return
	}

	var upstream struct {
		Status         string `json:"status"`
		AuthToken      string `json:"authToken"`
		AuthTokenSnake string `json:"auth_token"`
		Token          string `json:"token"`
		Email          string `json:"email"`
		Name           string `json:"name"`
		Message        string `json:"message"`
		Error          string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &upstream); err != nil {
		log.Error("oauth", "freebuff poll unmarshal failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to parse freebuff status response")
		return
	}

	status := strings.ToLower(strings.TrimSpace(upstream.Status))
	if status == "" {
		if strings.Contains(strings.ToLower(upstream.Error), "expired") || strings.Contains(strings.ToLower(upstream.Message), "expired") {
			status = "expired"
		} else {
			status = "pending"
		}
	}

	authToken := upstream.AuthToken
	if authToken == "" {
		authToken = upstream.AuthTokenSnake
	}
	if authToken == "" {
		authToken = upstream.Token
	}

	if status == "authorized" && authToken != "" {
		connID := "fb-" + shortHash(authToken)
		connName := upstream.Name
		if connName == "" && upstream.Email != "" {
			connName = "Freebuff (" + upstream.Email + ")"
		} else if connName == "" {
			connName = "Freebuff (" + connID + ")"
		}

		dataMap := map[string]any{
			"authToken":   authToken,
			"apiKey":      authToken,
			"accessToken": authToken,
		}
		if upstream.Email != "" {
			dataMap["email"] = upstream.Email
		}
		if upstream.Name != "" {
			dataMap["name"] = upstream.Name
		}

		dataBytes, err := json.Marshal(dataMap)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal connection data")
			return
		}

		if h.Repo != nil && h.Repo.RawDB() != nil {
			now := currentTimestamp()
			var exists int
			_ = h.Repo.RawDB().QueryRow("SELECT COUNT(*) FROM providerConnections WHERE id = ?", connID).Scan(&exists)
			if exists > 0 {
				_, err = h.Repo.RawDB().Exec(
					"UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?",
					connName, string(dataBytes), now, connID,
				)
			} else {
				_, err = h.Repo.RawDB().Exec(
					"INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, 'freebuff', 'oauth', ?, 1, ?, ?, ?)",
					connID, connName, string(dataBytes), now, now,
				)
			}
			if err != nil {
				log.Error("oauth", "save freebuff connection failed", "conn", connID, "error", err)
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save connection: %v", err))
				return
			}
		}

		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"status":       "authorized",
			"connectionId": connID,
		})
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": status,
	})
}
