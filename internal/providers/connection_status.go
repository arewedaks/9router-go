package providers

import "strings"

// Connection status vocabulary, mirroring the upstream Next.js 9router
// connection lifecycle (`src/sse/services/auth.js`):
//
//	active       -> a request succeeded, or the account recovered
//	unavailable  -> an auth/upstream failure locked the account
//	quota_exhausted -> upstream reported the quota/credit is exhausted
//	error        -> generic failure
//	expired      -> the stored OAuth token is past its expiry
//
// The dashboard must never equate "the toggle is on" with "the account works".
// Upstream does the same: `getStatusVariant(isActive, effectiveStatus)` in
// `src/shared/utils/connectionStatus.js` treats a connection that is not
// explicitly disabled as healthy only when the effective status is
// "active"/"success".
const (
	StatusActive         = "active"
	StatusUnavailable    = "unavailable"
	StatusQuotaExhausted = "quota_exhausted"
	StatusError          = "error"
	StatusExpired        = "expired"
	StatusUnknown        = "unknown"
)

// EffectiveStatus resolves the status a connection should be presented with.
//
// A stale "unavailable" marker is not proof the account is broken: the lock
// that produced it may have expired (upstream clears it lazily on the next
// success). Upstream therefore downgrades "unavailable" back to "active" once
// no cooldown remains — see `providers/page.js`:
//
//	const effectiveStatus =
//	  conn.testStatus === "unavailable" && !isCooldown ? "active" : conn.testStatus;
//
// `hasActiveCooldown` carries that `isCooldown` bit: it is true when the
// connection still holds a live `modelLock_*` entry.
//
// One recovery is refused: a connection whose last error was a spent
// credit/quota budget reports "quota_exhausted" instead of "active". A lapsed
// timer does not refill a balance, so treating it as healthy would keep a
// guaranteed-failing account in the pool — the exact "33/33 active but nothing
// connects" confusion the operator hit.
func EffectiveStatus(isActive int, testStatus string, hasActiveCooldown bool) string {
	return EffectiveStatusWithError(isActive, testStatus, hasActiveCooldown, "")
}

// EffectiveStatusWithError is EffectiveStatus plus the stored last error, so a
// quota exhaustion that outlives its cooldown is still reported as such.
func EffectiveStatusWithError(isActive int, testStatus string, hasActiveCooldown bool, lastError string) string {
	// A disabled connection reports its own state; the toggle wins.
	if isActive != 1 {
		return "disabled"
	}

	status := strings.ToLower(strings.TrimSpace(testStatus))
	if status == "" {
		return StatusUnknown
	}

	// A spent budget does not refill on a timer.
	if LooksLikeQuotaExhausted(lastError) {
		return StatusQuotaExhausted
	}

	if status == StatusUnavailable && !hasActiveCooldown {
		return StatusActive
	}
	return status
}

// IsEffectivelyActive reports whether a connection should count as usable:
// enabled AND its effective status is a healthy one. This is the definition
// used for the "N of M active" counters and the category tickers, so the
// dashboard stops claiming credit-exhausted accounts are connected.
func IsEffectivelyActive(isActive int, testStatus string, hasActiveCooldown bool) bool {
	return IsEffectivelyActiveWithError(isActive, testStatus, hasActiveCooldown, "")
}

// IsEffectivelyActiveWithError is IsEffectivelyActive plus the stored error.
func IsEffectivelyActiveWithError(isActive int, testStatus string, hasActiveCooldown bool, lastError string) bool {
	switch EffectiveStatusWithError(isActive, testStatus, hasActiveCooldown, lastError) {
	case StatusActive, "success":
		return true
	default:
		return false
	}
}

// StatusVariant maps an effective status onto the three display buckets the
// upstream UI uses ("success", "error", "default").
func StatusVariant(isActive int, effectiveStatus string) string {
	if isActive != 1 {
		return "default"
	}
	switch effectiveStatus {
	case StatusActive, "success":
		return "success"
	case StatusError, StatusExpired, StatusUnavailable, StatusQuotaExhausted:
		return "error"
	default:
		return "default"
	}
}
