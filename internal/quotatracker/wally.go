package quotatracker

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// wallyPayload is RunAnywhere's /v1/me response — the account profile. It
// carries no credit balance: Wally bills "against prepaid credits" per its API
// description, but the public API (OpenAPI: only /chat/completions and
// /models) publishes no credits endpoint, and /v1/credits & /v1/usage answer
// {"code":"unauthorized","message":"not signed in"} to a key because they are
// session-cookie endpoints. So the balance is not readable, and this handler
// does not pretend otherwise.
type wallyPayload struct {
	Email             string  `json:"email"`
	Plan              string  `json:"plan"`
	TokensThisMonth   float64 `json:"tokens_this_month"`
	MonthlyTokenLimit float64 `json:"monthly_token_limit"`
}

// wallyLimit is one allowance read from a live completion's rate-limit headers.
type wallyLimit struct {
	key   string
	label string
	unit  string
}

// wallyLimits maps the response headers Wally returns on a non-streaming
// completion to the rows the panel shows. Streaming responses carry none of
// them, so the probe below must ask for a non-streaming call.
var wallyLimits = []wallyLimit{
	{"x-ratelimit-limit-requests", "Requests", "requests"},
	{"x-ratelimit-limit-tokens", "Tokens", "tokens"},
	{"x-ratelimit-limit-concurrent-requests", "Concurrent requests", "requests"},
}

// fetchWally reports RunAnywhere (Wally) usage.
//
// Wally has no usage endpoint, so the numbers are assembled from the two places
// they do exist:
//
//   - /v1/me gives the live consumption count (tokens_this_month) and the plan.
//     Its monthly_token_limit is 0 on every account observed ("beta"): the plan
//     states no monthly ceiling, so no total is invented from it.
//   - the per-key limits exist only as x-ratelimit-* response headers on a real
//     completion. A minimal non-streaming request (max_tokens:1) returns them.
//     This costs one metered generation and holds a concurrency slot, which is
//     acceptable because the caller asks for a single connection at a time from
//     the quota panel — never a fleet probe.
//
// The result is labelled as measured: consumption is named "Tokens this month",
// and each header limit is reported with its remaining/total pair, which is what
// the dashboard's bar is designed for.
func fetchWally(ctx context.Context, client *http.Client, creds Credentials) (Result, error) {
	token := creds.AccessToken
	if token == "" {
		token = creds.APIKey
	}
	if token == "" {
		return Result{Message: "Credential not available for quota lookup."}, nil
	}

	profile, err := fetchWallyProfile(ctx, client, creds, token)
	if err != nil {
		return Result{}, err
	}
	if profile == nil {
		// fetchWallyProfile already turned a refusal into a message.
		return wallyCredentialMessage(creds), nil
	}

	quotas := map[string]Quota{}

	// Consumption: always available, and never divided by an invented total.
	quotas["monthly_tokens"] = Quota{
		Used:        nonNegative(profile.TokensThisMonth),
		Unlimited:   true,
		Unit:        "tokens",
		DisplayName: "Tokens this month",
	}

	// Per-key limits from a live call. A failure here leaves the consumption
	// row intact rather than blanking the panel: the balance question is
	// answered either way.
	if limits, err := fetchWallyLimits(ctx, client, creds, token); err == nil {
		for k, q := range limits {
			quotas[k] = q
		}
	}

	return Result{Plan: profile.Plan, Quotas: quotas}, nil
}

// wallyProbeModel is the model the limit probe asks for. A valid catalogue id
// is required — an unknown model is a 404 that carries no rate-limit headers —
// and this one is in Wally's served set.
func wallyProbeModel() string { return "glm-5.3-flash" }

// fetchWallyLimits reads the per-key caps Wally publishes only as response
// headers on a real completion. The request is the cheapest the API accepts
// (max_tokens:1) and must not stream, because streaming responses omit the
// headers. The generation content is discarded: only the headers are read.
func fetchWallyLimits(ctx context.Context, client *http.Client, creds Credentials, token string) (map[string]Quota, error) {
	endpoint := creds.BaseURL
	if endpoint == "" {
		endpoint = "https://inference.runanywhere.ai/v1/chat/completions"
	}

	payload, err := json.Marshal(map[string]any{
		"model":      wallyProbeModel(),
		"messages":   []map[string]string{{"role": "user", "content": "."}},
		"max_tokens": 1,
		"stream":     false,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range creds.StaticHeader {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	out := map[string]Quota{}
	for _, l := range wallyLimits {
		raw := strings.TrimSpace(resp.Header.Get(l.key))
		if raw == "" {
			continue
		}
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil || n <= 0 {
			continue
		}
		out[strings.TrimPrefix(l.key, "x-ratelimit-limit-")] = Quota{
			Total:       n,
			LimitOnly:   true,
			Unit:        l.unit,
			DisplayName: l.label,
		}
	}
	return out, nil
}

// fetchWallyProfile reads /v1/me. It returns (nil, nil) when the upstream
// refused the credential, so the caller can report it as a message.
func fetchWallyProfile(ctx context.Context, client *http.Client, creds Credentials, token string) (*wallyPayload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, creds.UsageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create quota request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range creds.StaticHeader {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("quota request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read quota response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quota API error (%d)", resp.StatusCode)
	}

	var payload wallyPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse quota response: %w", err)
	}
	return &payload, nil
}

// wallyCredentialMessage is the refusal text for a rejected key, kept in one
// place so the profile and limit paths cannot disagree.
func wallyCredentialMessage(Credentials) Result {
	return Result{Message: "Credential invalid or expired."}
}

func nonNegative(f float64) float64 {
	if f < 0 {
		return 0
	}
	return f
}
