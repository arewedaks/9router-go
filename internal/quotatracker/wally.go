package quotatracker

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
)

// wallyPayload is RunAnywhere's /v1/me response. It is the only usage surface
// the service exposes: /v1/credits, /v1/usage and /v1/balance are all 404 or
// 401 for an API key, and /v1/me is deliberately not part of the OpenAI shape.
type wallyPayload struct {
	Email             string  `json:"email"`
	Plan              string  `json:"plan"`
	TokensThisMonth   float64 `json:"tokens_this_month"`
	MonthlyTokenLimit float64 `json:"monthly_token_limit"`
}

// fetchWally returns the token allowance for one RunAnywhere (Wally) connection.
//
// The service reports consumption but not a remaining credit balance, and it
// answers monthly_token_limit=0 on every account seen so far. Zero is
// ambiguous — it reads either as "no limit on this plan" or as "the field is
// unfilled" — so this reports the used count as the fact it is and only shows
// a total when the API actually supplies one. Inventing a limit would make the
// dashboard display a ceiling the provider never set.
func fetchWally(ctx context.Context, client *http.Client, creds Credentials) (Result, error) {
	token := creds.AccessToken
	if token == "" {
		token = creds.APIKey
	}
	if token == "" {
		return Result{Message: "Credential not available for quota lookup."}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, creds.UsageURL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("create quota request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range creds.StaticHeader {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("quota request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("read quota response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Result{Message: "Credential invalid or expired."}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Result{Message: fmt.Sprintf("Quota API error (%d).", resp.StatusCode)}, nil
	}

	var payload wallyPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return Result{}, fmt.Errorf("parse quota response: %w", err)
	}

	q := Quota{
		Used:        payload.TokensThisMonth,
		Total:       payload.MonthlyTokenLimit,
		Unlimited:   payload.MonthlyTokenLimit <= 0,
		Recurring:   true,
		Unit:        "tokens",
		DisplayName: "Tokens this month",
	}
	// A recurring allowance whose total is unknown still has a reset boundary
	// (the calendar month), but the service does not publish the exact instant,
	// so ResetAt stays empty rather than being guessed.
	return Result{
		Plan:   payload.Plan,
		Quotas: map[string]Quota{"monthly_tokens": q},
	}, nil
}
