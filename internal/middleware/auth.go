package middleware

import (
	"9router/proxy/internal/log"
	"context"
	"net/http"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
)

// ContextKey is a custom type for context keys to avoid collisions.
type ContextKey string

// ApiKeyContextKey is the context key for the authenticated API key object.
const ApiKeyContextKey ContextKey = "apiKey"

// RequireApiKey creates a middleware handler that authenticates requests using client API keys.
// It checks the Authorization header (Bearer <key>) and the query parameter `key`.
// Valid keys are retrieved from the SQLite database; inactive or disabled keys are rejected with 401.
//
// A valid dashboard session cookie is also accepted. The browser-facing OAuth
// endpoints (add-account flows) live behind this middleware, and once the
// operator signs in with the dashboard password the session cookie is the only
// credential the page holds — it has no API key to send. Without this the
// "Sign in" button on every OAuth provider returned 401 to a logged-in user.
func RequireApiKey(repo *db.Repo) func(http.Handler) http.Handler {
	return RequireApiKeyWithSession(repo, nil)
}

// RequireApiKeyWithSession is RequireApiKey plus an optional session verifier.
// When verifySession is non-nil a valid `auth_token` cookie is accepted as an
// alternative credential; passing nil restores API-key-only behaviour.
//
// sessionPaths limits the cookie to browser-facing UI endpoints. The cookie must
// NOT unlock the engine routes (`/v1`, `/chat/completions`, …) that share this
// middleware: those are machine credentials, consumed by clients that carry an
// API key, and a browser cookie leaking into them would broaden the credential's
// reach for no benefit. An empty list means "every path".
func RequireApiKeyWithSession(repo *db.Repo, verifySession func(token string) bool, sessionPaths ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Dashboard session first: it is the credential a logged-in browser
			// actually has, and it is cheaper to check than a DB round-trip.
			if verifySession != nil && sessionPathAllowed(r.URL.Path, sessionPaths) {
				if c, err := r.Cookie("auth_token"); err == nil && c.Value != "" && verifySession(c.Value) {
					next.ServeHTTP(w, r)
					return
				}
			}

			apiKeyString := ExtractApiKey(r)
			if apiKeyString == "" {
				handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Authentication required. Provide an API key via Authorization: Bearer <key> header or ?key=<key> query parameter.")
				return
			}

			// Validate via SQLite repository and retrieve details
			apiKeyObj, err := repo.GetApiKeyByKey(apiKeyString)
			if err != nil {
				log.Error("auth", "DB lookup error", "error", err)
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			if apiKeyObj == nil {
				handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Invalid API key.")
				return
			}

			if apiKeyObj.IsActive != 1 {
				handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Invalid or inactive API key.")
				return
			}

			// Inject API Key info into the request context for downstream handlers/logging
			ctx := context.WithValue(r.Context(), ApiKeyContextKey, apiKeyObj)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// sessionPathAllowed reports whether a session cookie may authenticate this
// path. An empty allow-list means every path is eligible (used when the whole
// router is browser-facing). Prefix matching keeps the OAuth add-account flows
// covered without opening the engine surface.
func sessionPathAllowed(path string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, p := range allowed {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// GetAuthenticatedApiKey retrieves the authenticated APIKey object from the request context.
func GetAuthenticatedApiKey(r *http.Request) *models.APIKey {
	val := r.Context().Value(ApiKeyContextKey)
	if val == nil {
		return nil
	}
	keyObj, ok := val.(*models.APIKey)
	if !ok {
		return nil
	}
	return keyObj
}

// WithApiKeyForTest returns a context carrying the given key, exactly as the
// auth middleware would leave it. Exported so handler packages can exercise
// per-key authorisation without standing up the whole middleware chain.
func WithApiKeyForTest(ctx context.Context, key *models.APIKey) context.Context {
	return context.WithValue(ctx, ApiKeyContextKey, key)
}

// ExtractApiKey extracts the client API key from the request.
// Only header-based auth is accepted — keys in query strings would leak via
// browser history, referrers, and upstream proxy logs.
func ExtractApiKey(r *http.Request) string {
	// 1. Try Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
			return strings.TrimSpace(parts[1])
		}
	}

	// 2. Try custom X-API-Key header as fallback
	if xApiKey := r.Header.Get("X-API-Key"); xApiKey != "" {
		return xApiKey
	}

	return ""
}
