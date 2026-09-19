package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// postJSON is a small helper for the auth endpoints.
func postJSON(t *testing.T, r http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	return m
}

func TestAuthStatusDefaultsToRequireLogin(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	req := httptest.NewRequest("GET", "/api/dashboard/auth/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := decodeMap(t, w)
	if m["requireLogin"] != true {
		t.Fatalf("requireLogin = %v, want true by default", m["requireLogin"])
	}
	if m["hasPassword"] != false {
		t.Fatalf("hasPassword = %v, want false before any password is set", m["hasPassword"])
	}
}

func TestAuthLoginWithDefaultPasswordSetsCookie(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	w := postJSON(t, r, "/api/dashboard/auth/login", `{"password":"123456"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200: %s", w.Code, w.Body.String())
	}
	if m := decodeMap(t, w); m["success"] != true {
		t.Fatalf("success = %v, want true", m["success"])
	}
	cookies := w.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == authCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("login must set the auth_token cookie")
	}
	if !session.HttpOnly {
		t.Error("the session cookie must be HttpOnly")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", session.SameSite)
	}
}

func TestAuthLoginRejectsWrongPassword(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	w := postJSON(t, r, "/api/dashboard/auth/login", `{"password":"nope"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("login = %d, want 401", w.Code)
	}
	m := decodeMap(t, w)
	if m["error"] == nil {
		t.Error("a failed login must explain itself")
	}
	if _, ok := m["remainingBeforeLock"]; !ok {
		t.Error("a failed login should report the remaining attempt budget")
	}
}

// TestAuthLoginLocksOutAfterRepeatedFailures pins the rate limit end to end.
func TestAuthLoginLocksOutAfterRepeatedFailures(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	var last *httptest.ResponseRecorder
	for i := 0; i < loginMaxFailsBeforeLock; i++ {
		last = postJSON(t, r, "/api/dashboard/auth/login", `{"password":"nope"}`)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures status = %d, want 429", loginMaxFailsBeforeLock, last.Code)
	}
	if last.Header().Get("Retry-After") == "" {
		t.Error("a 429 must carry Retry-After")
	}
	// Even the correct password is refused while locked, otherwise the lock is
	// trivially bypassable.
	w := postJSON(t, r, "/api/dashboard/auth/login", `{"password":"123456"}`)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d while locked, want 429", w.Code)
	}
}

func TestAuthChangePasswordRequiresSession(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	w := postJSON(t, r, "/api/dashboard/auth/change-password", `{"currentPassword":"123456","newPassword":"newpass"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a session", w.Code)
	}
}

func TestAuthChangePasswordThenLoginWithNewPassword(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// Log in to obtain a session cookie.
	login := postJSON(t, r, "/api/dashboard/auth/login", `{"password":"123456"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("initial login failed: %d", login.Code)
	}
	session := ""
	for _, c := range login.Result().Cookies() {
		if c.Name == authCookieName {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("no session cookie from login")
	}

	// Change the password using the session.
	req := httptest.NewRequest("POST", "/api/dashboard/auth/change-password",
		bytes.NewBufferString(`{"currentPassword":"123456","newPassword":"s3cret!"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: session})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("change-password = %d, want 200: %s", w.Code, w.Body.String())
	}

	// The hash must be persisted, and the old default must stop working.
	settings, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.PasswordHash == "" {
		t.Fatal("the password hash was not stored")
	}
	if settings.PasswordHash == "s3cret!" {
		t.Fatal("the password must be stored hashed, never in plaintext")
	}
	if w := postJSON(t, r, "/api/dashboard/auth/login", `{"password":"123456"}`); w.Code == http.StatusOK {
		t.Fatal("the default password must stop working after a change")
	}
	if w := postJSON(t, r, "/api/dashboard/auth/login", `{"password":"s3cret!"}`); w.Code != http.StatusOK {
		t.Fatalf("the new password must work, got %d", w.Code)
	}
}

// TestAuthSessionReflectsCookie pins the refresh-safe session check: the login
// cookie is httpOnly, so the UI must ask the server whether it is still signed
// in. Without this the page re-prompted for the password on every reload.
func TestAuthSessionReflectsCookie(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	// No cookie -> not authenticated.
	req := httptest.NewRequest("GET", "/api/dashboard/auth/session", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d without a cookie, want 401", w.Code)
	}
	if m := decodeMap(t, w); m["authenticated"] != false {
		t.Fatalf("authenticated = %v, want false", m["authenticated"])
	}

	// Log in, then reuse the cookie on a *fresh* request to model a page reload.
	login := postJSON(t, r, "/api/dashboard/auth/login", `{"password":"123456"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login failed: %d", login.Code)
	}
	var session *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == authCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("login did not set a session cookie")
	}

	req = httptest.NewRequest("GET", "/api/dashboard/auth/session", nil)
	req.AddCookie(session)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d with a valid cookie, want 200", w.Code)
	}
	if m := decodeMap(t, w); m["authenticated"] != true {
		t.Fatalf("authenticated = %v, want true", m["authenticated"])
	}

	// A tampered cookie must not authenticate.
	req = httptest.NewRequest("GET", "/api/dashboard/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: session.Value + "x"})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d for a tampered cookie, want 401", w.Code)
	}
}

func TestAuthLogoutClearsCookie(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	w := postJSON(t, r, "/api/dashboard/auth/logout", ``)
	if w.Code != http.StatusOK {
		t.Fatalf("logout = %d, want 200", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == authCookieName {
			if c.MaxAge >= 0 && c.Value != "" {
				t.Fatalf("logout must expire the cookie, got value=%q maxAge=%d", c.Value, c.MaxAge)
			}
			return
		}
	}
	t.Fatal("logout must emit a clearing Set-Cookie")
}

func TestLoginDisabledSkipsSessionRequirement(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	if err := repo.SetRequireLogin(boolPtr(false)); err != nil {
		t.Fatalf("SetRequireLogin: %v", err)
	}

	// status must report the override so the UI can skip the form.
	req := httptest.NewRequest("GET", "/api/dashboard/auth/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if m := decodeMap(t, w); m["requireLogin"] != false {
		t.Fatalf("requireLogin = %v, want false", m["requireLogin"])
	}
}

func boolPtr(b bool) *bool { return &b }

// The reset endpoint must not be reachable with nothing but a local-looking
// request. Behind Cloudflare Tunnel (or any reverse proxy on the same host)
// every public request arrives from 127.0.0.1, so an unconditional local guard
// let anyone who knew the domain clear the password and sign in with the
// default. It now requires the current password.
func TestAuthResetPasswordRequiresCurrentPassword(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// A fresh test DB has no hash, so install one: the point is that an
	// unauthenticated request cannot clear a hash that is set.
	const initial = "$2b$10$abcdefghijklmnopqrstuv"
	if err := repo.SetPasswordHash(initial); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}

	// No body at all — the shape an attacker sends.
	w := postJSON(t, r, "/api/dashboard/auth/reset-password", `{}`)
	if w.Code == http.StatusOK {
		t.Fatal("reset-password succeeded without the current password: anyone behind a proxy can take over the dashboard")
	}

	settings, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.PasswordHash != initial {
		t.Fatal("the stored password hash was cleared by an unauthenticated request")
	}

	wrong := postJSON(t, r, "/api/dashboard/auth/reset-password", `{"currentPassword":"not-the-password"}`)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a wrong current password", wrong.Code)
	}
	settings, _ = repo.GetSettings()
	if settings.PasswordHash != initial {
		t.Fatal("a wrong current password still reset the hash")
	}
}

// The reverse-proxy switches must be reachable from the dashboard, not only
// from the environment: the whole point is to configure Cloudflare without
// editing a unit file. Exercised through the real routes.
func TestProxySettingsRoundTripThroughHTTP(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	// Default: off, so the assertions below prove the POST changed something.
	var before map[string]any
	{
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/dashboard/settings", nil))
		if err := json.Unmarshal(w.Body.Bytes(), &before); err != nil {
			t.Fatalf("GET /settings is not JSON: %v (%s)", err, w.Body.String())
		}
	}
	if before["trustProxy"] != false || before["authCookieSecure"] != false {
		t.Fatalf("proxy switches default to on (%v/%v); the test cannot prove a change",
			before["trustProxy"], before["authCookieSecure"])
	}
	if _, ok := before["listenHost"]; !ok {
		t.Fatal("GET /settings does not report listenHost, so the UI cannot show the bind address")
	}

	w := postJSON(t, r, "/api/dashboard/settings", `{"trustProxy":true,"authCookieSecure":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /settings = %d: %s", w.Code, w.Body.String())
	}

	var after map[string]any
	{
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/dashboard/settings", nil))
		if err := json.Unmarshal(w.Body.Bytes(), &after); err != nil {
			t.Fatalf("GET /settings after save is not JSON: %v", err)
		}
	}
	if after["trustProxy"] != true {
		t.Error("trustProxy did not persist; the Cloudflare toggle would silently do nothing")
	}
	if after["authCookieSecure"] != true {
		t.Error("authCookieSecure did not persist")
	}
}

// A Secure cookie must actually be emitted by the login route once the setting
// is on. Asserting the helper alone would pass even if the handler kept passing
// the old environment-derived value.
func TestLoginCookieIsSecureWhenSettingEnabled(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	login := postJSON(t, r, "/api/dashboard/auth/login", `{"password":"123456"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login failed: %d", login.Code)
	}
	for _, c := range login.Result().Cookies() {
		if c.Name == authCookieName && c.Secure {
			t.Fatal("login cookie is already Secure by default; the setting is not what enabled it")
		}
	}

	if w := postJSON(t, r, "/api/dashboard/settings", `{"authCookieSecure":true}`); w.Code != http.StatusOK {
		t.Fatalf("enabling the setting failed: %d", w.Code)
	}

	login = postJSON(t, r, "/api/dashboard/auth/login", `{"password":"123456"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login after enabling failed: %d", login.Code)
	}
	found := false
	for _, c := range login.Result().Cookies() {
		if c.Name == authCookieName {
			found = true
			if !c.Secure {
				t.Fatal("the login cookie is not Secure even though authCookieSecure is on")
			}
		}
	}
	if !found {
		t.Fatal("no session cookie in the login response")
	}
}
