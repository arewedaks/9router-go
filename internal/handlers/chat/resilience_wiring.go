package chat

import (
	"context"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/resilience"
)

// Provider-level resilience wiring.
//
// Two layers sit between a request and an upstream provider:
//
//   - A circuit breaker per provider. It stops the gateway from hammering a
//     provider that is failing (5xx/timeout only — a 429 is per-account rate
//     limiting and must not trip it, or a few throttled accounts would block
//     every healthy one). Its state lives in memory and is keyed by provider,
//     so it complements the per-connection model locks rather than replacing
//     them: the locks say "this account/model is cooling down", the breaker
//     says "this whole provider is down".
//
//   - A per-account semaphore. It caps concurrent in-flight requests to one
//     account so a burst cannot trigger the provider's own rate limit. Keyed by
//     provider:account:proxy so accounts behind one proxy do not share a limit.

// breakerOptionsFromSettings is where per-provider thresholds would come from.
// Today every provider uses the defaults; the indirection keeps the call sites
// stable when the dashboard grows a per-provider override.
func breakerOptionsFor(provider string) resilience.BreakerOptions {
	return resilience.BreakerOptions{}
}

// semaphoreMaxConcurrency resolves the concurrency cap for a connection.
//
// A connection may set providerSpecificData.maxConcurrency to override; 0 or a
// negative value disables the limiter for that account. Absent means the
// default cap, which is what makes the semaphore actually do something — an
// unbounded default would leave the 429 cascade it exists to prevent.
func semaphoreMaxConcurrency(connData *ConnectionData) int {
	if connData == nil {
		return 0
	}
	psd := connData.ProviderSpecificData
	if psd == nil {
		return defaultSemaphoreConcurrency
	}
	raw, ok := psd["maxConcurrency"]
	if !ok {
		return defaultSemaphoreConcurrency
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return defaultSemaphoreConcurrency
	}
}

// defaultSemaphoreConcurrency is the per-account in-flight cap when a
// connection does not configure one.
const defaultSemaphoreConcurrency = 3

// semaphoreProxyHash identifies the proxy bucket an account sits behind, so
// accounts sharing a proxy share a concurrency limit. A connection with no
// proxy uses "direct".
func semaphoreProxyHash(connData *ConnectionData) string {
	if connData == nil {
		return "direct"
	}
	if connData.ProxyPoolID != "" {
		return "pool:" + connData.ProxyPoolID
	}
	if connData.ConnectionProxyURL != "" {
		return "url:" + connData.ConnectionProxyURL
	}
	return "direct"
}

// acquireAccountSlot reserves a concurrency slot for the connection, returning
// the release function. A limiter that is disabled or times out yields a no-op
// release and a nil error: the semaphore is an optimisation, and refusing a
// request because a slot was busy would be worse than sending it.
func acquireAccountSlot(ctx context.Context, provider, connectionID string, connData *ConnectionData) func() {
	max := semaphoreMaxConcurrency(connData)
	if max <= 0 {
		return func() {}
	}
	key := resilience.SemaphoreKey(provider, connectionID, semaphoreProxyHash(connData))
	release, err := resilience.Acquire(ctx, key, resilience.SemaphoreOptions{MaxConcurrency: max})
	if err != nil {
		// Log and proceed: a saturated queue must not turn into a failed
		// request when the upstream itself is reachable.
		log.Warn("fallback", "semaphore acquire failed, proceeding", "provider", provider, "conn", connectionID, "error", err)
		return func() {}
	}
	return release
}

// providerBreakerAllows reports whether the provider's breaker admits a
// request, logging when it does not so the reason is visible.
func providerBreakerAllows(provider string) bool {
	b := resilience.BreakerFor(provider, breakerOptionsFor(provider))
	if b.Allow() {
		return true
	}
	log.Warn("fallback", "provider breaker open, skipping", "provider", provider, "retryAfterMs", b.RetryAfter().Milliseconds())
	return false
}

// recordProviderOutcome feeds a completed attempt back to the breaker.
//
// Only provider-level failures count: a 5xx or a transport error. Client errors
// (400/401/403) mean the request or credential is wrong, not that the provider
// is down, and a 429 is per-account rate limiting handled by the semaphore and
// the model locks.
func recordProviderOutcome(provider string, status int, transportErr bool) {
	b := resilience.BreakerFor(provider, breakerOptionsFor(provider))
	if transportErr || resilience.IsProviderFailureStatus(status) {
		b.RecordFailure()
		return
	}
	// Any other completed exchange means the provider answered, so it is up.
	b.RecordSuccess()
}

// blockAccountAfter429 holds the account's semaphore gate so concurrent
// requests wait instead of piling onto an already-throttled account.
func blockAccountAfter429(provider, connectionID string, connData *ConnectionData, cooldown time.Duration) {
	if cooldown <= 0 {
		return
	}
	key := resilience.SemaphoreKey(provider, connectionID, semaphoreProxyHash(connData))
	resilience.Block(key, cooldown)
}
