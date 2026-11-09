package quotatracker

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// deepAITheosPayload is the lane's daily-allowance endpoint. DeepAI Code meters
// premier models in "Theos", granted once per machine per day:
//
//	GET /deepai_code/theos
//	{"enabled":true,"balance":"48.51650","granted":"50.00000",
//	 "refresh_at":"2026-11-10T06:02:24+00:00","lane_version":4,
//	 "check_in":{"nonce":"…","seconds":600}}
//
// balance and granted are JSON *strings* holding decimals, not numbers, so they
// are read into a string field and parsed — declaring them float64 makes every
// real response fail to unmarshal, the same trap the codebuddy "precise" fields
// have.
type deepAITheosPayload struct {
	Enabled   bool   `json:"enabled"`
	Balance   string `json:"balance"`
	Granted   string `json:"granted"`
	RefreshAt string `json:"refresh_at"`
}

// decimal parses a JSON-decimal-as-string ("48.51650"), returning ok=false for
// anything that is not a finite number.
func decimal(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// fetchDeepAI reports the remaining daily Theos for one DeepAI Code connection.
//
// The allowance recurs daily (refresh_at is the next grant, not an expiry), so
// Recurring is true and the UI says "Resets in". A lane that answers
// enabled=false has Theos switched off for the account, which is a normal
// outcome rather than an error — reporting the numbers anyway would claim an
// allowance that is not being granted.
func fetchDeepAI(ctx context.Context, client *http.Client, creds Credentials) (Result, error) {
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
	// The lane authenticates with `Api-Key`, not a bearer, and the lane version
	// header is what tells the server which build is asking.
	req.Header.Set("Api-Key", token)
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

	var payload deepAITheosPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return Result{}, fmt.Errorf("parse quota response: %w", err)
	}
	if !payload.Enabled {
		return Result{Message: "Daily Theos are disabled for this account."}, nil
	}

	granted, okGranted := decimal(payload.Granted)
	balance, okBalance := decimal(payload.Balance)
	if !okGranted && !okBalance {
		return Result{Message: "Quota API returned no measurable allowance."}, nil
	}
	used := granted - balance
	if used < 0 {
		used = 0
	}

	resetAt := payload.RefreshAt
	if resetAt != "" {
		// Normalise to UTC RFC3339 so the dashboard's relative-time renderer
		// reads one shape across providers.
		if t, err := time.Parse(time.RFC3339, resetAt); err == nil {
			resetAt = t.UTC().Format(time.RFC3339)
		}
	}

	return Result{
		Quotas: map[string]Quota{
			"daily_theos": {
				Used:        used,
				Total:       granted,
				ResetAt:     resetAt,
				Recurring:   true,
				Unit:        "Theos",
				DisplayName: "Daily Theos",
			},
		},
	}, nil
}
