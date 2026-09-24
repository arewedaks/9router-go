package quotatracker

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

// antigravityServer stands in for Google's two quota RPCs. It records which
// endpoints were called so a test can assert that the tier gate actually
// skipped the per-model call for a free account.
type antigravityServer struct {
	*httptest.Server
	modelsCalled       bool
	subscriptionStatus int
	modelsStatus       int
	weeklyStatus       int
	paidTier           string
	projectID          string
	models             map[string]any
	lastUserAgent      string
	weekly             string
}

func newAntigravityServer(t *testing.T) *antigravityServer {
	t.Helper()
	as := &antigravityServer{
		subscriptionStatus: http.StatusOK,
		modelsStatus:       http.StatusOK,
		weeklyStatus:       http.StatusOK,
		paidTier:           "premium",
		projectID:          "proj-123",
	}
	as.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			w.WriteHeader(as.subscriptionStatus)
			if as.subscriptionStatus != http.StatusOK {
				return
			}
			payload := map[string]any{}
			if as.projectID != "" {
				payload["cloudaicompanionProject"] = as.projectID
			}
			if as.paidTier != "" {
				payload["paidTier"] = map[string]any{"id": as.paidTier}
			}
			_ = json.MarshalWrite(w, payload)

		case strings.HasSuffix(r.URL.Path, ":fetchAvailableModels"):
			as.modelsCalled = true
			as.lastUserAgent = r.Header.Get("User-Agent")
			w.WriteHeader(as.modelsStatus)
			if as.modelsStatus != http.StatusOK {
				return
			}
			_ = json.MarshalWrite(w, map[string]any{"models": as.models})

		case strings.HasSuffix(r.URL.Path, ":retrieveUserQuotaSummary"):
			w.WriteHeader(as.weeklyStatus)
			if as.weeklyStatus != http.StatusOK {
				return
			}
			_, _ = w.Write([]byte(as.weekly))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(as.Close)
	return as
}

func (as *antigravityServer) creds() Credentials {
	return Credentials{
		Provider:        "antigravity",
		AccessToken:     "token",
		ProjectID:       "proj-123",
		UsageURL:        as.URL + "/v1internal:retrieveUserQuotaSummary",
		SubscriptionURL: as.URL + "/v1internal:loadCodeAssist",
	}
}

// quotaFor builds a per-model entry with the fraction the API reports.
func quotaFor(fraction float64) map[string]any {
	return map[string]any{
		"displayName": "Model",
		"quotaInfo": map[string]any{
			"remainingFraction": fraction,
			"resetTime":         "2099-01-01T00:00:00Z",
		},
	}
}

// A paid account gets per-model rows plus the family buckets.
func TestAntigravityPaidTierReportsModelsAndBuckets(t *testing.T) {
	as := newAntigravityServer(t)
	as.models = map[string]any{
		"gemini-3.8-flash-high": quotaFor(0.75),
		"claude-sonnet-4-6":     quotaFor(0.5),
		// Callable but absent from the retired hardcoded allow-list: the bug was
		// that this model's allowance was hidden. It must be reported now.
		"gemini-3.8-flash-tiered": quotaFor(0.25),
		"gemini-3.1-flash-lite":   quotaFor(0.6),
		// Non-chat surfaces and internal slots stay out.
		"gemini-3.1-flash-image":  quotaFor(0.9),
		"tab_flash_lite_preview":  quotaFor(0.9),
		"chat_20706":              quotaFor(0.9),
		"gemini-3.6-flash-high":   quotaFor(0.9), // retired id
		"gemini-3.8-flash-medium": map[string]any{"isInternal": true, "quotaInfo": map[string]any{"remainingFraction": 0.9}},
		"gemini-3.8-flash-low":    map[string]any{"displayName": "no quota info"},
	}
	as.weekly = `{"groups":[{"displayName":"Gemini Models","buckets":[
		{"bucketId":"gemini-weekly","displayName":"Gemini Weekly","remainingFraction":0.9,"resetTime":"2099-01-01T00:00:00Z"}
	]}]}`

	res, err := fetchAntigravity(context.Background(), as.Client(), as.creds())
	if err != nil {
		t.Fatalf("fetchAntigravity: %v", err)
	}
	if res.Plan != "premium" {
		t.Errorf("plan = %q, want premium", res.Plan)
	}
	if !as.modelsCalled {
		t.Error("a paid account must query per-model quotas")
	}

	hi, ok := res.Quotas["gemini-3.8-flash-high"]
	if !ok {
		t.Fatalf("missing per-model quota; got %v", keysOf(res.Quotas))
	}
	// 0.75 of the 1000 base.
	if hi.Used != 250 || hi.Total != 1000 {
		t.Errorf("gemini-3.8-flash-high = used %v total %v, want 250/1000", hi.Used, hi.Total)
	}
	if _, ok := res.Quotas["claude-sonnet-4-6"]; !ok {
		t.Error("claude model missing")
	}
	// Every callable model is reported, whether or not it appears in the picker's
	// static catalogue: a hardcoded allow-list here had gone stale and hid seven
	// models the operator could actually call.
	for _, m := range []string{"gemini-3.8-flash-tiered", "gemini-3.1-flash-lite"} {
		if _, ok := res.Quotas[m]; !ok {
			t.Errorf("callable model %q was hidden", m)
		}
	}
	// Non-chat surfaces, retired ids, internal slots and entries without quota
	// data stay out.
	for _, m := range []string{
		"gemini-3.1-flash-image",  // image surface
		"tab_flash_lite_preview",  // tab preview
		"chat_20706",              // internal slot
		"gemini-3.6-flash-high",   // retired
		"gemini-3.8-flash-medium", // isInternal
		"gemini-3.8-flash-low",    // no quotaInfo
	} {
		if _, ok := res.Quotas[m]; ok {
			t.Errorf("%q must not be reported", m)
		}
	}
	if _, ok := res.Quotas["gemini_weekly"]; !ok {
		t.Error("weekly bucket missing")
	}
}

// A free account has no 5h window. Its per-model numbers are misleading — a
// missing fraction reads as 0 — so they must not be fetched at all, and the
// weekly bucket is the whole answer.
func TestAntigravityFreeTierSkipsModelQuotas(t *testing.T) {
	as := newAntigravityServer(t)
	as.paidTier = "free-tier"
	as.models = map[string]any{"gemini-3.8-flash-high": quotaFor(0)}
	as.weekly = `{"groups":[{"displayName":"Gemini Models","buckets":[
		{"bucketId":"gemini-weekly","displayName":"Gemini Weekly","remainingFraction":0.6,"resetTime":"2099-01-01T00:00:00Z"}
	]}]}`

	res, err := fetchAntigravity(context.Background(), as.Client(), as.creds())
	if err != nil {
		t.Fatalf("fetchAntigravity: %v", err)
	}
	if res.Plan != "Free" {
		t.Errorf("plan = %q, want Free", res.Plan)
	}
	if as.modelsCalled {
		t.Error("a free-tier account must not query per-model quotas; they report a 5h window that does not exist")
	}
	if _, ok := res.Quotas["gemini-3.8-flash-high"]; ok {
		t.Error("free-tier per-model quotas leaked into the result")
	}
	if _, ok := res.Quotas["gemini_weekly"]; !ok {
		t.Error("free tier must still report its weekly bucket")
	}
}

// An account whose tier cannot be read is treated as free: the conservative
// reading is the one that does not invent a 5h window.
func TestAntigravityUnknownTierTreatedAsFree(t *testing.T) {
	as := newAntigravityServer(t)
	as.paidTier = ""
	as.weekly = `{"groups":[{"displayName":"Gemini Models","buckets":[
		{"bucketId":"gemini-weekly","displayName":"Gemini Weekly","remainingFraction":0.5}
	]}]}`

	res, err := fetchAntigravity(context.Background(), as.Client(), as.creds())
	if err != nil {
		t.Fatalf("fetchAntigravity: %v", err)
	}
	if as.modelsCalled {
		t.Error("an unreadable tier must not be assumed paid")
	}
	if len(res.Quotas) == 0 {
		t.Error("the weekly bucket should still be reported")
	}
}

// 401/403 are reported as messages with no quotas, not as errors: the chat path
// still works with an expired quota credential.
func TestAntigravityAuthFailuresReportMessage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{
		{"forbidden", http.StatusForbidden, "forbidden"},
		{"unauthorized", http.StatusUnauthorized, "authentication expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as := newAntigravityServer(t)
			as.modelsStatus = tc.status

			res, err := fetchAntigravity(context.Background(), as.Client(), as.creds())
			if err != nil {
				t.Fatalf("fetchAntigravity: %v", err)
			}
			if len(res.Quotas) != 0 {
				t.Errorf("expected no quotas, got %v", res.Quotas)
			}
			if !strings.Contains(strings.ToLower(res.Message), tc.want) {
				t.Errorf("message = %q, want it to mention %q", res.Message, tc.want)
			}
		})
	}
}

// A failing weekly overlay must not discard the per-model rows that answered.
func TestAntigravityWeeklyFailureKeepsModelRows(t *testing.T) {
	as := newAntigravityServer(t)
	as.models = map[string]any{"gemini-3.8-flash-high": quotaFor(0.75)}
	as.weeklyStatus = http.StatusInternalServerError

	res, err := fetchAntigravity(context.Background(), as.Client(), as.creds())
	if err != nil {
		t.Fatalf("fetchAntigravity: %v", err)
	}
	if _, ok := res.Quotas["gemini-3.8-flash-high"]; !ok {
		t.Errorf("a failed weekly overlay dropped the model rows: %v", keysOf(res.Quotas))
	}
}

// The weekly and session buckets parse into stable keys with human labels.
func TestParseAntigravityBuckets(t *testing.T) {
	raw := []byte(`{"groups":[
		{"displayName":"Gemini Models","buckets":[
			{"bucketId":"gemini-weekly","displayName":"Gemini Weekly","remainingFraction":0.8,"resetTime":"2099-01-01T00:00:00Z"},
			{"bucketId":"gemini-5h","displayName":"Gemini Five Hour","window":"5h","remainingFraction":0.4,"resetTime":"2099-01-01T05:00:00Z"}
		]},
		{"displayName":"Claude and GPT Models","buckets":[
			{"bucketId":"cg-weekly","displayName":"Claude GPT Weekly","remainingFraction":0.6},
			{"bucketId":"cg-5h","displayName":"Claude GPT 5h","window":"5h","remainingFraction":0.2}
		]}
	]}`)

	got := parseAntigravityBuckets(raw)
	for _, key := range []string{"gemini_weekly", "gemini_session", "claude_gpt_weekly", "claude_gpt_session"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing %q; got %v", key, keysOf(got))
		}
	}
	if q := got["gemini_weekly"]; q.Used != 200 {
		t.Errorf("gemini_weekly used = %v, want 200 (0.8 remaining of 1000)", q.Used)
	}
	if q := got["gemini_session"]; q.DisplayName != "Gemini (5h)" {
		t.Errorf("gemini_session label = %q, want a human label", q.DisplayName)
	}
	if q := got["gemini_session"]; q.ResetAt == "" {
		t.Error("gemini_session lost its reset time")
	}
}

// A disabled session bucket is pinned to 0 rather than dropped: the backend
// marks it disabled once the weekly is spent, and hiding the row removes the
// explanation for a blocked account. A disabled weekly is genuinely off.
func TestParseAntigravityBucketsDisabledHandling(t *testing.T) {
	raw := []byte(`{"groups":[{"displayName":"Gemini Models","buckets":[
		{"bucketId":"gemini-5h","displayName":"Gemini 5h","window":"5h","disabled":true,"remainingFraction":0.9},
		{"bucketId":"gemini-weekly","displayName":"Gemini Weekly","disabled":true,"remainingFraction":0.9}
	]}]}`)

	got := parseAntigravityBuckets(raw)
	session, ok := got["gemini_session"]
	if !ok {
		t.Fatalf("a disabled session bucket must still surface; got %v", keysOf(got))
	}
	if session.Used != 1000 {
		t.Errorf("disabled session used = %v, want it pinned to the full total", session.Used)
	}
	if _, ok := got["gemini_weekly"]; ok {
		t.Error("a disabled weekly bucket is genuinely off and must be skipped")
	}
}

// Upstream's reconciliation: when every model in a family is spent, the family's
// 5h row is marked spent too, because the summary can still report it as
// partially full. The weekly row must be left alone.
func TestReconcileAntigravitySessionRows(t *testing.T) {
	future := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)

	quotas := map[string]Quota{
		"gemini-3.8-flash-high":  {Used: 1000, Total: 1000, ResetAt: future},
		"gemini-3.8-flash-low":   {Used: 1000, Total: 1000, ResetAt: future},
		"gemini-3.1-flash-image": {Used: 0, Total: 1000},
		"claude-sonnet-4-6":      {Used: 100, Total: 1000},
	}
	weekly := map[string]Quota{
		"gemini_session":     {Used: 0, Total: 1000},
		"gemini_weekly":      {Used: 200, Total: 1000},
		"claude_gpt_session": {Used: 0, Total: 1000},
	}

	reconcileAntigravitySessionRows(quotas, weekly)

	if weekly["gemini_session"].Used != 1000 {
		t.Errorf("gemini_session used = %v, want it marked spent", weekly["gemini_session"].Used)
	}
	if weekly["gemini_session"].ResetAt != future {
		t.Errorf("gemini_session reset = %q, want the latest model reset", weekly["gemini_session"].ResetAt)
	}
	// The image model is excluded from the "all models spent" test, so it must
	// not have dragged the result either way.
	if weekly["claude_gpt_session"].Used != 0 {
		t.Errorf("claude_gpt_session used = %v, want it untouched (its models are not spent)", weekly["claude_gpt_session"].Used)
	}
	// A spent 5h window says nothing about the weekly allowance.
	if weekly["gemini_weekly"].Used != 200 {
		t.Errorf("gemini_weekly used = %v, want it untouched", weekly["gemini_weekly"].Used)
	}
}

// An empty family must not be read as "everything spent": with no models to
// judge, the window is unknown, not exhausted.
func TestReconcileAntigravitySessionRowsEmptyFamily(t *testing.T) {
	weekly := map[string]Quota{"gemini_session": {Used: 0, Total: 1000}}
	reconcileAntigravitySessionRows(map[string]Quota{"claude-sonnet-4-6": {Used: 0, Total: 1000}}, weekly)
	if weekly["gemini_session"].Used != 0 {
		t.Errorf("an empty gemini family marked the session spent: %v", weekly["gemini_session"].Used)
	}
}

// The subscription lookup's project id is used when the connection does not
// carry one, and a per-model 403 is surfaced as a message.
func TestAntigravityProjectFallbackAndForbidden(t *testing.T) {
	as := newAntigravityServer(t)
	as.models = map[string]any{"gemini-3.8-flash-high": quotaFor(0.5)}

	creds := as.creds()
	creds.ProjectID = "" // force the loadCodeAssist fallback
	res, err := fetchAntigravity(context.Background(), as.Client(), creds)
	if err != nil {
		t.Fatalf("fetchAntigravity: %v", err)
	}
	if _, ok := res.Quotas["gemini-3.8-flash-high"]; !ok {
		t.Errorf("per-model quotas missing after a project fallback: %v", keysOf(res.Quotas))
	}
}

func TestQuotaHostFromUsageURL(t *testing.T) {
	if got := quotaHostFromUsageURL("https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"); got != "https://cloudcode-pa.googleapis.com" {
		t.Errorf("host = %q", got)
	}
	if got := quotaHostFromUsageURL("https://example.test/no-rpc-path"); got != "" {
		t.Errorf("expected an empty host for a URL with no RPC path, got %q", got)
	}
}

// An unparseable reset must become empty rather than a zero time the UI would
// render as 1970.
func TestNormalizeResetTime(t *testing.T) {
	if got := normalizeResetTime(""); got != "" {
		t.Errorf("empty input = %q, want empty", got)
	}
	if got := normalizeResetTime("not-a-time"); got != "" {
		t.Errorf("unparseable input = %q, want empty", got)
	}
	if got := normalizeResetTime("2099-01-01T00:00:00Z"); got != "2099-01-01T00:00:00Z" {
		t.Errorf("valid input = %q", got)
	}
}

func keysOf(m map[string]Quota) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestAntigravityQuotaLabelsAreDeterministic guards the alias disambiguation.
// The API reuses one displayName for gemini-3.1-pro-high and gemini-pro-agent;
// the suffix that tells them apart must land on the alias every time, not on
// whichever id a randomised map walk happened to visit first.
func TestAntigravityQuotaLabelsAreDeterministic(t *testing.T) {
	for i := 0; i < 20; i++ {
		as := newAntigravityServer(t)
		as.models = map[string]any{
			"gemini-3.1-pro-high": map[string]any{"displayName": "Gemini 3.1 Pro (High)", "quotaInfo": quotaFor(0.5)},
			"gemini-pro-agent":    map[string]any{"displayName": "Gemini 3.1 Pro (High)", "quotaInfo": quotaFor(0.5)},
		}
		res, err := fetchAntigravity(context.Background(), as.Client(), as.creds())
		if err != nil {
			t.Fatalf("fetchAntigravity: %v", err)
		}
		// The canonical id keeps the bare label; the alias carries the suffix.
		if got := res.Quotas["gemini-3.1-pro-high"].DisplayName; got != "Gemini 3.1 Pro (High)" {
			t.Fatalf("iteration %d: canonical label = %q", i, got)
		}
		if got := res.Quotas["gemini-pro-agent"].DisplayName; got != "Gemini 3.1 Pro (High) · gemini-pro-agent" {
			t.Fatalf("iteration %d: alias label = %q", i, got)
		}
	}
}

// TestAntigravityHumanizesTieredModelIDs covers ids the API ships with no
// displayName: printing the raw id next to labelled rows read as unparsed.
func TestAntigravityHumanizesTieredModelIDs(t *testing.T) {
	as := newAntigravityServer(t)
	// No displayName key at all, which is what the API returns for tiered ids.
	as.models = map[string]any{
		"gemini-3.8-flash-tiered": map[string]any{
			"quotaInfo": map[string]any{"remainingFraction": 1.0, "resetTime": "2099-01-01T00:00:00Z"},
		},
	}
	res, err := fetchAntigravity(context.Background(), as.Client(), as.creds())
	if err != nil {
		t.Fatalf("fetchAntigravity: %v", err)
	}
	if got := res.Quotas["gemini-3.8-flash-tiered"].DisplayName; got != "Gemini 3.8 Flash Tiered" {
		t.Errorf("DisplayName = %q, want %q", got, "Gemini 3.8 Flash Tiered")
	}
}

// TestAntigravityQuotaSendsProfileUserAgent guards the fingerprint the quota RPC
// is sent with. The backend keys the catalogue on this header: the IDE profile
// answers 33 models, the CLI profile 27, and a malformed string answers the CLI
// set. The handler used to send "antigravity/1.0.0" regardless of the configured
// profile, so an IDE connection reported six fewer models than the picker.
func TestAntigravityQuotaSendsProfileUserAgent(t *testing.T) {
	cases := []struct {
		profile string
		want    string
	}{
		{"ide", "antigravity/ide/2.11.0 darwin/arm64"},
		{"cli", "antigravity/cli/1.1.5 (aidev_client; os_type=darwin; arch=arm64; auth_method=consumer)"},
		// An unset profile is the IDE default, not the malformed legacy string.
		{"", "antigravity/ide/2.11.0 darwin/arm64"},
		// An unknown value normalises to ide rather than reaching the wire raw.
		{"bogus", "antigravity/ide/2.11.0 darwin/arm64"},
	}
	for _, c := range cases {
		as := newAntigravityServer(t)
		as.models = map[string]any{"gemini-3.8-flash-tiered": quotaFor(1)}
		creds := as.creds()
		creds.ClientProfile = c.profile
		if _, err := fetchAntigravity(context.Background(), as.Client(), creds); err != nil {
			t.Fatalf("profile %q: fetchAntigravity: %v", c.profile, err)
		}
		if as.lastUserAgent != c.want {
			t.Errorf("profile %q: User-Agent = %q, want %q", c.profile, as.lastUserAgent, c.want)
		}
	}
}

// TestAntigravityProbesDailyHostFirst guards the host order.
//
// The two Cloud Code hosts disagree: daily reflects consumption, cloudcode-pa
// keeps answering "full" for the Gemini family after the 5-hour window is spent.
// Reading cloudcode-pa first (or alone) is what made the panel report a spent
// allowance as untouched, so the order is a correctness property, not a
// preference.
func TestAntigravityProbesDailyHostFirst(t *testing.T) {
	if len(providers.AntigravityQuotaHosts) < 2 {
		t.Fatal("expected both Cloud Code hosts")
	}
	if providers.AntigravityQuotaHosts[0] != "https://daily-cloudcode-pa.googleapis.com" {
		t.Errorf("first host = %q, want daily-cloudcode-pa", providers.AntigravityQuotaHosts[0])
	}

	// A configured built-in host selects the canonical list, not itself first:
	// ordering the stale host ahead of daily would reintroduce the bug.
	creds := Credentials{UsageURL: "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"}
	got := antigravityQuotaHostsFor(creds)
	if len(got) != len(providers.AntigravityQuotaHosts) || got[0] != providers.AntigravityQuotaHosts[0] {
		t.Errorf("configured built-in host produced %v, want the canonical list", got)
	}

	// A host outside the list is used alone: credentials must not be sent to
	// Google behind an operator's local proxy.
	got = antigravityQuotaHostsFor(Credentials{UsageURL: "http://127.0.0.1:9999/v1internal:retrieveUserQuotaSummary"})
	if len(got) != 1 || got[0] != "http://127.0.0.1:9999" {
		t.Errorf("custom host produced %v, want only the configured host", got)
	}
}
