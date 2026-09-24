package quotatracker

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

// Port of upstream's open-sse/services/usage/google.js getAntigravityUsage().
//
// Antigravity exposes two different allowance shapes and the account's tier
// decides which one is real:
//
//   - Paid: per-model remaining fractions (a 5h window per model) PLUS weekly
//     and 5h buckets for each model family.
//   - Free: only the weekly bucket. fetchAvailableModels still returns per-model
//     numbers for a free account, but they are not a 5h window — a missing
//     remainingFraction defaults to 0, so rendering them would show a wall of
//     exhausted models that are in fact usable.
//
// So the tier is read first and the per-model block is skipped for free
// accounts, which is why the subscription lookup cannot be dropped as an
// optimisation.

// antigravityQuotaEndpoint is the per-model RPC. It is not the provider's
// UsageURL: that one answers the weekly summary, and this call is a separate
// endpoint on the same host.
const antigravityQuotaEndpoint = "/v1internal:fetchAvailableModels"

// antigravitySummaryEndpoint answers the weekly + 5h family buckets.
const antigravitySummaryEndpoint = "/v1internal:retrieveUserQuotaSummary"

// antigravityQuotaHostsFor returns the hosts to probe for one connection.
//
// A configured host that is one of the built-ins selects the whole probe list
// in its canonical order — the order is the correctness property, not a
// preference: daily is the host that reflects consumption, so putting a
// configured cloudcode-pa first would reintroduce the stale reading this list
// exists to avoid.
//
// A host outside the list is used alone: an operator who pointed UsageURL at a
// local proxy meant that host, and silently sending the same credentials to
// Google would be a surprise, and would leak the request past the proxy.
func antigravityQuotaHostsFor(creds Credentials) []string {
	primary := quotaHostFromUsageURL(creds.UsageURL)
	if primary == "" {
		return providers.AntigravityQuotaHosts
	}
	for _, h := range providers.AntigravityQuotaHosts {
		if h == primary {
			return providers.AntigravityQuotaHosts
		}
	}
	return []string{primary}
}

// antigravityPostAnyHost sends one RPC to each candidate host and returns the
// first 200. A host that errors or answers non-200 falls through to the next,
// which is what makes a partial outage degrade instead of blanking the panel.
func antigravityPostAnyHost(ctx context.Context, client *http.Client, creds Credentials, path string, body []byte) (int, []byte, error) {
	var (
		status int
		raw    []byte
		err    error
	)
	for _, host := range antigravityQuotaHostsFor(creds) {
		status, raw, err = antigravityPost(ctx, client, host+path, creds, body)
		if err == nil && status == http.StatusOK {
			return status, raw, nil
		}
	}
	return status, raw, err
}

// antigravityClientName is the X-Client-Name every official client sends.
const antigravityClientName = "antigravity"

// antigravityUserAgentFor returns the fingerprint for the connection's profile.
//
// The backend keys the catalogue RPC on this header: the IDE profile answers 33
// models, the CLI profile 27, and an unparseable string ("antigravity/1.0.0",
// which is what this used to send) answers the CLI set. A quota call that
// ignored the profile therefore reported six fewer models than the picker the
// operator was looking at. The UA is built by the providers package so the two
// cannot drift.
func antigravityUserAgentFor(creds Credentials) string {
	return providers.AntigravityUserAgent(
		providers.NormalizeAntigravityClientProfile(creds.ClientProfile))
}

// antigravityQuotaTotal is the normalised base upstream uses. The API reports a
// fraction, not an amount, so the UI needs a denominator to draw a bar against.
const antigravityQuotaTotal = 1000

// antigravitySubscription is the subset of loadCodeAssist this handler needs.
type antigravitySubscription struct {
	ProjectID string
	PaidTier  string
}

// isFreeTier reports whether the account has no paid tier. An absent tier is
// treated as free: the conservative reading is the one that does not invent a 5h
// window, and a free account misread as paid shows exhausted models that work.
func (s antigravitySubscription) isFreeTier() bool {
	return s.PaidTier == "" || s.PaidTier == "free-tier"
}

// fetchAntigravity returns the live quota for one Antigravity connection.
func fetchAntigravity(ctx context.Context, client *http.Client, creds Credentials) (Result, error) {
	if creds.AccessToken == "" {
		return Result{Message: "Antigravity access token not available."}, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	sub := fetchAntigravitySubscription(ctx, client, creds)
	projectID := creds.ProjectID
	if projectID == "" {
		projectID = sub.ProjectID
	}

	quotas := map[string]Quota{}

	// Per-model quotas are a paid-tier concept (see the package comment).
	if !sub.isFreeTier() {
		models, status, err := fetchAntigravityModelQuotas(ctx, client, creds, projectID)
		switch {
		case err != nil:
			// Not fatal: the weekly overlay below can still say something useful.
			quotas = map[string]Quota{}
		case status == http.StatusForbidden:
			return Result{Message: "Antigravity quota API access forbidden. Chat may still work."}, nil
		case status == http.StatusUnauthorized:
			return Result{Message: "Antigravity quota API authentication expired. Chat may still work."}, nil
		case status != http.StatusOK:
			return Result{Message: fmt.Sprintf("Antigravity quota API error: %d.", status)}, nil
		default:
			quotas = models
		}
	}

	// Weekly + 5h overlay. Best-effort by design: a failure here must not
	// discard the per-model rows that already answered.
	weekly := fetchAntigravityWeeklyBuckets(ctx, client, creds, projectID)
	reconcileAntigravitySessionRows(quotas, weekly)
	for k, v := range weekly {
		if _, taken := quotas[k]; !taken {
			quotas[k] = v
		}
	}

	if len(quotas) == 0 {
		return Result{Plan: planLabel(sub), Message: "No quota reported for this account."}, nil
	}

	return Result{Plan: planLabel(sub), Quotas: quotas}, nil
}

func planLabel(sub antigravitySubscription) string {
	if sub.PaidTier == "" {
		return ""
	}
	if sub.isFreeTier() {
		return "Free"
	}
	return sub.PaidTier
}

// fetchAntigravitySubscription reads the project id and tier via loadCodeAssist.
// A failure returns the zero value, which isFreeTier() treats as free.
func fetchAntigravitySubscription(ctx context.Context, client *http.Client, creds Credentials) antigravitySubscription {
	url := creds.SubscriptionURL
	if url == "" {
		return antigravitySubscription{}
	}
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"pluginType": "GEMINI",
		},
	})
	if err != nil {
		return antigravitySubscription{}
	}

	status, raw, err := antigravityPost(ctx, client, url, creds, body)
	if err != nil || status != http.StatusOK {
		return antigravitySubscription{}
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return antigravitySubscription{}
	}

	sub := antigravitySubscription{}
	if pid, ok := data["cloudaicompanionProject"].(string); ok {
		sub.ProjectID = strings.TrimSpace(pid)
	}
	// paidTier is what upstream reads. currentTier is the fallback for accounts
	// whose response predates that field; taking the first non-empty keeps a
	// paid account from being misread as free.
	for _, key := range []string{"paidTier", "currentTier"} {
		if m, ok := data[key].(map[string]any); ok {
			if id, ok := m["id"].(string); ok && strings.TrimSpace(id) != "" {
				sub.PaidTier = strings.TrimSpace(id)
				break
			}
		}
	}
	return sub
}

// fetchAntigravityModelQuotas reads per-model remaining fractions.
func fetchAntigravityModelQuotas(ctx context.Context, client *http.Client, creds Credentials, projectID string) (map[string]Quota, int, error) {
	if quotaHostFromUsageURL(creds.UsageURL) == "" {
		return nil, 0, fmt.Errorf("no quota host for %s", creds.Provider)
	}

	reqBody := map[string]any{}
	if projectID != "" {
		reqBody["project"] = projectID
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, err
	}

	status, raw, err := antigravityPostAnyHost(ctx, client, creds, antigravityQuotaEndpoint, body)
	if err != nil {
		return nil, 0, err
	}
	if status != http.StatusOK {
		return nil, status, nil
	}

	var payload struct {
		Models map[string]struct {
			DisplayName string `json:"displayName"`
			IsInternal  bool   `json:"isInternal"`
			QuotaInfo   *struct {
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
			} `json:"quotaInfo"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, status, err
	}

	// Iterate in sorted id order: a map walk is randomised, and the label
	// disambiguation below depends on which of two ids is seen first, so an
	// unsorted walk would flip the suffix between identical requests.
	ids := make([]string, 0, len(payload.Models))
	for model := range payload.Models {
		ids = append(ids, model)
	}
	sort.Strings(ids)

	out := make(map[string]Quota)
	taken := make(map[string]bool)
	for _, model := range ids {
		info := payload.Models[model]
		if info.QuotaInfo == nil || info.IsInternal {
			continue
		}
		// Same filter the model picker uses, so the panel cannot report fewer
		// models than the operator can actually call. A hardcoded allow-list
		// here had gone stale and hid seven callable models.
		if !providers.IsDiscoverableAntigravityModel(model) {
			continue
		}
		// A missing fraction means "not reported", which upstream reads as 0.
		frac := 0.0
		if info.QuotaInfo.RemainingFraction != nil {
			frac = *info.QuotaInfo.RemainingFraction
		}
		remaining := float64(int(antigravityQuotaTotal*frac + 0.5))
		used := float64(antigravityQuotaTotal) - remaining
		if used < 0 {
			used = 0
		}
		name := info.DisplayName
		if name == "" {
			name = humanizeAntigravityModelID(model)
		}
		// The API reuses one displayName for several ids (gemini-3.1-pro-high
		// and gemini-pro-agent both answer "Gemini 3.1 Pro (High)"). Left alone
		// that renders as duplicate rows with different numbers, so the later
		// one is qualified with the id it actually routes to. Sorted order puts
		// the canonical id first, so the alias carries the suffix.
		if taken[name] {
			name = name + " · " + model
		}
		taken[name] = true

		out[model] = Quota{
			Used:        used,
			Total:       antigravityQuotaTotal,
			ResetAt:     normalizeResetTime(info.QuotaInfo.ResetTime),
			Recurring:   true,
			Unit:        "tokens",
			DisplayName: name,
		}
	}
	return out, status, nil
}

// fetchAntigravityWeeklyBuckets reads the weekly + 5h summary and normalises it
// into the same Quota shape.
func fetchAntigravityWeeklyBuckets(ctx context.Context, client *http.Client, creds Credentials, projectID string) map[string]Quota {
	if creds.UsageURL == "" {
		return nil
	}
	reqBody := map[string]any{}
	if projectID != "" {
		reqBody["project"] = projectID
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil
	}
	status, raw, err := antigravityPostAnyHost(ctx, client, creds, antigravitySummaryEndpoint, body)
	if err != nil || status != http.StatusOK {
		return nil
	}
	return parseAntigravityBuckets(raw)
}

// parseAntigravityBuckets classifies weekly vs 5h-session buckets into stable
// keys. Mirrors the chat path's ParseWeeklyQuotaSummary (which feeds routing) so
// the panel and the router cannot disagree about the same account.
//
// A disabled session bucket is pinned to 0: the backend marks it disabled once
// the weekly is spent, and dropping the row would hide the reason requests fail.
// A disabled weekly bucket is genuinely off and skipped.
func parseAntigravityBuckets(raw []byte) map[string]Quota {
	type bucket struct {
		BucketID          string   `json:"bucketId"`
		DisplayName       string   `json:"displayName"`
		Window            string   `json:"window"`
		Disabled          bool     `json:"disabled"`
		RemainingFraction *float64 `json:"remainingFraction"`
		ResetTime         string   `json:"resetTime"`
	}
	var payload struct {
		Groups []struct {
			DisplayName string   `json:"displayName"`
			Buckets     []bucket `json:"buckets"`
		} `json:"groups"`
		QuotaSummary *struct {
			Groups []struct {
				DisplayName string   `json:"displayName"`
				Buckets     []bucket `json:"buckets"`
			} `json:"groups"`
		} `json:"quotaSummary"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	groups := payload.Groups
	if len(groups) == 0 && payload.QuotaSummary != nil {
		groups = payload.QuotaSummary.Groups
	}

	out := make(map[string]Quota)
	for _, g := range groups {
		gName := strings.ToLower(g.DisplayName)
		isGemini := strings.Contains(gName, "gemini")
		isClaudeGPT := strings.Contains(gName, "claude") || strings.Contains(gName, "gpt")
		if !isGemini && !isClaudeGPT {
			continue
		}
		for _, b := range g.Buckets {
			window := strings.ToLower(b.Window)
			text := strings.ToLower(b.BucketID + " " + b.DisplayName)
			isWeekly := window == "weekly" || strings.Contains(text, "weekly")
			isSession := window == "5h" || window == "daily" ||
				strings.Contains(text, "five hour") ||
				strings.Contains(text, "5h") ||
				strings.Contains(text, "daily")
			if !isWeekly && !isSession {
				continue
			}
			if b.Disabled && isWeekly {
				continue
			}
			frac := 0.0
			if b.RemainingFraction != nil {
				frac = *b.RemainingFraction
			}
			if b.Disabled {
				frac = 0
			}
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
			remaining := float64(int(antigravityQuotaTotal*frac + 0.5))
			used := float64(antigravityQuotaTotal) - remaining
			if used < 0 {
				used = 0
			}

			key, label := "gemini_weekly", "Gemini (Weekly)"
			if isClaudeGPT {
				key, label = "claude_gpt_weekly", "Claude & GPT (Weekly)"
			}
			if isSession {
				if isGemini {
					key, label = "gemini_session", "Gemini (5h)"
				} else {
					key, label = "claude_gpt_session", "Claude & GPT (5h)"
				}
			}

			// First bucket per key wins, matching the router's parser.
			if _, exists := out[key]; exists {
				continue
			}
			out[key] = Quota{
				Used:        used,
				Total:       antigravityQuotaTotal,
				ResetAt:     normalizeResetTime(b.ResetTime),
				Recurring:   true,
				Unit:        "tokens",
				DisplayName: label,
			}
		}
	}
	return out
}

// reconcileAntigravitySessionRows marks a family's 5h row exhausted when every
// model in that family already reports zero.
//
// This is upstream's reconciliation, and it exists because the two sources
// disagree in one direction: the per-model view knows a model is blocked while
// the summary still reports the 5h window as partially full. Without this the
// panel shows healthy 5h rows next to a wall of exhausted models, and the
// operator has no way to tell which one explains the failure.
//
// The weekly row is deliberately NOT touched: a spent 5h window says nothing
// about the weekly allowance, and overwriting it would report an exhausted week
// that has not happened.
func reconcileAntigravitySessionRows(quotas, weekly map[string]Quota) {
	if len(quotas) == 0 || len(weekly) == 0 {
		return
	}

	allExhausted := func(prefix string, skipImage bool) bool {
		seen := false
		for k, q := range quotas {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			if skipImage && strings.Contains(k, "image") {
				continue
			}
			seen = true
			if q.Used < q.Total {
				return false
			}
		}
		// No models of this family in the response means nothing to reconcile:
		// reporting "exhausted" from an empty set would mark a healthy window
		// spent.
		return seen
	}

	markSession := func(key, prefix string, skipImage bool) {
		session, ok := weekly[key]
		if !ok {
			return
		}
		if !allExhausted(prefix, skipImage) || session.Used >= session.Total {
			return
		}
		// The reset is the latest model reset in the family: the window reopens
		// when its last blocked model does, not when the first one does.
		latest := session.ResetAt
		for k, q := range quotas {
			if !strings.HasPrefix(k, prefix) || (skipImage && strings.Contains(k, "image")) {
				continue
			}
			if q.ResetAt > latest {
				latest = q.ResetAt
			}
		}
		session.Used = session.Total
		session.ResetAt = latest
		weekly[key] = session
	}

	markSession("gemini_session", "gemini-", true)
	markSession("claude_gpt_session", "claude-", false)
}

// humanizeAntigravityModelID renders an id the API gave no displayName for.
// "gemini-3.8-flash-tiered" -> "Gemini 3.8 Flash Tiered".
func humanizeAntigravityModelID(id string) string {
	parts := strings.Split(id, "-")
	for i, part := range parts {
		if part == "" {
			continue
		}
		// Keep the version segment intact ("3.8", not "3.8" title-cased oddly)
		// and upper-case the short vendor acronyms.
		switch part {
		case "gpt", "oss", "pro", "lite", "tts":
			parts[i] = strings.ToUpper(part)
		default:
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

// antigravityPost performs an authenticated quota RPC. The client fingerprint
// headers are set here so every call carries the same identity.
func antigravityPost(ctx context.Context, client *http.Client, url string, creds Credentials, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", antigravityUserAgentFor(creds))
	req.Header.Set("X-Client-Name", antigravityClientName)
	for k, v := range creds.StaticHeader {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

// quotaHostFromUsageURL strips the RPC path off a quota URL so a sibling RPC can
// be addressed on the same host. Deriving it beats a second constant that could
// drift from the first.
func quotaHostFromUsageURL(usageURL string) string {
	i := strings.Index(usageURL, "/v1internal:")
	if i < 0 {
		return ""
	}
	return usageURL[:i]
}

// normalizeResetTime converts an RFC3339 stamp to the format the rest of the
// tracker emits. An unparseable or empty value becomes "" rather than a zero
// time, which the UI would render as 1970.
func normalizeResetTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
