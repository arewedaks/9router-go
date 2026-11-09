package quotatracker

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// clixadWallet is Clixad's /v1/wallet response — a metering gateway, not a
// per-model quota. From clixad 0.0.1-beta.50 (bundle dist/chunk-*.mjs):
//
//	GET /v1/wallet
//	{ balance: 125400, free: { daily_credits: 6000, credits_left: 4100,
//	                           models: ["gpt-5-nano", ...], turns_left: {...},
//	                           recommended: "gpt-5-nano" },
//	  premium: { unlocked: false, credits: 0, unlock_usd: 3 }, ... }
//
// Credits are ad-fundable (offer-wall surveys / purchases); the free tier is a
// daily credit grant for budget models, refilled at midnight UTC. The response
// shape is defensive: any unknown field is ignored and missing pieces are
// reported only if the ones that exist make sense.
type clixadWallet struct {
	Balance float64 `json:"balance"`
	Free    *struct {
		DailyCredits int64            `json:"daily_credits"`
		CreditsLeft  int64            `json:"credits_left"`
		Models       []string         `json:"models"`
		TurnsLeft    map[string]int64 `json:"turns_left"`
		Recommended  string           `json:"recommended"`
	} `json:"free"`
	Premium *struct {
		Unlocked  bool    `json:"unlocked"`
		Credits   int64   `json:"credits"`
		UnlockUSD float64 `json:"unlock_usd"`
	} `json:"premium"`
}

// fetchClixad reports one connection's Clixad wallet.
//
// Clixad's billing IS the quota: a free daily grant (budget models) plus a
// credit balance earned from rewarded offers and spending it on model calls.
// This handler exposes both:
//   - "daily_free": the free-tier daily credit grant, as a recurring allowance
//   - "credits": the current balance (LimitOnly — the balance is a point in
//     time, not a consumable total this handler can divide)
//
// UsageURL carries the wallet URL (https://clixad.onrender.com/v1/wallet).
// A 401 means the stored token no longer validates, which the panel reports
// as a message rather than a quota.
func fetchClixad(ctx context.Context, client *http.Client, creds Credentials) (Result, error) {
	token := creds.AccessToken
	if token == "" {
		token = creds.APIKey
	}
	if token == "" {
		return Result{Message: "Credential not available for quota lookup."}, nil
	}

	url := strings.TrimSpace(creds.UsageURL)
	if url == "" {
		url = "https://clixad.onrender.com/v1/wallet"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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

	var payload clixadWallet
	if err := json.Unmarshal(body, &payload); err != nil {
		return Result{}, fmt.Errorf("parse quota response: %w", err)
	}

	quotas := map[string]Quota{}
	if payload.Balance > 0 {
		quotas["credits"] = Quota{
			Used:        0,
			Total:       payload.Balance,
			LimitOnly:   true,
			Unit:        "credits",
			DisplayName: "Credit balance",
		}
	} else if payload.Balance == 0 {
		quotas["credits"] = Quota{
			Used:        0,
			Total:       0,
			Unit:        "credits",
			DisplayName: "Credit balance",
		}
	}
	if payload.Free != nil && payload.Free.DailyCredits > 0 && payload.Free.CreditsLeft >= 0 {
		used := float64(payload.Free.DailyCredits - payload.Free.CreditsLeft)
		if used < 0 {
			used = 0
		}
		quotas["daily_free"] = Quota{
			Used:        used,
			Total:       float64(payload.Free.DailyCredits),
			Recurring:   true,
			ResetAt:     nextMidnightUTC(),
			Unit:        "credits",
			DisplayName: "Daily free credits",
		}
	}
	if len(quotas) == 0 {
		return Result{Message: "Clixad wallet returned no measurable balance."}, nil
	}
	return Result{Quotas: quotas}, nil
}

// nextMidnightUTC is when the free daily grant refills (the CLI says midnight
// UTC) — a safe, documented boundary rather than one sniffed from the wire.
func nextMidnightUTC() string {
	now := time.Now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	return next.Format(time.RFC3339)
}
