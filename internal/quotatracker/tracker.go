// Package quotatracker fetches live quota/credit balances from provider billing
// APIs and normalises them into one shape the dashboard can render.
//
// Ported from upstream 9Router's open-sse/services/usage.js, which dispatches on
// provider id to a per-provider handler and returns:
//
//	{ plan: string, quotas: { "<label>": { used, total, resetAt, unlimited, recurring } } }
//
// The shape is kept deliberately close to upstream so the two dashboards stay
// comparable, and so a provider handler can be ported one at a time.
package quotatracker

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Quota is one credit/token allowance for a connection.
type Quota struct {
	Used      float64 `json:"used"`
	Total     float64 `json:"total"`
	ResetAt   string  `json:"resetAt,omitempty"`
	Unlimited bool    `json:"unlimited"`
	// Recurring distinguishes an allowance that refills (the resetAt is the next
	// refresh) from one-shot credits (the resetAt is the final expiry). The UI
	// must say "Resets in" for the first and "Expires in" for the second:
	// implying a refill that will not happen is the failure mode this guards.
	Recurring bool `json:"recurring"`
	// Unit is the provider's own unit ("credit", "tokens", "requests") so the UI
	// does not have to guess what the numbers mean.
	Unit string `json:"unit,omitempty"`
}

// Result is the outcome for one connection. Exactly one of Message or Quotas is
// meaningful: Message carries a human-readable reason there is nothing to show
// (unsupported provider, expired credential), which is a normal outcome rather
// than an error.
type Result struct {
	Plan    string           `json:"plan,omitempty"`
	Quotas  map[string]Quota `json:"quotas,omitempty"`
	Message string           `json:"message,omitempty"`
}

// Credentials is everything a handler may need to authenticate a quota call.
type Credentials struct {
	Provider     string
	AccessToken  string
	APIKey       string
	StaticHeader map[string]string
	// UsageURL is the provider's quota endpoint, read from the provider
	// registry. Carried per call rather than in a package variable: two
	// connections of different providers are fetched concurrently, and a shared
	// mutable endpoint would send one provider's request to another's host.
	UsageURL string
}

// Handler fetches quota for one provider.
type Handler func(ctx context.Context, client *http.Client, creds Credentials) (Result, error)

// registry maps a provider id to its handler. Adding a provider is one entry
// plus its handler; providers absent here report "not implemented" rather than
// being probed speculatively, which would send auth headers to unknown hosts.
var registry = map[string]Handler{
	"codebuddy-cn":   fetchCodeBuddy,
	"codebuddy-intl": fetchCodeBuddy,
	"workbuddy":      fetchCodeBuddy,
}

// Supported reports whether a provider has a quota handler.
func Supported(provider string) bool {
	_, ok := registry[provider]
	return ok
}

// Fetch returns the quota for a provider connection.
func Fetch(ctx context.Context, client *http.Client, creds Credentials) (Result, error) {
	handler, ok := registry[creds.Provider]
	if !ok {
		return Result{Message: fmt.Sprintf("Quota API not implemented for %s.", creds.Provider)}, nil
	}
	if creds.UsageURL == "" {
		return Result{Message: fmt.Sprintf("No quota endpoint configured for %s.", creds.Provider)}, nil
	}
	return handler(ctx, client, creds)
}

// ---------------------------------------------------------------------------
// CodeBuddy / WorkBuddy (shared Tencent billing payload)
// ---------------------------------------------------------------------------

// codeBuddyPayload mirrors the nested Tencent billing response. The numbers are
// wrapped twice (data.Response.Data) and every balance is present in both an
// exact string and a rounded integer; the exact one is preferred.
type codeBuddyPayload struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Response struct {
			Data struct {
				Accounts []codeBuddyAccount `json:"Accounts"`
			} `json:"Data"`
		} `json:"Response"`
	} `json:"data"`
}

type codeBuddyAccount struct {
	PackageName   string `json:"PackageName"`
	SubProduct    string `json:"SubProductName"`
	CapacityUnit  string `json:"CapacityUnit"`
	CycleStartRaw string `json:"CycleStartTime"`
	CycleEndRaw   string `json:"CycleEndTime"`
	// DeductionEndTime arrives as a Unix millisecond number.
	DeductionEndTime int64 `json:"DeductionEndTime"`

	// The *Precise fields are JSON STRINGS on the wire ("218.93999993"), while
	// the plain ones are numbers. jsontext.Value holds either without a custom
	// unmarshaller, and flexNum decodes it.
	CycleCapacityUsedPrecise jsontext.Value `json:"CycleCapacityUsedPrecise"`
	CycleCapacityUsed        float64        `json:"CycleCapacityUsed"`
	CycleCapacitySizePrecise jsontext.Value `json:"CycleCapacitySizePrecise"`
	CycleCapacitySize        float64        `json:"CycleCapacitySize"`

	CapacityUsedPrecise jsontext.Value `json:"CapacityUsedPrecise"`
	CapacityUsed        float64        `json:"CapacityUsed"`
	CapacitySizePrecise jsontext.Value `json:"CapacitySizePrecise"`
	CapacitySize        float64        `json:"CapacitySize"`
}

// num prefers the exact value and falls back to the rounded integer, mirroring
// upstream's num(precise, plain).
//
// The precise fields arrive as JSON strings ("218.93999993") while the plain
// ones are numbers, so the target is jsontext.Value and the quoted form is
// stripped before parsing. Declaring the field as float64 — the obvious
// reading — makes every real response fail to unmarshal.
func num(precise jsontext.Value, plain float64) float64 {
	if f, ok := flexNum(precise); ok {
		return f
	}
	return plain
}

// flexNum decodes a JSON value that may be either a number or a quoted number.
func flexNum(v jsontext.Value) (float64, bool) {
	raw := bytes.TrimSpace(v)
	if len(raw) == 0 {
		return 0, false
	}
	raw = bytes.Trim(raw, `"`)
	if len(raw) == 0 {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func fetchCodeBuddy(ctx context.Context, client *http.Client, creds Credentials) (Result, error) {
	token := creds.AccessToken
	if token == "" {
		token = creds.APIKey
	}
	if token == "" {
		return Result{Message: "Credential not available for quota lookup."}, nil
	}

	body, status, err := postJSON(ctx, client, creds, token)
	if err != nil {
		return Result{}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return Result{Message: "Credential invalid or expired."}, nil
	}
	if status != http.StatusOK {
		return Result{Message: fmt.Sprintf("Quota API error (%d).", status)}, nil
	}

	var payload codeBuddyPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return Result{}, fmt.Errorf("parse quota response: %w", err)
	}
	if payload.Code != 0 {
		return Result{Message: fmt.Sprintf("Quota error: %s", payload.Msg)}, nil
	}

	accounts := payload.Data.Response.Data.Accounts
	if len(accounts) == 0 {
		return Result{Message: "Connected. No credit package found."}, nil
	}

	// Refill packs roll into a new cycle long before the resource expires
	// (CycleEndTime << DeductionEndTime); bonus packs end exactly at expiry.
	// The gap is what separates "resets monthly" from "expires once".
	const refillGap = 48 * time.Hour
	isRefill := func(a codeBuddyAccount) bool {
		cycleEnd, ok := parseProviderTime(a.CycleEndRaw)
		if !ok || a.DeductionEndTime == 0 {
			return false
		}
		deductionEnd := time.UnixMilli(a.DeductionEndTime)
		return deductionEnd.Sub(cycleEnd) > refillGap
	}

	var refills, bonuses []codeBuddyAccount
	for _, a := range accounts {
		if isRefill(a) {
			refills = append(refills, a)
		} else {
			bonuses = append(bonuses, a)
		}
	}
	byExpiry := func(list []codeBuddyAccount) {
		sort.SliceStable(list, func(i, j int) bool {
			ei, _ := parseProviderTime(list[i].CycleEndRaw)
			ej, _ := parseProviderTime(list[j].CycleEndRaw)
			return ei.Before(ej)
		})
	}
	byExpiry(refills)
	byExpiry(bonuses)

	quotas := make(map[string]Quota, len(accounts))
	// Refill packs first, labelled by cycle length so "Monthly" reads as a
	// recurring allowance rather than a one-off.
	seen := map[string]int{}
	for _, a := range refills {
		base := refillCadence(a.CycleStartRaw, a.CycleEndRaw)
		seen[base]++
		name := base
		if seen[base] > 1 {
			name = fmt.Sprintf("%s %d", base, seen[base])
		}
		reset, _ := parseProviderTime(a.CycleEndRaw)
		quotas[name] = Quota{
			Used:      num(a.CycleCapacityUsedPrecise, a.CycleCapacityUsed),
			Total:     num(a.CycleCapacitySizePrecise, a.CycleCapacitySize),
			ResetAt:   reset.UTC().Format(time.RFC3339),
			Recurring: true,
			Unit:      a.CapacityUnit,
		}
	}
	for i, a := range bonuses {
		reset, _ := parseProviderTime(a.CycleEndRaw)
		quotas[fmt.Sprintf("Bonus Pack %d", i+1)] = Quota{
			Used:      num(a.CapacityUsedPrecise, a.CapacityUsed),
			Total:     num(a.CapacitySizePrecise, a.CapacitySize),
			ResetAt:   reset.UTC().Format(time.RFC3339),
			Recurring: false,
			Unit:      a.CapacityUnit,
		}
	}

	plan := "CodeBuddy"
	if len(refills) > 0 && refills[0].PackageName != "" {
		plan = refills[0].PackageName
	} else if accounts[0].PackageName != "" {
		plan = accounts[0].PackageName
	}

	return Result{Plan: plan, Quotas: quotas}, nil
}

// refillCadence labels a refill pack by how long its cycle runs. Tencent bills
// mostly monthly, so anything long or unparseable is "Monthly".
func refillCadence(startRaw, endRaw string) string {
	start, okStart := parseProviderTime(startRaw)
	end, okEnd := parseProviderTime(endRaw)
	if !okStart || !okEnd {
		return "Monthly"
	}
	switch days := end.Sub(start).Hours() / 24; {
	case days <= 1.5:
		return "Daily"
	case days <= 10:
		return "Weekly"
	default:
		return "Monthly"
	}
}

// parseProviderTime accepts the two shapes Tencent uses for a timestamp:
// "2026-09-25 00:53:49" (no zone, UTC+8) and a Unix millisecond number.
func parseProviderTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	// Tencent timestamps are in the provider's own zone; UTC+8 is fixed and has
	// no DST, so a FixedZone is exact rather than an approximation.
	loc := time.FixedZone("UTC+8", 8*3600)
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t, true
		}
	}
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if ms < 1e12 { // seconds, not milliseconds
			return time.Unix(ms, 0), true
		}
		return time.UnixMilli(ms), true
	}
	return time.Time{}, false
}

// postJSON performs the quota POST. The provider's static headers are reused so
// the request carries the same client fingerprint the chat route does; Tencent
// rejects the billing call without them.
func postJSON(ctx context.Context, client *http.Client, creds Credentials, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, creds.UsageURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, 0, fmt.Errorf("create quota request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range creds.StaticHeader {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("quota request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read quota response: %w", err)
	}
	return body, resp.StatusCode, nil
}
