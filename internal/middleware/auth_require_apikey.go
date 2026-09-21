package middleware

import (
	"net"
	"net/http"
	"strings"

	"9router/proxy/internal/db"
)

// RequireApiKeyUnlessDisabled wraps RequireApiKeyWithSession and honours
// VansRouter's `requireApiKey` / `allowRemoteNoApiKey` settings flags.
//
// The flags are read per request rather than captured at router build time: the
// dashboard toggle has to take effect on the next request, not after a restart,
// and the settings row lives in SQLite where the middleware cannot watch it.
// A read is a single indexed row lookup on a table this process already owns.
//
// The two flags together give three levels of exposure, mirroring VansRouter:
//
//	requireApiKey  allowRemoteNoApiKey   behaviour
//	true           (anything)            every /v1 request needs a key
//	false          false                 only loopback clients are let through
//	false          true                   anyone who can reach the port
//
// A missing flag keeps the stricter reading, so an untouched install and any
// migrated VansRouter row that never wrote these keys both require a key.
//
// A settings read failure also fails closed — a database error must not be the
// thing that silently exposes the proxy.
func RequireApiKeyUnlessDisabled(repo *db.Repo, verifySession func(token string) bool, sessionPaths ...string) func(http.Handler) http.Handler {
	guarded := RequireApiKeyWithSession(repo, verifySession, sessionPaths...)

	return func(next http.Handler) http.Handler {
		guardedNext := guarded(next)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if required, remoteOK := apiKeyPolicy(repo); !required {
				// The main switch is off. Without the second flag only a client
				// on this machine may skip the key, so an operator who turns the
				// switch off to test locally does not also publish the proxy.
				if remoteOK || isLoopbackRequest(r) {
					next.ServeHTTP(w, r)
					return
				}
			}
			guardedNext.ServeHTTP(w, r)
		})
	}
}

// apiKeyPolicy reports (requireApiKey, allowRemoteNoApiKey). Both default to the
// stricter value on absence and on any read error.
func apiKeyPolicy(repo *db.Repo) (required bool, remoteOK bool) {
	s, err := repo.GetSettings()
	if err != nil || s == nil {
		return true, false
	}
	if s.RequireAPIKey != nil && !*s.RequireAPIKey {
		required = false
	} else {
		required = true
	}
	remoteOK = s.AllowRemoteNoApiKey != nil && *s.AllowRemoteNoApiKey
	return required, remoteOK
}

// isLoopbackRequest reports whether the request came from this machine.
//
// Only the TCP peer address is consulted. X-Forwarded-For is deliberately NOT
// read here: it is a client-supplied header, so believing it would let any
// caller claim to be 127.0.0.1 and skip the key requirement entirely. Behind a
// reverse proxy on the same host the peer is the proxy, which is loopback, and
// that is the honest answer for "did this arrive from this machine". The
// dashboard's own trustProxy setting governs a different question (who to blame
// in the login limiter) and does not extend to deciding whether to authenticate.
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// No port: treat the whole value as the address. SplitHostPort fails on
		// bare IPs, which is how test requests are built, and on a bracketed
		// IPv6 literal with no port ([::1]).
		host = strings.TrimSuffix(strings.TrimPrefix(r.RemoteAddr, "["), "]")
	}
	if host == "" {
		return false
	}
	// http.Request.RemoteAddr may carry a zone on IPv6 (fe80::1%eth0).
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
