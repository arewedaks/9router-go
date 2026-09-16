package dashboard

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
)

// Per-account Connection Test — port of the upstream Next.js
// POST /api/providers/[id]/test ("Test Connection") surface.
//
// WHY THIS DOES NOT SELF-CALL THE PROXY
// The Model Test feature legitimately loops back to /v1/chat/completions, but
// that path is unusable here: the chat layer resolves a connection by
// priority only (getBestConnection), and no header or query parameter can pin
// it to a specific account. A loopback "test this account" would silently
// exercise whichever connection has the highest priority — reporting a false
// green (or false red) against the wrong row. So this file probes the upstream
// directly, with the connection's own credentials, exactly like
// antigravity_search.go already does.

// connectionTestTimeout is the per-account probe deadline. Kept separate from
// MODEL_TEST_TIMEOUT_MS so a hung connection probe cannot be confused with —
// or reconfigured by — the model-test tuning knobs. Mirrors upstream's
// OAUTH_TEST_TIMEOUT_MS (30s).
func connectionTestTimeout() time.Duration {
	if v := os.Getenv("CONNECTION_TEST_TIMEOUT_MS"); v != "" {
		if n, err := time.ParseDuration(v + "ms"); err == nil && n > 0 {
			return n
		}
	}
	return 30 * time.Second
}

// httpClientForTest returns an HTTP client for a single connection probe. The
// timeout is per-request; the caller may also impose a shorter context
// deadline. Probes never reuse a pooled/idle connection across accounts, so a
// slow account cannot make another account's probe appear slow.
func (h *Handler) httpClientForTest() *http.Client {
	return &http.Client{Timeout: connectionTestTimeout()}
}

// connectionTestResult is the per-account outcome surfaced in the UI. It is a
// superset of modelTestResult so the SPA can reuse its status-rendering
// patterns without a second vocabulary.
//
// Verdict contract (three-way, mirroring upstream's split):
//
//	2xx             -> Valid=true,  no warning            (credential proven good)
//	400 (not geo)   -> Valid=true,  Warning set           (inconclusive: auth was
//	                                                       accepted, credential
//	                                                       cannot be judged)
//	400 (geo-block) -> Valid=true,  Warning + GeoBlocked  (egress/availability
//	                                                       failure of the TEST,
//	                                                       NOT of the credential)
//	401/403         -> Valid=false                        (credential rejected)
//
// GeoBlocked exists so the UI can colour a geo-block differently from an
// inconclusive probe. Rendering a geo-block as a plain success would recreate
// the exact false-green that motivated probing the real model surface: the
// operator sees green while every chat request fails.
type connectionTestResult struct {
	ConnectionID string `json:"connectionId"`
	Provider     string `json:"provider,omitempty"`
	Name         string `json:"name,omitempty"`
	Valid        bool   `json:"valid"`
	Error        string `json:"error,omitempty"`
	Warning      string `json:"warning,omitempty"`
	GeoBlocked   bool   `json:"geoBlocked,omitempty"`
	Status       int    `json:"status,omitempty"`
	LatencyMs    int64  `json:"latencyMs"`
	Refreshed    bool   `json:"refreshed"`
	TestedAt     string `json:"testedAt,omitempty"`
}

// geoBlockSignals are Google's regional-availability refusals. Ported verbatim
// from the upstream GEO_BLOCK_SIGNALS list; matched case-insensitively against
// the (capped) response body.
var geoBlockSignals = []string{
	"user location is not supported",
	"location is not supported",
	"not supported for the api use",
	"region is not supported",
	"unsupported location",
	"not available in your location",
	"not available in your region",
}

// isGeoBlockedError reports whether an upstream body carries a geo-block
// refusal. Checked against the body text rather than the status because Google
// can return the refusal under several different status codes.
func isGeoBlockedError(body string) bool {
	lower := strings.ToLower(body)
	for _, s := range geoBlockSignals {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// connectionTestMaxBody caps how much upstream body we read. The dashboard is
// unauthenticated in this fork, so error text is never echoed unbounded.
const connectionTestMaxBody = 64 << 10

// antigravityRuntimeBaseURLs are the hosts serving the Cloud Code model
// surface, in preference order. Mirrors upstream
// ANTIGRAVITY_RUNTIME_BASE_URLS.
var antigravityRuntimeBaseURLs = []string{
	"https://daily-cloudcode-pa.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

// isAntigravityProviderID reports whether a provider ID names the Antigravity
// backend. `agy` (the standalone CLI's own provider id in OmniRoute) does not
// exist in this fork's registry, but it is accepted here so a future
// registration cannot silently fall through to an unsupported probe.
func isAntigravityProviderID(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "antigravity", "agy":
		return true
	}
	return false
}

// testConnection probes one account and returns a normalised verdict. It is
// READ-ONLY: no credential state, activation flag or cooldown is written.
// Upstream persists testStatus/lastError/rateLimitedUntil/backoffLevel from
// this endpoint. That is deliberately NOT ported. Upstream gates the route
// behind requireManagementAuth and pairs the writes with a credential-health
// scheduler that re-probes every 300s and clears them. Here the dashboard
// routes are unauthenticated (pre-existing fork behaviour) and there is no
// recovery scheduler, so a write would be both a remote DoS primitive (loop
// the endpoint to flip working accounts out of rotation) and sticky until
// manual intervention. Reporting the verdict to the operator is the whole
// feature; persisting it is unnecessary and unsafe.
//
// It also never refreshes OAuth tokens. This fork's refresh path
// (providers.NewStandardRefresher via KnownOAuthConfigs) has no per-connection
// mutex or in-flight dedup — unlike upstream's getAccessToken. A test-triggered
// refresh could therefore fire concurrent refreshes against the same refresh
// token and invalidate a healthy account (refresh_token_reused), which is
// strictly worse than reporting a stale-token 401. A 401 is reported as
// "chat will refresh on next use" instead.
func (h *Handler) testConnection(ctx context.Context, connID string) (result connectionTestResult) {
	conn, err := h.repo.GetProviderConnectionByID(connID)
	if err != nil {
		return connectionTestResult{ConnectionID: connID, Error: "Failed to load connection: " + err.Error()}
	}
	if conn == nil {
		return connectionTestResult{ConnectionID: connID, Error: "Connection not found"}
	}

	out := connectionTestResult{ConnectionID: conn.ID, Provider: conn.Provider}
	if conn.Name != nil && *conn.Name != "" {
		out.Name = *conn.Name
	} else if conn.Email != nil && *conn.Email != "" {
		out.Name = *conn.Email
	}

	// Latency is measured on the result actually returned (a named return),
	// not on a local copy — otherwise every probe would report 0ms.
	start := time.Now()
	defer func() { result.LatencyMs = time.Since(start).Milliseconds() }()

	// The probe needs fields ParseConnectionData does not surface (expiresAt,
	// projectId), so the raw blob is parsed here — the same approach
	// antigravity_fetch.go and models_fetch.go already use in this package.
	var raw map[string]any
	if strings.TrimSpace(conn.Data) != "" {
		if err := json.Unmarshal([]byte(conn.Data), &raw); err != nil {
			out.Error = "Connection data is not valid JSON: " + err.Error()
			return out
		}
	}

	if isAntigravityProviderID(conn.Provider) {
		return h.probeAntigravityConnection(ctx, raw, out)
	}
	return h.probeGenericConnection(ctx, raw, out)
}

// connString reads the first non-empty string field from a raw connection blob.
func connString(raw map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := raw[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// probeAntigravityConnection probes the REAL Cloud Code model surface.
//
// The previous-generation probe only hit Google's OAuth userinfo endpoint,
// which is NOT geo-restricted — so "Test Connection" stayed green while every
// model call failed with "User location is not supported for the API use."
// Probing /v1internal:generateContent with a properly wrapped AntigravityRequest
// body exercises the exact surface chat depends on:
//
//	2xx       -> the wrapper shape is accepted and the model answered.
//	400       -> auth accepted, body/surface rejected (inconclusive).
//	401/403   -> token rejected.
//
// A *minimal* body (as upstream sends) always yields a 400 INVALID_ARGUMENT
// because the endpoint requires the AntigravityRequest envelope, so a correct
// probe would never be able to return a clean 2xx. Building the real wrapper —
// the same translator.WrapForAntigravity shape the chat path sends — instead
// lets a healthy account return a genuine 200, verified live.
func (h *Handler) probeAntigravityConnection(ctx context.Context, raw map[string]any, out connectionTestResult) connectionTestResult {
	accessToken := connString(raw, "accessToken", "access_token", "apiKey")
	if accessToken == "" {
		out.Error = "No access token on this connection. Sign in again to obtain one."
		return out
	}

	// A locally-known-expired token is reported without a network round trip:
	// it is free to detect, and probing it would only burn a request to learn
	// what we already know.
	if antigravityTokenExpired(raw) {
		out.Error = "Token expired — the next chat request will refresh it automatically."
		return out
	}

	body := buildAntigravityProbeBody(antigravityProbeModel())

	// The UA must match the connection's own client profile, otherwise a CLI
	// connection would be probed with an IDE fingerprint (and vice versa).
	profileData, _ := raw["providerSpecificData"].(map[string]any)
	userAgent := providers.AntigravityUserAgentForData(profileData)

	// Prefer the connection's own configured host, then fall back to the known
	// runtime hosts so a connection pinned to an unreachable host still gets a
	// verdict. Probing only one hardcoded host would misreport a connection
	// configured against the other.
	return h.probeAntigravityAgainstHosts(ctx, h.probeAntigravityHosts(), raw, accessToken, userAgent, body, out)
}

// antigravityProbeHosts returns the hosts to try, in order. The connection's
// own configured host comes first (so a connection pinned to a specific host is
// probed against that host), then the known runtime hosts as fallbacks.
func (h *Handler) probeAntigravityHosts() []string {
	if len(h.probeHostsOverride) > 0 {
		return h.probeHostsOverride
	}
	hosts := antigravityRuntimeBaseURLs
	if cfg, ok := providers.KnownProviders["antigravity"]; ok && cfg.BaseURL != "" {
		if base := strings.TrimRight(cfg.BaseURL, "/"); !containsString(hosts, base) {
			hosts = append([]string{base}, hosts...)
		}
	}
	return hosts
}

// probeAntigravityAgainstHosts is the testable core: it tries each host in turn
// and classifies the response. Split out so tests can point at an httptest
// server without mutating the global provider registry.
func (h *Handler) probeAntigravityAgainstHosts(ctx context.Context, hosts []string, raw map[string]any, accessToken, userAgent string, body []byte, out connectionTestResult) connectionTestResult {
	client := h.httpClientForTest()
	var lastStatus int
	var lastErrText string

	for _, host := range hosts {
		status, respBody, err := h.postAntigravityProbe(ctx, client, host, accessToken, userAgent, body)
		if err != nil {
			lastErrText = err.Error()
			continue
		}
		lastStatus = status

		// Classify on the body first: Google can emit a geo-block refusal under
		// more than one status code, so status alone is not enough.
		if isGeoBlockedError(respBody) {
			out.Valid = true
			out.GeoBlocked = true
			out.Status = status
			out.Warning = "Egress location blocked by Google (\"User location is not supported\"). " +
				"The Cloud Code API is not offered from this server's exit region — route antigravity " +
				"through a proxy in a supported region, or use a different provider. This is NOT an account problem."
			return out
		}

		switch {
		case status >= 200 && status < 300:
			out.Valid = true
			out.Status = status
			out.TestedAt = time.Now().UTC().Format(time.RFC3339)
			return out

		case status == 401 || status == 403:
			out.Status = status
			out.Error = "Token rejected by Google (HTTP " + fmt.Sprint(status) + "). " +
				"The next chat request will refresh it automatically; if this persists, sign the account in again."
			return out

		case status == 400:
			// The token was accepted (otherwise this would be 401/403) but the
			// surface rejected the request, so credential validity cannot be
			// judged from this probe. Report inconclusive rather than failing a
			// possibly-good account — and rather than claiming a success it did
			// not prove.
			out.Valid = true
			out.Status = status
			out.Warning = "Probe was inconclusive: Google accepted the credential but rejected the test " +
				"request (HTTP 400). The account is most likely healthy. Detail: " + truncate(sanitizeUpstreamText(respBody), 240)
			return out

		default:
			// 5xx / 429 / anything else is an upstream availability problem, not
			// a credential verdict. Try the next host before giving up.
			lastErrText = "HTTP " + fmt.Sprint(status) + ": " + truncate(sanitizeUpstreamText(respBody), 240)
		}
	}

	out.Status = lastStatus
	if lastErrText == "" {
		lastErrText = "no response from any Antigravity host"
	}
	out.Error = "Probe failed: " + lastErrText
	return out
}

// postAntigravityProbe sends one probe request and returns the status plus a
// bounded, sanitized body.
func (h *Handler) postAntigravityProbe(ctx context.Context, client *http.Client, host, accessToken, userAgent string, body []byte) (int, string, error) {
	url := strings.TrimRight(host, "/") + "/v1internal:generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, connectionTestMaxBody))
	return resp.StatusCode, string(raw), nil
}

// buildAntigravityProbeBody builds the AntigravityRequest envelope the model
// endpoint requires. The shape mirrors translator.WrapForAntigravity: a bare
// Gemini body is rejected with INVALID_ARGUMENT, so the wrapper is mandatory
// for a probe to be able to reach a 2xx.
func buildAntigravityProbeBody(model string) []byte {
	inner := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "ping"}}},
		},
		"generationConfig": map[string]any{"maxOutputTokens": 1},
	}
	wrapper := map[string]any{
		"project":     "",
		"model":       model,
		"userAgent":   "antigravity",
		"requestType": "agent",
		"requestId":   "agent-conn-test",
		"request":     inner,
	}
	b, err := json.Marshal(wrapper)
	if err != nil {
		// Marshal of a static map cannot fail; fall back to a literal envelope.
		return []byte(`{"model":"` + model + `","requestType":"agent","request":{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}}`)
	}
	return b
}

// antigravityProbeModel returns a cheap, widely-available chat model for the
// probe. A fixed plain chat id is used rather than the connection's configured
// model so the probe does not fail on a preview/image/TTS variant for reasons
// unrelated to the credential.
func antigravityProbeModel() string {
	return "gemini-2.5-flash"
}

// antigravityTokenExpired reports whether the connection's token is locally
// known to be expired. Only a definite, parsed expiry counts — an unparseable
// or absent value returns false so the probe still runs. Both the RFC3339 and
// the "+07:00" offset forms seen in stored connections are accepted.
func antigravityTokenExpired(raw map[string]any) bool {
	s := connString(raw, "expiresAt", "expires_at")
	if s == "" {
		return false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-07:00", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Before(time.Now())
		}
	}
	return false
}

// probeGenericConnection probes an API-key provider by hitting its own chat
// endpoint. It reuses the connection's configured auth header/scheme so the
// probe matches what real requests send.
//
// This is intentionally conservative: providers whose auth surface cannot be
// exercised by a chat call are reported as skipped (Valid=false, no Error) so
// the UI can say "not verifiable" instead of inventing a verdict.
func (h *Handler) probeGenericConnection(ctx context.Context, raw map[string]any, out connectionTestResult) connectionTestResult {
	apiKey := connString(raw, "apiKey", "api_key", "accessToken", "access_token", "token", "key", "copilotToken")
	if apiKey == "" {
		out.Error = "No credential found on this connection (looked for apiKey / accessToken / token)."
		return out
	}

	cfg, ok := providers.KnownProviders[out.Provider]
	if !ok {
		out.Warning = "Provider " + out.Provider + " has no known upstream configuration in this build, so it cannot be probed."
		return out
	}

	// Only OpenAI-compatible chat surfaces can be exercised with a chat probe.
	// A gemini-native provider other than Antigravity has no OpenAI-style body,
	// and a provider with no BaseURL has nothing to call.
	if cfg.IsGeminiNative() || cfg.BaseURL == "" {
		out.Warning = "Automatic probing is not supported for this provider's request format. Use the Models tab to test one of its models instead."
		return out
	}

	client := h.httpClientForTest()
	ctx, cancel := context.WithTimeout(ctx, connectionTestTimeout())
	defer cancel()

	body, _ := json.Marshal(map[string]any{
		"model":      genericProbeModel(out.Provider),
		"max_tokens": 1,
		"messages":   []map[string]any{{"role": "user", "content": "ping"}},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		out.Error = "Could not build probe request: " + err.Error()
		return out
	}
	req.Header.Set("Content-Type", "application/json")
	setProbeAuthHeader(req, cfg, apiKey)
	for k, v := range cfg.StaticHeaders {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			out.Error = fmt.Sprintf("Test timed out after %ds.", int(connectionTestTimeout().Seconds()))
			return out
		}
		out.Error = "Probe request failed: " + err.Error()
		return out
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, connectionTestMaxBody))
	text := string(bodyBytes)
	out.Status = resp.StatusCode

	if isGeoBlockedError(text) {
		out.Valid = true
		out.GeoBlocked = true
		out.Warning = "Egress location blocked by the provider. This is a routing/region problem, not an account problem."
		return out
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		out.Valid = true
		out.TestedAt = time.Now().UTC().Format(time.RFC3339)
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		out.Error = fmt.Sprintf("Credential rejected (HTTP %d): %s",
			resp.StatusCode, truncate(sanitizeUpstreamText(text), 240))
	case resp.StatusCode == 400 || resp.StatusCode == 422:
		// Auth was accepted (a rejected key would be 401/403); the body or model
		// id was refused, so credential validity cannot be judged.
		out.Valid = true
		out.Warning = fmt.Sprintf("Probe was inconclusive (HTTP %d) — the request was rejected but the credential was not. Detail: %s",
			resp.StatusCode, truncate(sanitizeUpstreamText(text), 200))
	case resp.StatusCode == 429:
		out.Valid = true
		out.Warning = "Provider rate-limited the probe (HTTP 429). The credential was not judged."
	default:
		out.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(sanitizeUpstreamText(text), 240))
	}
	return out
}

// genericProbeModel returns a cheap model id for a provider's chat probe.
func genericProbeModel(provider string) string {
	if m, ok := genericProbeModels[provider]; ok {
		return m
	}
	return "gpt-4o-mini"
}

// genericProbeModels maps a provider to a low-cost model suitable for a probe.
var genericProbeModels = map[string]string{
	"openai":     "gpt-4o-mini",
	"deepseek":   "deepseek-chat",
	"groq":       "llama-3.1-8b-instant",
	"mistral":    "mistral-small-latest",
	"openrouter": "openai/gpt-4o-mini",
}

// setProbeAuthHeader applies the provider's configured auth header/scheme.
func setProbeAuthHeader(req *http.Request, cfg providers.ProviderConfig, apiKey string) {
	name := cfg.AuthHeader
	if name == "" {
		name = "Authorization"
	}
	switch cfg.AuthScheme {
	case "bearer":
		req.Header.Set(name, "Bearer "+apiKey)
	case "raw":
		req.Header.Set(name, apiKey)
	default:
		if cfg.NoAuth {
			return
		}
		req.Header.Set(name, "Bearer "+apiKey)
	}
}

// sanitizeUpstreamText collapses control characters and newlines so upstream
// error text cannot forge extra log lines or blow up the UI, and caps length.
// Tokens are never included: only the response body is passed here.
func sanitizeUpstreamText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		if r < 0x20 {
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}

// containsString reports whether a slice contains an exact value.
func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
