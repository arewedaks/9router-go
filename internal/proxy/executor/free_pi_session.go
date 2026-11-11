package executor

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// Free-pi session machinery.
//
// free-pi's upstream ties every completion to an `x-session-id` and bounds how
// many sessions one account may hold at once (4 — hungkevin/free-pi-cli-proxy
// server.mjs:958). Exceeding it yields a 409 with code `concurrent_session`,
// which the reference proxy clears with POST /session/reset and a single retry.
//
// This file builds deliberately for the tightest reading of that limit: one
// session per account, no matter how many connections or processes exist. The
// other three are unspent headroom. That costs nothing and survives the day
// upstream tightens the cap to 1.
//
// Two mechanisms carry it:
//
//   - Session identity is DERIVED, not minted. uuid.NewSHA1 over the account
//     identity means two dashboard connections for one account, a restarted
//     process, and a second gateway instance all compute the SAME id. Nothing
//     has to be remembered for that to hold.
//   - A lease marks which process is currently driving the account, so a 409
//     from a sibling's in-flight request is never "fixed" by resetting the
//     session out from under it.
//
// The lease holds a per-process epoch rather than the session id on purpose:
// every holder of an account computes the same session id, so a session-id
// value would make the compare-and-delete in LeaseStore.ReleaseLease match for
// everybody and free a sibling's live row.

const (
	// freePiLeaseScope namespaces free-pi session leases in upstream_leases.
	// Aliased from db so the two cannot drift.
	freePiLeaseScope = db.LeaseScopeFreePiSession

	// freePiLeaseTTL bounds how long a process claims its account. It is a
	// reset GUARD, not a request lock: an expired row only means a sibling may
	// now take over reset rights, and since every process derives the same
	// session id, taking over re-establishes the same session. Short enough
	// that a crashed process stops blocking within a minute (AcquireLease only
	// takes over *expired* rows, so a long TTL turns one crash into a long
	// block), long enough to cover a normal request.
	freePiLeaseTTL = 60 * time.Second

	// freePiResetCooldown is the minimum gap between two automatic
	// POST /session/reset calls for one account. It is the last line of
	// defence against a reset loop.
	freePiResetCooldown = 3 * time.Minute

	// freePiCooldownEntries caps the cooldown bookkeeping. Entries expire after
	// freePiResetCooldown, so the only way to grow past this is a burst of
	// distinct accounts larger than the cap — at which point forgetting the
	// oldest is right, not fatal (AGENTS.md section 4.G).
	freePiCooldownEntries = 256
)

// freePiSessionNamespace is the UUIDv5 namespace for derived session ids. A
// fixed constant: changing it would orphan every existing upstream session.
var freePiSessionNamespace = uuid.MustParse("6f70656e-7069-2d66-7265-6570692d7365")

// freePiProcessEpoch identifies THIS process for the lifetime of the binary. It
// is the lease value, so a lease row can be attributed to its holder.
var freePiProcessEpoch = uuid.New().String()

// freePiStripeCount is the number of mutexes guarding accounts. Striping keeps
// memory O(1) with no map to bound, while still serializing the only critical
// section that matters: the read-lease / reset / record-lease decision
// (AGENTS.md section 4.G — an unbounded per-connection lock map is a leak).
const freePiStripeCount = 64

var freePiStripes [freePiStripeCount]sync.Mutex

// freePiStripe returns the mutex guarding one account's reset decision.
func freePiStripe(accountKey string) *sync.Mutex {
	sum := sha256.Sum256([]byte(accountKey))
	return &freePiStripes[int(sum[0])%freePiStripeCount]
}

// freePiAccountKey is the stable, non-secret identity of a free-pi account.
//
// The JWT `sub` claim is the account id (see testdata/freepi/auth-token-granted.json).
// When a token cannot be decoded — a malformed paste, or a future opaque
// credential — the token itself is hashed instead, so callers still get a
// stable key and the raw secret never reaches the leases table or a log.
func freePiAccountKey(apiKey string) string {
	if sub := freePiJWSubject(apiKey); sub != "" {
		return "sub:" + sub
	}
	sum := sha256.Sum256([]byte(apiKey))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// freePiJWSubject extracts the `sub` claim from a JWT without verifying the
// signature. Verification is upstream's job: we only need a stable label, never
// a security decision, so a forged token here is already worthless upstream.
func freePiJWSubject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		// Some issuers emit padded base64url; accept that too rather than
		// treating a valid token as opaque.
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Sub
}

// freePiSessionIDFor returns the upstream session id for one connection.
//
// precedence: an explicitly stored id (written at login) wins, so an operator
// can pin it; otherwise it is derived from the account identity, which is what
// makes every process and every extra connection converge on one session.
func freePiSessionIDFor(apiKey string, connData map[string]any) string {
	if connData != nil {
		if stored, ok := connData["freePiSessionId"].(string); ok && stored != "" {
			return stored
		}
	}
	return uuid.NewSHA1(freePiSessionNamespace, []byte(freePiAccountKey(apiKey))).String()
}

// FreePiSessionID is the exported form of the derivation, so the login handler
// can persist exactly the value the executor will send. Two implementations of
// this rule would be two ways to drift into a second upstream session.
func FreePiSessionID(apiKey string) string {
	return freePiSessionIDFor(apiKey, nil)
}

// freePiClientVersionFor returns the x-client-version to send.
//
// The server refuses clients below a minimum it publishes at GET
// /client-version, so the value stored at login (the server's own `latest`) is
// preferred over the compiled-in floor.
func freePiClientVersionFor(connData map[string]any) string {
	stored := ""
	if connData != nil {
		if v, ok := connData["freePiClientVersion"].(string); ok {
			stored = v
		}
	}
	// Prefer the newest of: the compiled-in floor, the version recorded at
	// sign-in, and what /client-version reports right now.
	//
	// The floor MOVES and it moves fast — 0.6.18 answered HTTP 426
	// upgrade_required hours after sign-in wrote that number into the
	// connection, so a stored value alone is a time bomb: the account keeps
	// working until upstream raises its minimum, then every request fails with a
	// message about a CLI the operator is not running. The catalogue endpoint is
	// unauthenticated and cheap, so it is consulted (with a short cache) rather
	// than trusted to a value frozen at login.
	return newestVersion(providers.FreePiClientVersion, stored, freePiLiveClientVersion())
}

// freePiLiveClientVersion returns the version /client-version currently
// advertises, memoised for a few minutes.
//
// A failure yields "": the gate is enforced by upstream, and a network blip on
// a best-effort header must not turn into a request the operator cannot make.
// The compiled-in floor still applies in that case.
var (
	freePiVersionMu     sync.Mutex
	freePiVersionCache  string
	freePiVersionExpiry time.Time
)

const freePiVersionTTL = 5 * time.Minute

func freePiLiveClientVersion() string {
	freePiVersionMu.Lock()
	if time.Now().Before(freePiVersionExpiry) {
		cached := freePiVersionCache
		freePiVersionMu.Unlock()
		return cached
	}
	freePiVersionMu.Unlock()

	latest := ""
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, providers.FreePiOrigin+"/client-version", nil); err == nil {
		req.Header.Set("Accept", "application/json")
		if resp, err := freePiVersionClient.Do(req); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var parsed struct {
					Latest string `json:"latest"`
					Min    string `json:"min"`
				}
				if raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)); err == nil {
					if json.Unmarshal(raw, &parsed) == nil {
						latest = newestVersion(parsed.Latest, parsed.Min)
					}
				}
			}
		}
	}

	freePiVersionMu.Lock()
	freePiVersionCache = latest
	// Cache a miss too, but briefly: hammering a down endpoint on every request
	// would be worse than sending a floor that may be one revision old.
	freePiVersionExpiry = time.Now().Add(freePiVersionTTL)
	freePiVersionMu.Unlock()
	return latest
}

// freePiVersionClient is a var so tests can point it at a local server.
var freePiVersionClient = &http.Client{Timeout: 6 * time.Second}

// newestVersion returns the highest of the given dotted version strings,
// ignoring blanks. Comparison is numeric per component, which is what free-pi
// publishes (0.6.19); a non-numeric component degrades to a string compare so an
// unexpected scheme still yields a deterministic answer rather than a panic.
func newestVersion(candidates ...string) string {
	best := ""
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if best == "" || compareVersion(c, best) > 0 {
			best = c
		}
	}
	return best
}

func compareVersion(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var ai, bi string
		if i < len(as) {
			ai = as[i]
		}
		if i < len(bs) {
			bi = bs[i]
		}
		if ai == bi {
			continue
		}
		an, aerr := strconv.Atoi(ai)
		bn, berr := strconv.Atoi(bi)
		if aerr == nil && berr == nil {
			if an != bn {
				if an > bn {
					return 1
				}
				return -1
			}
			continue
		}
		if ai > bi {
			return 1
		}
		return -1
	}
	return 0
}

// freePiAutoResetEnabled reports whether the connection permits an automatic
// POST /session/reset. Default is enabled; `freePiAutoReset: false` opts out,
// which is the documented setting for anyone running free-pi-cli interactively
// on the same account.
func freePiAutoResetEnabled(connData map[string]any) bool {
	if connData == nil {
		return true
	}
	if v, ok := connData["freePiAutoReset"].(bool); ok {
		return v
	}
	return true
}

// FreePiTokenExpiredCode is the synthetic code reported when the credential's
// own `exp` claim is in the past.
//
// free-pi issues a long-lived JWT with NO refresh token, so expiry is terminal:
// the only remedy is re-authentication. Catching it locally, before the request
// goes out, buys two things — the client gets "re-authenticate" instead of a
// bare 401 after a wasted round trip, and the connection is classified with a
// long cooldown rather than the generic two-minute 401 rule that would otherwise
// retry a permanently dead credential every couple of minutes, forever.
const FreePiTokenExpiredCode = "free_pi_token_expired"

// freePiTokenExpiry returns the credential's `exp` claim. ok is false when the
// token is opaque or carries no usable expiry, in which case the caller must let
// upstream decide.
func freePiTokenExpiry(token string) (exp time.Time, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Time{}, false
		}
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == "" {
		return time.Time{}, false
	}
	secs, err := claims.Exp.Int64()
	if err != nil || secs <= 0 {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// freePiTokenExpired reports whether the credential is already dead.
//
// The skew allowance is deliberately zero: `exp` is a hard boundary and a token
// a few seconds from expiry would fail upstream anyway, so treating it as live
// only wastes a request.
func freePiTokenExpired(token string, now time.Time) bool {
	exp, ok := freePiTokenExpiry(token)
	if !ok {
		return false
	}
	return !exp.After(now)
}
