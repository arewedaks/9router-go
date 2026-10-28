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

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/oauth"
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
// probeUserAgent replaces Go's default "Go-http-client/1.1", which
// Cloudflare-fronted inference hosts (Nous, and anything behind the same WAF)
// answer with a 403 HTML challenge page. Mirrors proxy.doRequestOnce and the
// models fetcher, which already override it for the same reason.
const probeUserAgent = "9router/1.0"

// setProbeUserAgent applies probeUserAgent unless the caller picked one (for
// example the provider's StaticHeaders or the antigravity IDE fingerprint).
func setProbeUserAgent(req *http.Request) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", probeUserAgent)
	}
}

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
	// Note carries information that is worth showing but does NOT downgrade the
	// verdict: a valid key with a drained wallet is still a valid key, and the
	// chip must stay green while the tooltip explains what will fail.
	Note       string `json:"note,omitempty"`
	GeoBlocked bool   `json:"geoBlocked,omitempty"`
	Status     int    `json:"status,omitempty"`
	LatencyMs  int64  `json:"latencyMs"`
	Refreshed  bool   `json:"refreshed"`
	TestedAt   string `json:"testedAt,omitempty"`
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
// READ-ONLY with respect to the connection's ROUTING state: no activation flag,
// cooldown or backoff is written. Upstream persists
// testStatus/lastError/rateLimitedUntil/backoffLevel from this endpoint. That is
// deliberately NOT ported. Upstream gates the route behind requireManagementAuth
// and pairs the writes with a credential-health scheduler that re-probes every
// 300s and clears them. Here there is no recovery scheduler, so persisting a
// FAILURE would be both a DoS primitive (loop the endpoint to flip working
// accounts out of rotation) and sticky until manual intervention.
//
// The one write that is allowed is the inverse: clearStaleOnPass drops failure
// markers after a probe proves the credential answers. It can only turn a row
// green, and it exists because otherwise the dashboard shows the same account
// two different ways at once — the row painted from a months-old stored error
// (refresh) and the chip from a live probe (Test).
//
// It DOES refresh an expired OAuth token before probing, because a red "token
// expired" verdict for an account that is one refresh away from healthy is
// worse than useless — it trains the operator to ignore the indicator. The
// refresh goes through oauth.RefreshStoredConnection, which holds a
// per-connection lock; that serialisation is what makes refreshing here safe
// (the earlier version of this file refused to refresh at all precisely because
// the chat path's refresh had no such lock and the two could race on a single-use
// refresh token, bricking a healthy account).
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

	// A locally-expired token is refreshed before probing so the operator gets a
	// real verdict instead of a stale "token expired" — the account is usually one
	// refresh away from healthy. This is a deliberate state change (the fresh token
	// is persisted), serialised by oauth.RefreshStoredConnection's per-connection
	// lock so it cannot race a live chat request.
	//
	// The freshness check happens INSIDE that lock, so when several tests run
	// concurrently only one performs the exchange; the rest observe the token the
	// first one just wrote. They must then ADOPT that token — see below — or they
	// would probe with the stale copy they read before queueing on the lock.
	//
	// A FAILED refresh is not fatal: the probe runs with the old token and will
	// report the upstream rejection accurately (401/403), which is the verdict the
	// operator needs in that case.
	if refresh, refreshErr := h.refreshExpiredTokenForTest(ctx, conn, raw); refreshErr != nil {
		log.Warn("dashboard", "connection test token refresh failed", "conn", connID, "error", refreshErr)
	} else if refresh.changed {
		out.Refreshed = refresh.performed
		// Use the token the refresher resolved (its own, or one written by a
		// concurrent caller), and re-read the blob for any other fields a refresh
		// may have updated (projectId, rotated refreshToken).
		raw["accessToken"] = refresh.accessToken
		if refresh.projectID != "" {
			raw["projectId"] = refresh.projectID
		}
		if updated, err := h.repo.GetProviderConnectionByID(connID); err == nil && updated != nil {
			conn = updated
			if strings.TrimSpace(updated.Data) != "" {
				var fresh map[string]any
				if err := json.Unmarshal([]byte(updated.Data), &fresh); err == nil {
					// Keep the resolved token authoritative: a concurrent refresh may
					// have written a newer one between our load and this re-read.
					fresh["accessToken"] = refresh.accessToken
					raw = fresh
				}
			}
		}
	}

	if isAntigravityProviderID(conn.Provider) {
		return h.probeAntigravityConnection(ctx, raw, out)
	}
	if isClineFamily(conn.Provider) {
		return h.probeClineConnection(ctx, raw, out)
	}
	if isCodebuddyProvider(conn.Provider) {
		return h.probeCodebuddyConnection(ctx, conn.ID, raw, conn.Provider, out)
	}
	if providers.ResolveAlias(strings.TrimSpace(conn.Provider)) == "openrouter" {
		return h.probeOpenrouterConnection(ctx, raw, out)
	}
	return h.probeGenericConnection(ctx, raw, out)
}

// clearStaleOnPass drops the stored failure markers after a probe proves the
// credential answers.
//
// Without it the row and the chip disagree: the row is painted from
// providerConnections.data, where a chat error from months ago still sits
// (errorCode 402, lastError "Insufficient credits", lastErrorAt 2026-09-10), so
// every page refresh redraws the account as quota_exhausted while clicking Test
// reports OK — and since nothing ever rewrites that blob, it stays wrong
// forever. Observed exactly that on the OpenRouter account on 2026-10-28.
//
// The write is deliberately one-way. Persisting a FAILURE from this endpoint is
// the DoS primitive the read-only design avoids (the route is reachable from the
// dashboard, so anything that can park an account can be looped). Clearing on
// success has no such shape: it can only turn a row green, and the chat path
// re-records a real failure the moment one happens. Routing state
// (rateLimitedUntil, backoffLevel, modelLock_*) is never touched — see
// Repo.ClearConnectionStaleError.
//
// Only a conclusive pass counts. A 429 "not judged" or a geo-block says nothing
// about the credential either way and must leave the stored verdict alone.
func (h *Handler) clearStaleOnPass(connID string, res connectionTestResult) {
	if !res.Valid || res.GeoBlocked || res.Error != "" || res.Warning != "" {
		return
	}
	if err := h.repo.ClearConnectionStaleError(connID); err != nil {
		log.Warn("dashboard", "clear stale connection error failed", "conn", connID, "error", err)
	}
}

// probeOpenrouterConnection judges an OpenRouter key against /api/v1/key, not a
// chat call.
//
// A chat probe cannot answer the only question the operator has — "is this key
// alive?" — because OpenRouter prices its catalogue per account: an account that
// never bought credits answers 402 "Insufficient credits" for every paid model,
// which is a payment state, not a credential state. The old path probed
// openai/gpt-4o-mini, so a perfectly good free-tier account showed a red chip
// and the row looked broken while the same key served :free models with 200.
//
// /api/v1/key is free, unlimited and deterministic: 200 with the key's own
// metadata (tier, limits, usage, expiry) for a live key, 401 "User not found"
// for a dead one. That is exactly the Cline treatment, for the same reason.
//
// The 402 case is still reported — as a warning, not a failure — because a
// drained account is valid but cannot serve paid models, and the operator needs
// to see that distinction without the row turning red.
func (h *Handler) probeOpenrouterConnection(ctx context.Context, raw map[string]any, out connectionTestResult) connectionTestResult {
	token := connString(raw, "apiKey", "api_key", "accessToken", "access_token", "token", "key")
	if token == "" {
		out.Error = "No credential found on this connection (looked for apiKey / accessToken / token)."
		return out
	}

	client := h.httpClientForTest()
	ctx, cancel := context.WithTimeout(ctx, connectionTestTimeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/key", nil)
	if err != nil {
		out.Error = "Could not build probe request: " + err.Error()
		return out
	}
	req.Header.Set("Authorization", "Bearer "+token)
	setProbeUserAgent(req)

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

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		out.Valid = true
		out.TestedAt = time.Now().UTC().Format(time.RFC3339)
		// A live key with no credits is still valid; say so in the tooltip
		// without downgrading the chip. A drained wallet is a payment state,
		// not a credential state, and turning the row amber trains the operator
		// to ignore amber on accounts that work.
		if note := openrouterBalanceNote(text); note != "" {
			out.Note = note
		}
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		out.Error = fmt.Sprintf("Credential rejected (HTTP %d): %s",
			resp.StatusCode, truncate(sanitizeUpstreamText(text), 240))
	case resp.StatusCode == 429:
		out.Valid = true
		out.Warning = "OpenRouter rate-limited the probe (HTTP 429). The credential was not judged."
	default:
		out.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(sanitizeUpstreamText(text), 240))
	}
	return out
}

// openrouterBalanceNote turns the /api/v1/key payload into one sentence about
// the account's spending state, or "" when there is nothing to say.
//
// The fields are the ones OpenRouter documents: is_free_tier, limit_remaining
// (nil means unlimited) and usage. A free-tier key that has never purchased
// credits reports is_free_tier true and a zero balance; that key serves :free
// models but no paid one, which is worth a warning and not a failure.
func openrouterBalanceNote(body string) string {
	var doc struct {
		Data struct {
			IsFreeTier     bool     `json:"is_free_tier"`
			Limit          *float64 `json:"limit"`
			LimitRemaining *float64 `json:"limit_remaining"`
			Usage          float64  `json:"usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return ""
	}
	d := doc.Data
	if d.LimitRemaining != nil && *d.LimitRemaining <= 0 {
		return fmt.Sprintf("Key is valid but its credit limit is spent (usage $%.2f). Paid models will answer 402; :free models still work.", d.Usage)
	}
	if d.IsFreeTier && d.Limit == nil {
		return fmt.Sprintf("Key is valid on the free tier (usage $%.2f). Paid models will answer 402 until credits are purchased; :free models still work.", d.Usage)
	}
	return ""
}

// testRefresh carries the token a pre-probe refresh resolved for a connection.
//
// changed is true when the token on the connection is not the one the probe was
// about to use — either because this caller refreshed it, or because a
// concurrent caller did and this caller adopted their fresh token. performed
// distinguishes "I did the exchange" from "someone else did", which is what the
// UI reports as a refresh indicator.
type testRefresh struct {
	changed     bool
	performed   bool
	accessToken string
	projectID   string
}

// refreshExpiredTokenForTest resolves a fresh OAuth token for the probe.
//
// It returns changed=false for connections that are not OAuth, have no refresh
// token, or whose token is still valid AND was not concurrently refreshed — in
// those cases the caller's existing blob is already correct.
func (h *Handler) refreshExpiredTokenForTest(ctx context.Context, conn *models.ProviderConnection, raw map[string]any) (testRefresh, error) {
	if conn == nil || conn.ID == "" {
		return testRefresh{}, nil
	}
	// Only OAuth connections carry a refresh token; an API-key connection has
	// nothing to refresh and must not be touched.
	oauthData := providers.ParseOAuthConnection(raw)
	if oauthData == nil || oauthData.RefreshToken == "" {
		return testRefresh{}, nil
	}

	token, projectID, refreshed, err := oauth.RefreshStoredConnection(ctx, h.repo, h.httpClientForTest(), conn.ID, false)
	if err != nil {
		// Even on error the helper returns the best-known token; adopt it so a
		// partially-successful refresh (token swapped but persist failed) is used.
		return testRefresh{changed: token != "" && token != oauthData.AccessToken, accessToken: token, projectID: projectID}, err
	}

	// refreshed=true means THIS call did the exchange. refreshed=false with a
	// different token means a concurrent caller did, and the lock handed us their
	// result. Both cases require the probe to use the returned token; only the
	// former is reported as "refreshed just now" in the UI.
	changed := refreshed || (token != "" && token != oauthData.AccessToken)
	return testRefresh{changed: changed, performed: refreshed, accessToken: token, projectID: projectID}, nil
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

	// Reaching here means the token is still (locally) expired after the
	// pre-probe refresh attempt — either the refresh failed, or the connection
	// has no refresh token. Report it without a network round trip: it is free to
	// detect, and probing it would only burn a request to learn what we already
	// know.
	if antigravityTokenExpired(raw) {
		out.Error = "Token expired and could not be refreshed (no usable refresh token, or the refresh was rejected). Sign the account in again."
		return out
	}

	body := buildAntigravityProbeBody(antigravityProbeModel())

	// The client profile is provider-wide, so a probe uses the same identity the
	// router would. Reading it per connection here would make Test report on a
	// fingerprint that no request ever uses.
	profile := providers.AntigravityProfileIDE
	if settings, err := h.repo.GetSettings(); err == nil && settings != nil {
		profile = providers.NormalizeAntigravityClientProfile(settings.AntigravityClientProfile)
	}
	userAgent := providers.AntigravityUserAgent(profile)

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
// probeClineConnection validates a Cline / ClinePass credential with the model
// CATALOGUE GET instead of a chat call. A chat probe cannot judge Cline: paid
// models need credits (HTTP 402) and free-tier models flap (HTTP 429/500), so
// the same good credential shows a different colour on every press. The
// catalogue GET is free, unlimited, and deterministic — 200 = valid, 401/403 =
// rejected.
func (h *Handler) probeClineConnection(ctx context.Context, raw map[string]any, out connectionTestResult) connectionTestResult {
	token := connString(raw, "accessToken", "access_token", "apiKey", "token", "key")
	if token == "" {
		out.Error = "No credential found on this connection (looked for accessToken / apiKey / token)."
		return out
	}
	_, status, err := h.getClineCatalog(
		endpointForCline(isClinePassProvider(out.Provider)), token, connectionTestTimeout())
	if err != nil {
		if ctx.Err() != nil {
			out.Error = fmt.Sprintf("Test timed out after %ds.", int(connectionTestTimeout().Seconds()))
			return out
		}
		out.Error = "Catalogue probe failed: " + err.Error()
		return out
	}
	out.Status = status

	switch {
	case status == http.StatusOK:
		out.Valid = true
		out.TestedAt = time.Now().UTC().Format(time.RFC3339)
	case status == 401 || status == 403:
		out.Error = fmt.Sprintf("Credential rejected (HTTP %d): re-authenticate this Cline account.", status)
	case status == 402:
		out.Valid = true
		out.Warning = "Credential is valid but the account is out of credits (HTTP 402)."
	case status == 429:
		out.Valid = true
		out.Warning = "Cline rate-limited the probe (HTTP 429). The credential was not judged."
	default:
		out.Valid = true
		out.Warning = fmt.Sprintf("Catalogue returned HTTP %d — the credential was accepted.", status)
	}
	return out
}

func (h *Handler) probeGenericConnection(ctx context.Context, raw map[string]any, out connectionTestResult) connectionTestResult {
	apiKey := connString(raw, "apiKey", "api_key", "accessToken", "access_token", "token", "key", "copilotToken")
	if apiKey == "" {
		out.Error = "No credential found on this connection (looked for apiKey / accessToken / token)."
		return out
	}
	// Same token normalization the chat path applies
	// (handlers/chat.NormalizeProviderToken): Cline's API rejects a plain
	// bearer and answers the misleading "latest version of Cline" 401 even
	// when the credential is good.
	switch out.Provider {
	case "cline", "clinepass":
		apiKey = providers.ClineAccessToken(apiKey)
	}

	cfg, ok := providers.KnownProviders[out.Provider]
	if !ok {
		// Custom provider nodes (OpenAI / Anthropic compatible endpoints)
		if nodeData := h.findProviderNodeData(out.Provider); nodeData != nil && nodeData.BaseURL != "" {
			return h.probeCustomNodeConnection(ctx, nodeData, apiKey, out)
		}
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
		"model":      h.probeModel(out.Provider),
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
	setProbeUserAgent(req)

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
	case resp.StatusCode == 402:
		// Auth was accepted (402 = payment/balance, not auth); the account is
		// drained. Cline answers insufficient_credits here. Credential valid,
		// account unusable until topped up.
		out.Valid = true
		out.Warning = fmt.Sprintf("Credential is valid but the account is out of credits (HTTP 402): %s",
			truncate(sanitizeUpstreamText(text), 200))
	default:
		out.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(sanitizeUpstreamText(text), 240))
	}
	return out
}

func (h *Handler) findProviderNodeData(providerID string) *db.ProviderNodeData {
	if h.repo == nil || providerID == "" {
		return nil
	}
	canonical := providers.ResolveAlias(providerID)
	if _, d, err := h.repo.GetProviderNodeByID(providerID); err == nil && d != nil {
		return d
	}
	if canonical != providerID {
		if _, d, err := h.repo.GetProviderNodeByID(canonical); err == nil && d != nil {
			return d
		}
	}
	if _, d, err := h.repo.GetProviderNodeByPrefix(providerID); err == nil && d != nil {
		return d
	}
	return nil
}

func (h *Handler) probeCustomNodeConnection(ctx context.Context, nodeData *db.ProviderNodeData, apiKey string, out connectionTestResult) connectionTestResult {
	baseURL := strings.TrimRight(strings.TrimSpace(nodeData.BaseURL), "/")
	if baseURL == "" {
		out.Error = "No base URL configured for custom provider node."
		return out
	}

	canonical := providers.ResolveAlias(out.Provider)
	isAnthropic := strings.HasPrefix(strings.ToLower(out.Provider), providers.CustomAnthropicPrefix) ||
		strings.HasPrefix(strings.ToLower(canonical), providers.CustomAnthropicPrefix) ||
		strings.Contains(strings.ToLower(nodeData.APIType), "anthropic")

	client := h.httpClientForTest()
	ctx, cancel := context.WithTimeout(ctx, connectionTestTimeout())
	defer cancel()

	// 1. Try GET /models (or configured modelsPath)
	modelsPath := nodeData.ModelsPath
	if modelsPath == "" {
		modelsPath = "/models"
	}
	cleanPath := "/" + strings.TrimLeft(modelsPath, "/")
	modelsURL := baseURL
	if isAnthropic {
		modelsURL = strings.TrimSuffix(baseURL, "/messages")
	}
	if !strings.HasSuffix(modelsURL, cleanPath) {
		modelsURL += cleanPath
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		if isAnthropic {
			req.Header.Set("x-api-key", apiKey)
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		setProbeUserAgent(req)
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, connectionTestMaxBody))
			text := string(bodyBytes)
			out.Status = resp.StatusCode

			if isGeoBlockedError(text) {
				out.Valid = true
				out.GeoBlocked = true
				out.Warning = "Egress location blocked by the provider."
				return out
			}

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				out.Valid = true
				out.TestedAt = time.Now().UTC().Format(time.RFC3339)
				return out
			}
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				out.Error = fmt.Sprintf("Credential rejected (HTTP %d): %s",
					resp.StatusCode, truncate(sanitizeUpstreamText(text), 240))
				return out
			}
			if resp.StatusCode == 429 {
				out.Valid = true
				out.Warning = "Provider rate-limited the probe (HTTP 429). The credential was not judged."
				return out
			}
			// If status is 404 / 405 (models endpoint not supported), fall through to chat probe
		}
	}

	// 2. Fallback: Chat completion probe
	probeModel := ""
	candKeys := h.providerKeyCandidates(canonical, out.Provider)
	if cachedModels, err := h.repo.ListCachedModelsForDashboard(candKeys...); err == nil && len(cachedModels) > 0 {
		for _, cm := range cachedModels {
			if cm.Kind == "" || cm.Kind == "llm" {
				probeModel = cm.ModelID
				break
			}
		}
	}
	// An operator-pinned model outranks the cache: whatever was imported first
	// may be a paid model the account cannot use, which is exactly the false
	// red this field exists to avoid.
	if pinned := h.pinnedProbeModel(out.Provider); pinned != "" {
		probeModel = pinned
	}
	if probeModel == "" {
		probeModel = "gpt-4o-mini"
	}

	chatURL := baseURL
	if isAnthropic {
		if !strings.HasSuffix(chatURL, "/messages") {
			chatURL += "/messages"
		}
	} else {
		if !strings.HasSuffix(chatURL, "/chat/completions") {
			chatURL += "/chat/completions"
		}
	}

	bodyMap := map[string]any{
		"model":      probeModel,
		"max_tokens": 1,
		"messages":   []map[string]any{{"role": "user", "content": "ping"}},
	}
	body, _ := json.Marshal(bodyMap)

	chatReq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(body))
	if err != nil {
		out.Error = "Could not build probe request: " + err.Error()
		return out
	}
	chatReq.Header.Set("Content-Type", "application/json")
	chatReq.Header.Set("Authorization", "Bearer "+apiKey)
	if isAnthropic {
		chatReq.Header.Set("x-api-key", apiKey)
		chatReq.Header.Set("anthropic-version", "2023-06-01")
	}
	setProbeUserAgent(chatReq)

	chatResp, err := client.Do(chatReq)
	if err != nil {
		if ctx.Err() != nil {
			out.Error = fmt.Sprintf("Test timed out after %ds.", int(connectionTestTimeout().Seconds()))
			return out
		}
		out.Error = "Probe request failed: " + err.Error()
		return out
	}
	defer chatResp.Body.Close()

	chatBytes, _ := io.ReadAll(io.LimitReader(chatResp.Body, connectionTestMaxBody))
	chatText := string(chatBytes)
	out.Status = chatResp.StatusCode

	if isGeoBlockedError(chatText) {
		out.Valid = true
		out.GeoBlocked = true
		out.Warning = "Egress location blocked by the provider."
		return out
	}

	switch {
	case chatResp.StatusCode >= 200 && chatResp.StatusCode < 300:
		out.Valid = true
		out.TestedAt = time.Now().UTC().Format(time.RFC3339)
	case chatResp.StatusCode == 401 || chatResp.StatusCode == 403:
		out.Error = fmt.Sprintf("Credential rejected (HTTP %d): %s",
			chatResp.StatusCode, truncate(sanitizeUpstreamText(chatText), 240))
	case chatResp.StatusCode == 400 || chatResp.StatusCode == 404 || chatResp.StatusCode == 422:
		out.Valid = true
		out.Warning = fmt.Sprintf("Auth accepted (HTTP %d): %s",
			chatResp.StatusCode, truncate(sanitizeUpstreamText(chatText), 200))
	case chatResp.StatusCode == 429:
		out.Valid = true
		out.Warning = "Provider rate-limited the probe (HTTP 429). The credential was not judged."
	default:
		out.Error = fmt.Sprintf("HTTP %d: %s", chatResp.StatusCode, truncate(sanitizeUpstreamText(chatText), 240))
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

// pinnedProbeModel returns the model the operator pinned for this provider, or
// "" when they pinned none. An empty result is not "no model": it means the
// caller keeps whatever default it already had.
func (h *Handler) pinnedProbeModel(provider string) string {
	if h == nil || h.repo == nil || provider == "" {
		return ""
	}
	s, err := h.repo.GetSettings()
	if err != nil || s == nil || s.ProviderStrategies == nil {
		return ""
	}
	for _, key := range []string{provider, providers.ResolveAlias(provider)} {
		if key == "" {
			continue
		}
		if strat, ok := s.ProviderStrategies[key]; ok && strat.TestModel != "" {
			return strat.TestModel
		}
	}
	return ""
}

// probeModel returns the model the per-account probe should ask for: the model
// the operator pinned for this provider, else the built-in default. A pinned
// model is the escape hatch for providers whose cheap models rotate — the
// default would start reporting healthy accounts as broken the day its probe
// model is retired.
func (h *Handler) probeModel(provider string) string {
	if m := h.pinnedProbeModel(provider); m != "" {
		return m
	}
	return genericProbeModel(provider)
}

// genericProbeModels maps a provider to a low-cost model suitable for a probe.
var genericProbeModels = map[string]string{
	"openai":     "gpt-4o-mini",
	"deepseek":   "deepseek-chat",
	"groq":       "llama-3.1-8b-instant",
	"mistral":    "mistral-small-latest",
	"openrouter": "openai/gpt-4o-mini",
	// Cline requires the modelType/model form and gates paid models behind
	// credits; a free-tier flash model gets a clean 200 on a drained account.
	"cline":     "google/gemma-4-26b-a4b-it:free",
	"clinepass": "cline-pass/deepseek-v4-flash",
	// RunAnywhere rejects models outside the key's entitlement with HTTP 403
	// model_not_entitled, which the probe path reads as a bad credential. The
	// default gpt-4o-mini is not served here, so the probe would fail on every
	// healthy Wally key.
	"wally": "glm-5.3-flash",
	// Nous serves its free tier to any Portal-authenticated key; a paid-only
	// default would report a healthy free account as broken. longcat-2.0:free
	// was withdrawn ("This model is no longer free" -> 404), so probe on the
	// current free preview instead.
	"nous": "meituan/longcat-2.5-preview:free",
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
