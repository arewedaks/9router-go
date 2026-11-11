package providers

// Free-pi wire contract constants.
//
// Everything here is a fact about api.freepi.ai that more than one layer of this
// repository needs: the executor builds requests from it, the OAuth handler runs
// the login flow with it, the dashboard exposes it, and the quota reader parses
// it. Keeping them in one file (rather than re-typing string literals per
// package) is what lets the contract test in freepi_contract_test.go pin them
// against the recorded fixtures in testdata/freepi.
//
// Provenance, per value:
//   - dennisonbertram/free-pi-cli — the official CLI (authoritative for the wire
//     contract it dials: src/provider.ts, src/auth-flow.ts, src/consent.ts,
//     src/update-check.ts, src/commands.ts).
//   - hungkevin/free-pi-cli-proxy — a working third-party proxy against the same
//     upstream (authoritative for operational behaviour the CLI never hits:
//     server.mjs).
const (
	// FreePiProviderID is the registry id; FreePiAlias is the published prefix
	// (free-pi/<model> and fp/<model> both resolve here).
	FreePiProviderID = "free-pi"
	FreePiAlias      = "fp"

	// FreePiBaseURL is the chat endpoint. The executor derives the sibling
	// routes (auth, ads, session) from its origin rather than hardcoding a
	// second copy of the host, so FREEPI_BASE_URL-style overrides stay possible.
	FreePiBaseURL = "https://api.freepi.ai/v1/chat/completions"

	// FreePiOrigin is the API root, used for /v1/chat/completions' siblings:
	// /auth/*, /ads/*, /session/reset, /client-version, /me, /me/stats.
	FreePiOrigin = "https://api.freepi.ai"

	// FreePiConsentVersion is the consent revision the server expects at
	// POST /auth/consent (src/consent.ts CONSENT_VERSION). The dashboard renders
	// the matching text verbatim and requires an explicit accept; the server
	// rejects a poll that arrives without recorded consent (403 consent_required).
	FreePiConsentVersion = "4"

	// FreePiClientVersion is the fallback for the x-client-version header. The
	// server enforces a minimum (GET /client-version → `min`) and refuses older
	// clients outright, so this must stay at or above that floor; at login we
	// replace it with the server-reported `latest`.
	//
	// Measured live 2026-11-11: min == latest == "0.6.18". The upstream commit that
	// introduced this provider pinned "0.7", a version free-pi has never published —
	// it happens to survive only because the floor is currently below it. Tracking
	// the real number keeps the header honest if the floor ever moves past a stale
	// guess.
	FreePiClientVersion = "0.6.18"

	// FreePiDefaultModel is the id free-pi-cli's own provider config defaults to
	// (MODEL_ID in src/provider.ts). The upstream commit used
	// "deepseek/deepseek-v4-flash"; free-pi serves bare ids with no vendor prefix,
	// so that value answered model_not_found. The id below is what /client-version
	// actually publishes (2026-11-11), and it is seeded into the static catalog only
	// so a freshly added connection is routable before the catalogue import runs.
	FreePiDefaultModel = "deepseek-v4-flash"

	// FreePiSessionCap is how many concurrent sessions one account tolerates
	// (hungkevin/free-pi-cli-proxy server.mjs:958, and the 409 message text). We
	// deliberately occupy a single session and treat the rest as unspent
	// headroom: the session id is derived from account identity, so every
	// connection and every process converges on the same one.
	FreePiSessionCap = 4
)

// Free-pi upstream error codes. Branch on the code, never on the message text:
// the wording is server-owned and changes between deploys (the CLI's own
// error-notice extension makes the same point).
const (
	// FreePiCodeConcurrentSession means the account already holds
	// FreePiSessionCap live sessions. It is a count problem, not a contention
	// problem — a human CLI's sessions are indistinguishable from our own. The
	// documented remedy is POST /session/reset ("/close-other-session" in the
	// CLI), applied at most once per request.
	FreePiCodeConcurrentSession = "concurrent_session"

	// FreePiCodeRateLimitExceeded is the HOURLY request cap for this account. It
	// clears at the next hour boundary, so the right response is a short lock
	// and rotation eligibility — not a long backoff.
	FreePiCodeRateLimitExceeded = "rate_limit_exceeded"

	// The remaining 429 codes mean "the day's free allowance is spent". Each is a
	// named constant (not just a string in the slice below) because the error
	// classifier matches on them by name; a typo in a rule would be a typo in a
	// cooldown nobody notices until the retry budget is gone.
	FreePiCodeDailyCreditLimited = "creditlimitexceeded"
	FreePiCodeBillingError       = "billing_error"
	FreePiCodeBudgetLimitError   = "budgetlimiterror"
	FreePiCodeGoUsageLimitError  = "gousagelimiterror"

	// FreePiCodeConsentRequired arrives as HTTP 403 from POST /auth/token when
	// the server has no recorded consent for the login session.
	FreePiCodeConsentRequired = "consent_required"
)

// FreePiDailyQuotaCodes are the 429 codes that mean "the day's free allowance is
// spent". They must never be retried on the same account: the limit does not
// clear until 00:00, so a retry only burns the budget and delays the error
// (hungkevin/free-pi-cli-proxy server.mjs:1057).
var FreePiDailyQuotaCodes = []string{
	FreePiCodeDailyCreditLimited,
	"insufficient_quota",
	FreePiCodeBillingError,
	FreePiCodeBudgetLimitError,
	FreePiCodeGoUsageLimitError,
}

// FreePiIsDailyQuotaCode reports whether a 429 code means the daily allowance is
// gone rather than an hourly throttle.
func FreePiIsDailyQuotaCode(code string) bool {
	for _, c := range FreePiDailyQuotaCodes {
		if c == code {
			return true
		}
	}
	return false
}

// FreePiImpressionHosts are the only hosts we will follow an ad `imp_url` to.
// The URL is upstream-supplied and we fetch it server-side, so an unvalidated
// host is an SSRF primitive; the reference proxy applies the same check
// (server.mjs:350).
var FreePiImpressionHosts = []string{"trygravity.ai", "trygravity.com"}

// FreePiAdGateMessage is the 403 the account receives when upstream will not
// serve completions because no ad has been shown. We surface it verbatim rather
// than inventing an impression to clear it.
const FreePiAdGateMessage = "Ads aren't showing for this account"
