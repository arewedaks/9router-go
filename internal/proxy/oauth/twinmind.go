package oauth

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func init() {
	Register("twinmind", RefreshTwinmind)
}

// twinmindFirebaseAPIKey is the web client's Firebase API key for the
// thirdear-ai project. It is a public web key, not a secret: it identifies the
// Firebase project the securetoken endpoint exchanges against, and every
// TwinMind browser session sends the same value.
const twinmindFirebaseAPIKey = "AIzaSyD2Sd_NP3vA4rwvoroKqDefpXZeCMDXcIQ"

// twinmindTokenURLForTest is the endpoint the refresher actually calls. It
// normally equals the securetoken URL with the API key; tests repoint it at an
// httptest server so no real network call is made.
var twinmindTokenURLForTest = "https://securetoken.googleapis.com/v1/token?key=" + twinmindFirebaseAPIKey

// twinmindTokenResponse is Google's Secure Token envelope.
type twinmindTokenResponse struct {
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    string `json:"expires_in"`
}

// RefreshTwinmind exchanges the stored Firebase refresh token for a fresh
// one-hour ID token. TwinMind's chat API takes `Authorization: Bearer <ID
// token>`, and the ID token expires every hour, so a connection whose stored
// credential is only the ID token dies on the second call — the refresh token
// is the long-lived credential the connection must hold.
//
// Google may rotate the refresh token on exchange, so the new one is returned
// in RefreshToken and persisted by the store.
func RefreshTwinmind(ctx context.Context, p *Params) (*TokenResult, error) {
	refreshToken := strings.TrimSpace(p.RefreshToken)
	if refreshToken == "" {
		// Some import paths stash the long-lived credential in the access-token
		// slot (it is opaque and much longer than an ID token, so callers cannot
		// tell them apart). Accept it there too.
		refreshToken = strings.TrimSpace(p.AccessToken)
	}
	if refreshToken == "" {
		return nil, fmt.Errorf("twinmind: no refresh token available")
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, twinmindTokenURLForTest, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("twinmind: create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("twinmind: refresh request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("twinmind: read refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("twinmind: refresh HTTP %d: %.200s", resp.StatusCode, string(raw))
	}

	var parsed twinmindTokenResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("twinmind: refresh response not JSON: %w", err)
	}
	if strings.TrimSpace(parsed.IDToken) == "" {
		return nil, fmt.Errorf("twinmind: refresh returned no id_token")
	}

	expiresIn := 3600
	if n, err := strconv.Atoi(strings.TrimSpace(parsed.ExpiresIn)); err == nil && n > 0 {
		expiresIn = n
	}

	out := &TokenResult{
		AccessToken: parsed.IDToken,
		ExpiresIn:   expiresIn,
	}
	// Persist the possibly-rotated refresh token; an empty string is dropped by
	// BuildConnectionUpdate so the stored one survives when Google does not
	// rotate.
	if parsed.RefreshToken != "" {
		out.RefreshToken = parsed.RefreshToken
	}
	return out, nil
}
