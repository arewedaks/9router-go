package oauth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

// sealToSession encrypts a token the way zed.dev does, using the public key the
// authorize call handed out.
func sealToSession(t *testing.T, sessionID, token string) string {
	t.Helper()
	s := providers.ZedGetSession(sessionID)
	if s == nil {
		t.Fatalf("session %q is gone", sessionID)
	}
	der, err := base64.RawURLEncoding.DecodeString(s.PublicKey)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	pub, err := x509.ParsePKCS1PublicKey(der)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	sealed, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, []byte(token), nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(sealed)
}

// Local installs: the loopback listener takes the redirect and the panel's poll
// stores the account without the operator copying anything.
func TestZedAuthorizeCapturesLoopbackCallback(t *testing.T) {
	providers.ResetZedSessions()
	t.Cleanup(providers.ResetZedSessions)
	repo := newDuplicateTestRepo(t)
	h := NewOAuthHandler(repo)

	rec := httptest.NewRecorder()
	h.HandleZedAuthorize(rec, httptest.NewRequest(http.MethodPost, "/api/oauth/zed/authorize", strings.NewReader(`{"name":"box"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize status = %d: %s", rec.Code, rec.Body.String())
	}
	var auth struct {
		Session     string `json:"session"`
		Port        int    `json:"port"`
		SignInURL   string `json:"signInUrl"`
		AutoCapture bool   `json:"autoCapture"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &auth); err != nil {
		t.Fatalf("decode authorize: %v", err)
	}
	if auth.Session == "" || auth.Port == 0 || auth.AutoCapture == false {
		t.Fatalf("authorize = %+v", auth)
	}
	if !strings.HasPrefix(auth.SignInURL, "https://zed.dev/native_app_signin?") {
		t.Fatalf("sign-in url = %q", auth.SignInURL)
	}

	// Pending until the browser comes back.
	statusURL := "/api/oauth/zed/status?session=" + auth.Session
	rec = httptest.NewRecorder()
	h.HandleZedStatus(rec, httptest.NewRequest(http.MethodGet, statusURL, nil))
	if !strings.Contains(rec.Body.String(), `"pending":true`) {
		t.Fatalf("status before callback = %s", rec.Body.String())
	}

	// zed.dev redirects the browser to the loopback port with the sealed token.
	sealed := sealToSession(t, auth.Session, "zed-token-1")
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // the 302 goes to zed.dev
		},
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/?user_id=user-1&access_token=%s", auth.Port, sealed))
	if err != nil {
		t.Fatalf("loopback callback: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback answered %d, want a redirect to the success page", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != providers.ZedSignInDoneURL {
		t.Errorf("redirect = %q", loc)
	}

	rec = httptest.NewRecorder()
	h.HandleZedStatus(rec, httptest.NewRequest(http.MethodGet, statusURL, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("status after callback = %d %s", rec.Code, rec.Body.String())
	}

	conns, err := repo.GetProviderConnections("zed", false)
	if err != nil || len(conns) != 1 {
		t.Fatalf("connections = %d, err %v", len(conns), err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conns[0].Data), &data); err != nil {
		t.Fatalf("decode connection: %v", err)
	}
	// Zed authenticates as "<user_id> <token>", so both halves must be kept.
	if data["accessToken"] != "zed-token-1" || data["zedUserId"] != "user-1" {
		t.Fatalf("stored data = %v", data)
	}

	// A second poll must not create a second account for the same sign-in.
	rec = httptest.NewRecorder()
	h.HandleZedStatus(rec, httptest.NewRequest(http.MethodGet, statusURL, nil))
	if conns, _ := repo.GetProviderConnections("zed", false); len(conns) != 1 {
		t.Fatalf("duplicate connection after re-poll: %d", len(conns))
	}
}

// Remote installs: the operator pastes the address the browser landed on.
func TestZedExchangeAcceptsPastedCallback(t *testing.T) {
	providers.ResetZedSessions()
	t.Cleanup(providers.ResetZedSessions)
	repo := newDuplicateTestRepo(t)
	h := NewOAuthHandler(repo)

	rec := httptest.NewRecorder()
	h.HandleZedAuthorize(rec, httptest.NewRequest(http.MethodPost, "/api/oauth/zed/authorize", strings.NewReader(`{}`)))
	var auth struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &auth)

	sealed := sealToSession(t, auth.Session, "zed-token-2")
	paste := fmt.Sprintf("http://127.0.0.1:53682/?user_id=user-2&access_token=%s", sealed)
	body, _ := json.Marshal(map[string]string{"session": auth.Session, "callback": paste})
	rec = httptest.NewRecorder()
	h.HandleZedExchange(rec, httptest.NewRequest(http.MethodPost, "/api/oauth/zed/exchange", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("exchange = %d %s", rec.Code, rec.Body.String())
	}
	conns, err := repo.GetProviderConnections("zed", false)
	if err != nil || len(conns) != 1 {
		t.Fatalf("connections = %d, err %v", len(conns), err)
	}

	// Garbage or an expired session must be refused, not stored.
	rec = httptest.NewRecorder()
	h.HandleZedExchange(rec, httptest.NewRequest(http.MethodPost, "/api/oauth/zed/exchange",
		strings.NewReader(`{"session":"nope","callback":"http://127.0.0.1:53682/?user_id=u&access_token=t"}`)))
	if rec.Code != http.StatusGone {
		t.Fatalf("expired session answered %d, want 410", rec.Code)
	}
}

// The panel needs the flow shape before it renders anything.
func TestZedFlowIsNativeApp(t *testing.T) {
	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleZedFlow(rec, httptest.NewRequest(http.MethodGet, "/api/oauth/zed/flow", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), `"kind":"native_app"`) {
		t.Fatalf("flow body = %s", body)
	}
}
