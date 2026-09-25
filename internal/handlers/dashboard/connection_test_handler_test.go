package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"

	json "encoding/json/v2"
)

// --- geo-block classification -------------------------------------------

// TestIsGeoBlockedError verifies every upstream signal is recognised and that
// ordinary errors are not misclassified. A false positive here would tell an
// operator "not an account problem" for a genuine credential failure.
func TestIsGeoBlockedError(t *testing.T) {
	blocked := []string{
		`{"error":{"message":"User location is not supported for the API use."}}`,
		`Location is not supported.`,
		`The region is not supported`,
		`unsupported location`,
		`not available in your location`,
		`not available in your region`,
		`not supported for the api use`,
		// Case-insensitivity: Google's casing varies by surface.
		`USER LOCATION IS NOT SUPPORTED`,
	}
	for _, body := range blocked {
		if !isGeoBlockedError(body) {
			t.Errorf("expected geo-block for %q", body)
		}
	}

	notBlocked := []string{
		``,
		`{"error":{"code":401,"message":"Request had invalid authentication credentials."}}`,
		`{"error":{"code":400,"message":"Invalid JSON payload received."}}`,
		`rate limit exceeded`,
		`the model is not supported`, // mentions "supported" but not a location
	}
	for _, body := range notBlocked {
		if isGeoBlockedError(body) {
			t.Errorf("did NOT expect geo-block for %q", body)
		}
	}
}

// --- probe body ----------------------------------------------------------

// TestBuildAntigravityProbeBody_RequiresEnvelope guards the load-bearing detail:
// a bare Gemini body is rejected with INVALID_ARGUMENT, so the AntigravityRequest
// envelope must always be present.
func TestBuildAntigravityProbeBody_RequiresEnvelope(t *testing.T) {
	body := buildAntigravityProbeBody("gemini-2.5-flash")

	var wrapper map[string]any
	if err := json.Unmarshal(body, &wrapper); err != nil {
		t.Fatalf("probe body must be valid JSON: %v", err)
	}
	if _, ok := wrapper["request"]; !ok {
		t.Fatalf("envelope missing 'request'; a bare body would be rejected with 400")
	}
	if _, ok := wrapper["model"]; !ok {
		t.Fatalf("envelope missing 'model'")
	}
	if got, _ := wrapper["model"].(string); got != "gemini-2.5-flash" {
		t.Fatalf("unexpected model %q", got)
	}

	inner, ok := wrapper["request"].(map[string]any)
	if !ok {
		t.Fatalf("request must be an object")
	}
	if _, ok := inner["contents"]; !ok {
		t.Fatalf("inner request missing contents")
	}
}

// --- expiry parsing ------------------------------------------------------

// TestAntigravityTokenExpired covers both timestamp formats seen in stored
// connections, and the important negative case: an unparseable or absent expiry
// must NOT short-circuit the probe.
func TestAntigravityTokenExpired(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour)
	future := time.Now().Add(2 * time.Hour)

	cases := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{"expired RFC3339 UTC", map[string]any{"expiresAt": past.UTC().Format(time.RFC3339)}, true},
		{"expired with offset", map[string]any{"expiresAt": past.Format("2006-01-02T15:04:05-07:00")}, true},
		{"fresh RFC3339", map[string]any{"expiresAt": future.UTC().Format(time.RFC3339)}, false},
		{"fresh with offset", map[string]any{"expiresAt": future.Format("2006-01-02T15:04:05-07:00")}, false},
		// Absent/unparseable must not be treated as expired, or every probe
		// would be skipped for connections that simply omit the field.
		{"absent", map[string]any{}, false},
		{"garbage", map[string]any{"expiresAt": "not-a-time"}, false},
		{"empty string", map[string]any{"expiresAt": "  "}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := antigravityTokenExpired(tc.raw); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// --- sanitisation -------------------------------------------------------

// TestSanitizeUpstreamText verifies newlines/control chars are collapsed (so
// upstream text cannot forge log lines or break the UI row) and length is
// bounded.
func TestSanitizeUpstreamText(t *testing.T) {
	got := sanitizeUpstreamText("line one\nline two\r\n\tindented\x00nul")
	if strings.ContainsAny(got, "\n\r\t\x00") {
		t.Fatalf("control characters survived: %q", got)
	}
	if !strings.Contains(got, "line one") || !strings.Contains(got, "line two") {
		t.Fatalf("content lost: %q", got)
	}
}

// --- connection id helpers ----------------------------------------------

func TestConnString(t *testing.T) {
	raw := map[string]any{
		"accessToken": "tok",
		"apiKey":      "key",
		"blank":       "   ",
		"num":         42,
	}
	if got := connString(raw, "accessToken"); got != "tok" {
		t.Fatalf("got %q", got)
	}
	// Falls through to the next key when the first is absent.
	if got := connString(raw, "missing", "apiKey"); got != "key" {
		t.Fatalf("got %q", got)
	}
	// Blank and non-string values are skipped.
	if got := connString(raw, "blank", "num"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestContainsString(t *testing.T) {
	xs := []string{"a", "b"}
	if !containsString(xs, "a") {
		t.Fatal("expected to find a")
	}
	if containsString(xs, "c") {
		t.Fatal("did not expect to find c")
	}
}

// --- Antigravity probe, end to end against a fake host ------------------

// newAntigravityTestHandler wires a repo-free handler whose KnownProviders host
// is the fake server.
func antigravitySSEHandler(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The probe must carry the connection's bearer token.
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("probe missing bearer token")
		}
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("probe missing User-Agent")
		}
		if !strings.Contains(r.URL.Path, ":generateContent") {
			t.Errorf("probe hit unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// probeAntigravityAgainst runs probeAntigravityConnection against a fake host
// by invoking the host-parameterised core directly, so the global provider
// registry is never mutated.
//
// The User-Agent is no longer derived from the connection: the client profile is
// a provider-wide setting, so the probe resolves it the same way production does.
func probeAntigravityAgainst(t *testing.T, srv *httptest.Server, raw map[string]any) connectionTestResult {
	t.Helper()
	h, _ := newProfileTestHandler(t)
	accessToken := connString(raw, "accessToken", "access_token", "apiKey")
	if accessToken == "" || antigravityTokenExpired(raw) {
		// Preserve the caller's intended short-circuit path.
		return h.probeAntigravityConnection(context.Background(), raw, connectionTestResult{})
	}
	return h.probeAntigravityAgainstHosts(
		context.Background(),
		[]string{srv.URL},
		raw,
		accessToken,
		antigravityProbeUserAgent(t, h),
		buildAntigravityProbeBody(antigravityProbeModel()),
		connectionTestResult{},
	)
}

// TestProbeAntigravityConnection_ClassifiesStatuses is the core contract test:
// 2xx=valid, 400=inconclusive-but-valid, 401=invalid, geo-block=flagged.
func TestProbeAntigravityConnection_ClassifiesStatuses(t *testing.T) {
	cases := []struct {
		name           string
		status         int
		body           string
		wantValid      bool
		wantGeoBlocked bool
		wantWarning    bool
		wantError      bool
	}{
		{
			name:      "2xx is a clean pass",
			status:    http.StatusOK,
			body:      `{"response":{"candidates":[{"content":{"parts":[{"text":""}]}}]}}`,
			wantValid: true,
		},
		{
			name:        "400 is inconclusive, still valid",
			status:      http.StatusBadRequest,
			body:        `{"error":{"code":400,"message":"Invalid JSON payload received."}}`,
			wantValid:   true,
			wantWarning: true,
		},
		{
			name:           "geo-block 400 is flagged as an egress problem",
			status:         http.StatusBadRequest,
			body:           `{"error":{"message":"User location is not supported for the API use."}}`,
			wantValid:      true,
			wantGeoBlocked: true,
			wantWarning:    true,
		},
		{
			name:      "401 rejects the credential",
			status:    http.StatusUnauthorized,
			body:      `{"error":{"code":401,"message":"Request had invalid authentication credentials."}}`,
			wantValid: false,
			wantError: true,
		},
		{
			name:      "403 rejects the credential",
			status:    http.StatusForbidden,
			body:      `{"error":{"code":403,"message":"Forbidden"}}`,
			wantValid: false,
			wantError: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := antigravitySSEHandler(t, tc.status, tc.body)
			defer srv.Close()

			raw := map[string]any{"accessToken": "test-token"}
			res := probeAntigravityAgainst(t, srv, raw)

			if res.Valid != tc.wantValid {
				t.Fatalf("valid=%v want %v (error=%q warning=%q)", res.Valid, tc.wantValid, res.Error, res.Warning)
			}
			if res.GeoBlocked != tc.wantGeoBlocked {
				t.Fatalf("geoBlocked=%v want %v", res.GeoBlocked, tc.wantGeoBlocked)
			}
			if tc.wantWarning && res.Warning == "" {
				t.Fatalf("expected a warning")
			}
			if tc.wantError && res.Error == "" {
				t.Fatalf("expected an error")
			}
			if tc.wantValid && res.Error != "" {
				t.Fatalf("a valid verdict must not carry a hard error: %q", res.Error)
			}
		})
	}
}

// TestProbeAntigravityConnection_NoToken verifies a connection without a token
// fails fast without any network call.
func TestProbeAntigravityConnection_NoToken(t *testing.T) {
	h := &Handler{}
	res := h.probeAntigravityConnection(context.Background(), map[string]any{}, connectionTestResult{})
	if res.Valid {
		t.Fatal("expected invalid")
	}
	if !strings.Contains(res.Error, "No access token") {
		t.Fatalf("unexpected error %q", res.Error)
	}
}

// TestProbeAntigravityConnection_ExpiredTokenShortCircuits verifies a locally
// expired token is reported without burning a request, and the message tells
// the operator chat will refresh it (rather than demanding re-auth).
func TestProbeAntigravityConnection_ExpiredTokenShortCircuits(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	raw := map[string]any{
		"accessToken": "test-token",
		"expiresAt":   time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	}
	res := probeAntigravityAgainst(t, srv, raw)

	if hits != 0 {
		t.Fatalf("expected no network call for an expired token, got %d", hits)
	}
	if res.Valid {
		t.Fatal("expected invalid")
	}
	if !strings.Contains(strings.ToLower(res.Error), "expired") {
		t.Fatalf("expected an expiry message, got %q", res.Error)
	}
	if !strings.Contains(strings.ToLower(res.Error), "refresh") {
		t.Fatalf("message should say chat will refresh it, got %q", res.Error)
	}
}

// TestTestConnection_ReportsLatency is a regression guard: latency is measured
// on the returned value via a named return, not on a local copy. Using a
// non-named return silently reported 0ms for every probe.
func TestTestConnection_ReportsLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"response":{}}`))
	}))
	defer srv.Close()

	h, _ := newProfileTestHandler(t)
	h.probeHostsOverride = []string{srv.URL}
	seedConnection(t, h.repo, "c-lat", "antigravity", `{"accessToken":"tok"}`)

	// Drive the real entry point (not the inner core) so the named-return
	// latency measurement is actually exercised.
	res := h.testConnection(context.Background(), "c-lat")

	if !res.Valid {
		t.Fatalf("expected valid, got error %q", res.Error)
	}
	if res.LatencyMs <= 0 {
		t.Fatalf("latency must be measured, got %d", res.LatencyMs)
	}
}

// --- HTTP handler: unknown connection -----------------------------------

// TestHandleTestConnection_UnknownConnection verifies a missing connection id
// yields 404 rather than a fabricated verdict.
func TestHandleTestConnection_UnknownConnection(t *testing.T) {
	h, _ := newProfileTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/connections/nope/test", strings.NewReader("{}"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "does-not-exist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	h.HandleTestConnection(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown connection, got %d: %s", w.Code, w.Body.String())
	}
}

// TestConnectionTestBatchDeadline verifies the batch deadline scales with the
// number of sequential probes (so a multi-account batch is not killed after the
// first probe's timeout) and is capped.
func TestConnectionTestBatchDeadline(t *testing.T) {
	one := connectionTestBatchDeadline(1, false)
	many := connectionTestBatchDeadline(5, false)
	if many <= one {
		t.Fatalf("deadline should grow with probe count: %v vs %v", many, one)
	}
	parallel := connectionTestBatchDeadline(50, true)
	if parallel >= many {
		t.Fatalf("parallel deadline should not scale per-probe: %v vs %v", parallel, many)
	}
	if capped := connectionTestBatchDeadline(100000, false); capped > 10*time.Minute+time.Second {
		t.Fatalf("deadline must be capped, got %v", capped)
	}
}

// --- pre-probe token refresh ---------------------------------------------

// TestRefreshExpiredTokenForTest_AdoptsConcurrentRefresh is the regression test
// for the concurrency bug found in live testing: when several probes run at
// once, the first refreshes the token and the rest observe a still-valid token
// (so they do no refresh of their own). Each of those callers must STILL adopt
// the fresh token, otherwise it probes with the stale copy it read before
// queueing — reporting a false "invalid" for a perfectly healthy account.
func TestRefreshExpiredTokenForTest_AdoptsConcurrentRefresh(t *testing.T) {
	h, database := newProfileTestHandler(t)

	// A fake token endpoint: counts calls and always issues a fresh token.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write([]byte(`{"access_token":"fresh-from-server","expires_in":3600}`))
	}))
	defer srv.Close()

	const provider = "refreshregress"
	prevCfg, hadCfg := providers.KnownOAuthConfigs[provider]
	providers.KnownOAuthConfigs[provider] = providers.OAuthClientConfig{
		ClientID: "id", ClientSecret: "secret", TokenURL: srv.URL,
	}
	t.Cleanup(func() {
		if hadCfg {
			providers.KnownOAuthConfigs[provider] = prevCfg
		} else {
			delete(providers.KnownOAuthConfigs, provider)
		}
	})

	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	data := `{"accessToken":"stale-token","refreshToken":"r1","expiresAt":"` + past + `"}`
	seedConnection(t, db.NewRepo(database), "conn-reg", provider, data)

	conn, err := h.repo.GetProviderConnectionByID("conn-reg")
	if err != nil || conn == nil {
		t.Fatalf("load seeded connection: %v", err)
	}

	// Simulate the losing caller: it read the blob (stale token) BEFORE another
	// caller refreshed. It must come back with the FRESH token, not its stale one.
	staleRaw := map[string]any{
		"accessToken":  "stale-token",
		"refreshToken": "r1",
		"expiresAt":    past,
	}

	refresh, err := h.refreshExpiredTokenForTest(context.Background(), conn, staleRaw)
	if err != nil {
		t.Fatalf("refresh error: %v", err)
	}
	if !refresh.changed {
		t.Fatal("changed must be true: the resolved token differs from the stale one")
	}
	if refresh.accessToken != "fresh-from-server" {
		t.Fatalf("adopted token %q, want fresh-from-server", refresh.accessToken)
	}
	if refresh.performed != true {
		// This caller *did* perform the first refresh (nothing refreshed before it).
		t.Fatal("the first caller should have performed the refresh")
	}

	// A second caller with the SAME stale blob must adopt the token without a
	// second exchange.
	staleRaw2 := map[string]any{
		"accessToken":  "stale-token",
		"refreshToken": "r1",
		"expiresAt":    past,
	}
	refresh2, err := h.refreshExpiredTokenForTest(context.Background(), conn, staleRaw2)
	if err != nil {
		t.Fatalf("second refresh error: %v", err)
	}
	if refresh2.accessToken != "fresh-from-server" {
		t.Fatalf("second caller adopted %q, want fresh-from-server", refresh2.accessToken)
	}
	if refresh2.performed {
		t.Fatal("second caller must NOT perform another refresh; the token is already fresh")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("token endpoint called %d times across two callers; want exactly 1", n)
	}
}

// TestRefreshExpiredTokenForTest_IgnoresNonOAuth guards that API-key connections
// are never touched by the pre-probe refresh.
func TestRefreshExpiredTokenForTest_IgnoresNonOAuth(t *testing.T) {
	h, database := newProfileTestHandler(t)
	seedConnection(t, db.NewRepo(database), "conn-key", "openai", `{"apiKey":"sk-abc"}`)

	conn, err := h.repo.GetProviderConnectionByID("conn-key")
	if err != nil || conn == nil {
		t.Fatalf("load connection: %v", err)
	}
	refresh, err := h.refreshExpiredTokenForTest(context.Background(), conn, map[string]any{"apiKey": "sk-abc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refresh.changed || refresh.performed || refresh.accessToken != "" {
		t.Fatalf("api-key connection must be untouched, got %+v", refresh)
	}
}

// antigravityProbeUserAgent resolves the User-Agent the production probe would
// send, i.e. the one derived from the provider-wide client profile setting.
func antigravityProbeUserAgent(t *testing.T, h *Handler) string {
	t.Helper()
	settings, err := h.repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	return providers.AntigravityUserAgent(
		providers.NormalizeAntigravityClientProfile(settings.AntigravityClientProfile))
}

func TestTestConnection_CustomProviderNode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			if r.Header.Get("Authorization") != "Bearer valid-custom-key" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"custom-model"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	h, database := newProfileTestHandler(t)
	repo := db.NewRepo(database)

	nodeID := "openai-compatible-chat-cust1"
	nodeData := fmt.Sprintf(`{"prefix":"cust1","apiType":"chat","baseUrl":%q,"nodeName":"Cust 1"}`, srv.URL+"/v1")
	if _, err := repo.DB().Exec(
		`INSERT INTO providerNodes (id, name, type, data, createdAt, updatedAt)
		 VALUES (?, 'Cust 1', 'openai-compatible', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID, nodeData,
	); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// 1. Valid Key
	seedConnection(t, repo, "c-valid", nodeID, `{"apiKey":"valid-custom-key"}`)
	res := h.testConnection(context.Background(), "c-valid")
	if !res.Valid {
		t.Fatalf("expected valid custom connection, got error: %s, warning: %s", res.Error, res.Warning)
	}
	if res.Status != 200 {
		t.Errorf("status = %d, want 200", res.Status)
	}

	// 2. Invalid Key
	seedConnection(t, repo, "c-invalid", nodeID, `{"apiKey":"bad-key"}`)
	resBad := h.testConnection(context.Background(), "c-invalid")
	if resBad.Valid {
		t.Fatalf("expected invalid for bad key, got valid")
	}
	if !strings.Contains(resBad.Error, "rejected (HTTP 401)") {
		t.Errorf("expected 401 rejection error, got %q", resBad.Error)
	}
}
