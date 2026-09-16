package middleware

import (
	"net/http"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// DashboardSessionConfig parameterises RequireDashboardSession.
type DashboardSessionConfig struct {
	Repo *db.Repo
	// VerifySession reports whether a cookie value is a valid, unexpired session.
	// Injected rather than imported so the dashboard package owns token format
	// details and this middleware stays a thin gate.
	VerifySession func(token string) bool
	// AskLoginDisabled reports the effective requireLogin setting. When true the
	// dashboard is open and no credential is needed (VansRouter's requireLogin
	// escape hatch).
	LoginDisabled func() bool
	// LoginPath is where an unauthenticated browser is redirected for HTML
	// requests. API requests get a 401 JSON instead of a redirect.
	LoginPath string
}

// RequireDashboardSession guards the dashboard UI and its API.
//
// Three ways in, in order:
//
//  1. A valid session cookie (the normal browser path).
//  2. A valid API key (Authorization: Bearer sk-... / X-API-Key). Kept because
//     scripts, curl and CI used the key to drive the dashboard before login
//     existed; removing it would break them for no security gain — the key is
//     still the proxy credential and is checked against the same table.
//  3. login disabled in settings.
//
// Browsers are redirected to the login page; non-HTML callers receive JSON so an
// API client never has to parse an HTML login form.
func RequireDashboardSession(cfg DashboardSessionConfig) func(http.Handler) http.Handler {
	loginPath := cfg.LoginPath
	if loginPath == "" {
		loginPath = "/login"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.LoginDisabled != nil && cfg.LoginDisabled() {
				next.ServeHTTP(w, r)
				return
			}

			if c, err := r.Cookie("auth_token"); err == nil && c.Value != "" && cfg.VerifySession != nil {
				if cfg.VerifySession(c.Value) {
					next.ServeHTTP(w, r)
					return
				}
			}

			if key := ExtractApiKey(r); key != "" && cfg.Repo != nil {
				if obj, err := cfg.Repo.GetApiKeyByKey(key); err == nil && obj != nil && obj.IsActive == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}

			if wantsHTML(r) {
				http.Redirect(w, r, loginPath, http.StatusFound)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Authentication required. Sign in to the dashboard, or provide an API key.")
		})
	}
}

// wantsHTML reports whether the caller is a browser navigating to a page rather
// than an API client. Path prefixes are the reliable signal (the dashboard UI is
// served from /dashboard); an explicit Accept header is honoured as a fallback.
func wantsHTML(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/dashboard") {
		return false
	}
	accept := r.Header.Get("Accept")
	return accept == "" || strings.Contains(accept, "text/html")
}
