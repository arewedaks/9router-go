package dashboard

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sessionFlag performs an authenticated call using the built-in default
// password, which is exactly the state a fresh install is in.
func sessionFlag(t *testing.T, r http.Handler, path string) map[string]any {
	t.Helper()

	// Sign in first: the session cookie is httpOnly, so the flag has to be read
	// through a real authenticated request rather than by inspecting a cookie.
	login := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/dashboard/auth/login",
		strings.NewReader(`{"password":"123456"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.RemoteAddr = "127.0.0.1:1234"
	r.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("login with the default password = %d, want 200: %s", login.Code, login.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range login.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d, want 200: %s", path, rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// A fresh install must advertise that it still uses the built-in password,
// otherwise the dashboard has no way to warn the operator.
func TestAuthSessionFlagsDefaultPassword(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	got := sessionFlag(t, r, "/api/dashboard/auth/session")
	if got["authenticated"] != true {
		t.Fatalf("authenticated = %v, want true", got["authenticated"])
	}
	if got["usingDefaultPassword"] != true {
		t.Errorf("usingDefaultPassword = %v, want true on a fresh install", got["usingDefaultPassword"])
	}
}

// The pre-login status route drives the warning shown on the login screen, so it
// must report the same fact without requiring a session.
func TestAuthStatusFlagsDefaultPassword(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/dashboard/auth/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["usingDefaultPassword"] != true {
		t.Errorf("usingDefaultPassword = %v, want true", got["usingDefaultPassword"])
	}
}

// Once a real password is stored the flag must clear, or the warning would nag
// forever and stop meaning anything.
func TestDefaultPasswordFlagClearsAfterChange(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	// Changing the password requires a session, so sign in with the default
	// first — the state a fresh install is in.
	login := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/dashboard/auth/login",
		strings.NewReader(`{"password":"123456"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("login with the default password = %d, want 200", login.Code)
	}

	change := httptest.NewRecorder()
	changeReq := httptest.NewRequest(http.MethodPost, "/api/dashboard/auth/change-password",
		strings.NewReader(`{"currentPassword":"123456","newPassword":"a-real-password"}`))
	changeReq.Header.Set("Content-Type", "application/json")
	for _, c := range login.Result().Cookies() {
		changeReq.AddCookie(c)
	}
	r.ServeHTTP(change, changeReq)
	if change.Code != http.StatusOK {
		t.Fatalf("change-password = %d, want 200: %s", change.Code, change.Body.String())
	}

	// Sign in with the NEW password to read the flag on a genuinely configured
	// instance.
	login2 := httptest.NewRecorder()
	loginReq2 := httptest.NewRequest(http.MethodPost, "/api/dashboard/auth/login",
		strings.NewReader(`{"password":"a-real-password"}`))
	loginReq2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(login2, loginReq2)
	if login2.Code != http.StatusOK {
		t.Fatalf("login with the new password = %d, want 200", login2.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/auth/session", nil)
	for _, c := range login2.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["usingDefaultPassword"] != false {
		t.Errorf("usingDefaultPassword = %v, want false after a real password is set", got["usingDefaultPassword"])
	}
}

// The API must never seed a key on its own: keys are created by the operator.
// This is the invariant behind "keys are user-created, not provided".
func TestNoApiKeyExistsBeforeOperatorCreatesOne(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// Touch the authenticated surface so nothing key-related could be created as
	// a side effect of signing in.
	sessionFlag(t, r, "/api/dashboard/auth/session")

	keys, err := repo.GetAllApiKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("a fresh install must have zero API keys, got %d", len(keys))
	}
}
