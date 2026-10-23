package oauth

// Perch token refresh (Supabase Auth).
//
// The access token Perch accepts is a Supabase session JWT with a one-hour
// lifetime, and Supabase rotates the refresh token on every use. Without a
// refresher here a stored Perch connection went stale an hour after sign-in and
// every model call answered
//
//	401 {"ok":false,"error":"Unauthorized","errorCode":"not_authenticated"}
//
// which the harness surfaced as "your API key was rejected by the provider" —
// pointing the operator at the wrong thing entirely.
//
// The exchange is Supabase's standard refresh grant:
//
//	POST {supabaseUrl}/auth/v1/token?grant_type=refresh_token
//	apikey: <anon key>
//	{"refresh_token": "<stored>"}
//	→ {"access_token":…, "refresh_token":…, "expires_in":3600, "user":{…}}
//
// The anon key is read from Perch's own config endpoint rather than hardcoded:
// it is a rotating public client credential and the Supabase project moved at
// least once during this integration.

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func init() {
	Register("perch", RefreshPerch)
	Register("pch", RefreshPerch)
	Register("perchai", RefreshPerch)
}

// perchAuthEndpoints is the config route the CLI reads its Supabase coordinates
// from. Fetched per refresh rather than cached: the anon key is a rotating
// public credential and the Supabase project has moved before.
const perchAuthConfigURL = "https://app.perchai.app/api/perch-terminal/cli-auth/config"

type perchAuthConfig struct {
	SupabaseURL     string `json:"supabaseUrl"`
	SupabaseAnonKey string `json:"supabaseAnonKey"`
}

func fetchPerchAuthConfig(ctx context.Context, client *http.Client) (*perchAuthConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, perchAuthConfigURL, nil)
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
	var cfg perchAuthConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("perch: decode auth config: %w", err)
	}
	if cfg.SupabaseURL == "" || cfg.SupabaseAnonKey == "" {
		return nil, fmt.Errorf("perch: auth config missing supabase coordinates")
	}
	return &cfg, nil
}

// truncateForMsg keeps an upstream error body short enough for a log line or a
// toast without dropping the part that names the problem.
func truncateForMsg(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// RefreshPerch exchanges a stored refresh token for a fresh Supabase session.
func RefreshPerch(ctx context.Context, p *Params) (*TokenResult, error) {
	if p == nil {
		return nil, fmt.Errorf("perch: nil params")
	}
	if strings.TrimSpace(p.RefreshToken) == "" {
		return nil, fmt.Errorf("perch: refresh_token is required — sign in again")
	}

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	cfg, err := fetchPerchAuthConfig(ctx, client)
	if err != nil {
		return nil, err
	}
	supabase := strings.TrimRight(cfg.SupabaseURL, "/")
	if supabase == "" || cfg.SupabaseAnonKey == "" {
		return nil, fmt.Errorf("perch: auth config missing supabase coordinates")
	}

	payload, err := json.Marshal(map[string]string{
		"refresh_token": strings.TrimSpace(p.RefreshToken),
	})
	if err != nil {
		return nil, fmt.Errorf("perch: marshal refresh: %w", err)
	}

	tokenURL := supabase + "/auth/v1/token?grant_type=refresh_token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("perch: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Supabase requires the publishable key on both headers; the bearer one is
	// what the gateway validates, apikey is what the project routes on.
	req.Header.Set("apikey", cfg.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseAnonKey)
	req.Header.Set("User-Agent", "perchai-cli/unknown")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perch: refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("perch: refresh returned HTTP %d: %s", resp.StatusCode, truncateForMsg(string(body)))
	}

	var session struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		User         struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return nil, fmt.Errorf("perch: decode refresh: %w", err)
	}
	if session.AccessToken == "" {
		return nil, fmt.Errorf("perch: refresh returned no access token")
	}

	out := &TokenResult{
		AccessToken: session.AccessToken,
		ExpiresIn:   session.ExpiresIn,
		// Supabase rotates the refresh token on every exchange. Storing the old
		// one would make the very next refresh fail with "invalid refresh token".
		RefreshToken: session.RefreshToken,
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	if session.User.Email != "" {
		out.ExtraFields = map[string]any{"email": session.User.Email}
	}
	return out, nil
}
