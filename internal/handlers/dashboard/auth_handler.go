package dashboard

import (
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
)

// Handler fields added for dashboard auth (see auth_limiter.go / auth_session.go).
//
// hasher and limiter are interfaces/pointers so tests can inject a cheap hash and
// a controllable clock without mutating package-level state.
type authDeps struct {
	hasher   PasswordHasher
	limiter  *loginLimiter
	secret   []byte
	secretEr error
}

// resetHint mirrors the reference's guidance for a locked-out operator.
const resetHint = "Forgot password? Reset it to the default from the CLI (Settings → Reset Password to Default)."

// HandleAuthSession reports whether the *current request* carries a valid
// session. The dashboard calls it on every page load: the session cookie is
// httpOnly, so JavaScript cannot inspect it, and without this check a refresh
// would show the password prompt even though the cookie is still valid.
func (h *Handler) HandleAuthSession(w http.ResponseWriter, r *http.Request) {
	if !h.isAuthenticated(r) {
		handlerutil.WriteJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}

// HandleAuthStatus reports whether the dashboard demands a login. The UI calls
// this before rendering so it can skip the password form when login is disabled.
func (h *Handler) HandleAuthStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := h.repo.GetSettings()
	if err != nil {
		settings = nil
	}
	requireLogin := true
	hasPassword := false
	if settings != nil {
		if settings.RequireLogin != nil {
			requireLogin = *settings.RequireLogin
		}
		hasPassword = settings.PasswordHash != ""
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"requireLogin": requireLogin,
		"hasPassword":  hasPassword,
	})
}

// HandleAuthLogin verifies the submitted password and, on success, sets the
// session cookie. Failures are rate limited per IP with escalating lockouts.
func (h *Handler) HandleAuthLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, strings.EqualFold(os.Getenv("TRUST_PROXY"), "true"))
	if locked, retryAfter := h.auth.limiter.checkLock(ip); locked {
		seconds := int(retryAfter.Seconds()) + 1
		w.Header().Set("Retry-After", itoa(seconds))
		handlerutil.WriteJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":      "Too many failed attempts. Try again in " + itoa(seconds) + "s. " + resetHint,
			"retryAfter": seconds,
			"resetHint":  resetHint,
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	settings, _ := h.repo.GetSettings()
	storedHash := ""
	if settings != nil {
		storedHash = settings.PasswordHash
	}

	if !VerifyPassword(h.auth.hasher, storedHash, req.Password) {
		remaining := h.auth.limiter.recordFail(ip)
		if locked, retryAfter := h.auth.limiter.checkLock(ip); locked {
			seconds := int(retryAfter.Seconds()) + 1
			w.Header().Set("Retry-After", itoa(seconds))
			handlerutil.WriteJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":      "Too many failed attempts. Try again in " + itoa(seconds) + "s. " + resetHint,
				"retryAfter": seconds,
				"resetHint":  resetHint,
			})
			return
		}
		handlerutil.WriteJSON(w, http.StatusUnauthorized, map[string]any{
			"error":               "Invalid password. " + itoa(remaining) + " attempt(s) left before lockout.",
			"remainingBeforeLock": remaining,
		})
		return
	}

	token, err := CreateSessionToken(h.auth.secret, time.Now())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to create session")
		return
	}
	h.auth.limiter.recordSuccess(ip)
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   shouldUseSecureCookie(r),
		MaxAge:   int(authSessionTTL.Seconds()),
	})
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleAuthLogout clears the session cookie.
func (h *Handler) HandleAuthLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   shouldUseSecureCookie(r),
		MaxAge:   -1,
	})
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleAuthChangePassword sets a new dashboard password after verifying the
// current one. The first-time case (no hash yet) accepts the default password,
// mirroring VansRouter's settings route.
func (h *Handler) HandleAuthChangePassword(w http.ResponseWriter, r *http.Request) {
	if !h.isAuthenticated(r) {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(req.NewPassword) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "New password is required")
		return
	}

	settings, _ := h.repo.GetSettings()
	storedHash := ""
	if settings != nil {
		storedHash = settings.PasswordHash
	}
	if !VerifyPassword(h.auth.hasher, storedHash, req.CurrentPassword) {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Invalid current password")
		return
	}

	hash, err := h.auth.hasher.Hash(req.NewPassword)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to hash password")
		return
	}
	if err := h.repo.SetPasswordHash(hash); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to save password")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleAuthResetPassword restores the default password by clearing the stored
// hash. Local-only, matching the reference's guard.
func (h *Handler) HandleAuthResetPassword(w http.ResponseWriter, r *http.Request) {
	if !isLocalRequest(r) {
		handlerutil.WriteJSONError(w, http.StatusForbidden, "Local only")
		return
	}
	if err := h.repo.SetPasswordHash(""); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to reset password")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// isAuthenticated reports whether the request carries a valid session cookie, or
// whether login is disabled entirely in settings.
func (h *Handler) isAuthenticated(r *http.Request) bool {
	if c, err := r.Cookie(authCookieName); err == nil && c.Value != "" {
		if VerifySessionToken(h.auth.secret, c.Value, time.Now()) {
			return true
		}
	}
	if settings, err := h.repo.GetSettings(); err == nil && settings != nil {
		if settings.RequireLogin != nil && !*settings.RequireLogin {
			return true
		}
	}
	return false
}

// shouldUseSecureCookie marks the cookie Secure when the request arrived over
// HTTPS (directly or via a trusted proxy), or when forced by env.
func shouldUseSecureCookie(r *http.Request) bool {
	if strings.EqualFold(os.Getenv("AUTH_COOKIE_SECURE"), "true") {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// isLocalRequest reports whether the request originated from the local machine.
func isLocalRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		host = r.RemoteAddr
	}
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	if strings.HasPrefix(host, "::ffff:127.") {
		return true
	}
	return false
}

// itoa is a tiny int→string helper to avoid importing strconv at every call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
