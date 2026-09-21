package middleware

import (
	"net/http"

	"9router/proxy/internal/db"
)

// RequireApiKeyUnlessDisabled wraps RequireApiKeyWithSession and honours
// VansRouter's `requireApiKey` settings flag.
//
// The flag is read per request rather than captured at router build time: the
// dashboard toggle has to take effect on the next request, not after a restart,
// and the settings row lives in SQLite where the middleware cannot watch it.
// A read is a single indexed row lookup on a table this process already owns.
//
// When the flag is absent it defaults to requiring a key, so an untouched
// install and any migrated VansRouter database that never set the field keep
// the stricter behaviour. Only an explicit false opens the engine routes.
//
// A settings read failure also fails closed — a database error must not be the
// thing that silently exposes the proxy.
func RequireApiKeyUnlessDisabled(repo *db.Repo, verifySession func(token string) bool, sessionPaths ...string) func(http.Handler) http.Handler {
	guarded := RequireApiKeyWithSession(repo, verifySession, sessionPaths...)

	return func(next http.Handler) http.Handler {
		guardedNext := guarded(next)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiKeyRequired(repo) {
				guardedNext.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// apiKeyRequired reports whether the engine routes must be authenticated.
// Absent flag, or any error reading it, means yes.
func apiKeyRequired(repo *db.Repo) bool {
	s, err := repo.GetSettings()
	if err != nil || s == nil || s.RequireAPIKey == nil {
		return true
	}
	return *s.RequireAPIKey
}
