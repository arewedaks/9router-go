package oauth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/providers"
)

// ADCode's operator signs in with Google, so the provider must offer that flow
// too — not only the GitHub device grant.
func TestAdCodeOffersBothBrowserFlows(t *testing.T) {
	h := NewOAuthHandler(nil)

	cases := []struct {
		method   string
		wantKind flowKind
	}{
		{"", flowDeviceCode}, // default stays GitHub
		{"github", flowDeviceCode},
		{"google", flowAuthCode},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/oauth/adcode/flow?method="+c.method, nil)
		req.SetPathValue("provider", "adcode")
		h.HandleGenericFlowInfo(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("method %q: status %d: %s", c.method, rec.Code, rec.Body.String())
		}
		if body := rec.Body.String(); !strings.Contains(body, `"kind":"`+string(c.wantKind)+`"`) {
			t.Errorf("method %q reports %s, want %s", c.method, body, c.wantKind)
		}
	}
}

// Google only accepts a loopback redirect for this client — measured: any other
// host answers 400 redirect_uri_mismatch — so the consent URL must carry one, the
// vendor client id, PKCE and offline access.
func TestAdCodeGoogleAuthorizeUsesLoopbackAndPKCE(t *testing.T) {
	// The pair comes from the environment on a real install (the repo is public),
	// so the test supplies what the vendor app carries.
	origID, origSecret := providers.AdCodeGoogleClientID, providers.AdCodeGoogleClientSecret
	providers.AdCodeGoogleClientID = "test-google-client.apps.googleusercontent.com"
	providers.AdCodeGoogleClientSecret = "test-secret"
	t.Cleanup(func() {
		providers.AdCodeGoogleClientID, providers.AdCodeGoogleClientSecret = origID, origSecret
	})
	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/adcode/authorize?method=google", nil)
	req.SetPathValue("provider", "adcode")
	h.HandleGenericAuthorize(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		AuthURL  string `json:"authUrl"`
		State    string `json:"state"`
		Verifier string `json:"codeVerifier"`
		Redirect string `json:"redirectUri"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	u, err := url.Parse(resp.AuthURL)
	if err != nil {
		t.Fatalf("authUrl: %v", err)
	}
	if u.Host != "accounts.google.com" {
		t.Errorf("authUrl host = %q", u.Host)
	}
	q := u.Query()
	if q.Get("client_id") != providers.AdCodeGoogleClientID {
		t.Errorf("client_id = %q, want the vendor's Google client", q.Get("client_id"))
	}
	if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") {
		t.Errorf("redirect_uri = %q, want a loopback address", q.Get("redirect_uri"))
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Errorf("PKCE missing: %v", q)
	}
	if !strings.Contains(q.Get("scope"), "openid") || q.Get("access_type") != "offline" {
		t.Errorf("scope/offline params wrong: %v", q)
	}
	if resp.State == "" || resp.Verifier == "" || resp.Redirect == "" {
		t.Errorf("flow context incomplete: %+v", resp)
	}
}

// End to end for the Google half: Google answers with an id_token, Firebase
// trades it for a session, and the connection stores the refresh token.
func TestAdCodeGoogleExchangeStoresFirebaseSession(t *testing.T) {
	var gotPostBody, gotSecret atomic.Value
	googleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotSecret.Store(r.Form.Get("client_secret"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"google-access","id_token":"google-id","expires_in":3600,"token_type":"Bearer"}`)
	}))
	t.Cleanup(googleSrv.Close)

	idpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPostBody.Store(body["postBody"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"refreshToken":"firebase-google-refresh","idToken":"fb-id","email":"boz@gmail.com"}`)
	}))
	t.Cleanup(idpSrv.Close)

	origID, origSecret := providers.AdCodeGoogleClientID, providers.AdCodeGoogleClientSecret
	providers.AdCodeGoogleClientID = "test-google-client.apps.googleusercontent.com"
	providers.AdCodeGoogleClientSecret = "test-secret"
	origAuth, origToken := providers.AdCodeGoogleAuthorizeURL, providers.AdCodeGoogleTokenURL
	origIDP := providers.AdCodeIDPURL
	providers.AdCodeGoogleTokenURL = googleSrv.URL
	providers.AdCodeIDPURL = idpSrv.URL
	t.Cleanup(func() {
		providers.AdCodeGoogleAuthorizeURL, providers.AdCodeGoogleTokenURL = origAuth, origToken
		providers.AdCodeGoogleClientID, providers.AdCodeGoogleClientSecret = origID, origSecret
		providers.AdCodeIDPURL = origIDP
	})

	repo := newDuplicateTestRepo(t)
	h := NewOAuthHandler(repo)

	// 1. start the flow to learn state/verifier/redirect
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/adcode/authorize?method=google", nil)
	req.SetPathValue("provider", "adcode")
	h.HandleGenericAuthorize(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize: %d %s", rec.Code, rec.Body.String())
	}
	var start struct {
		State    string `json:"state"`
		Verifier string `json:"codeVerifier"`
		Redirect string `json:"redirectUri"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
		t.Fatalf("decode authorize: %v", err)
	}

	// 2. exchange the callback the operator pasted
	body, _ := json.Marshal(map[string]string{
		"method":       "google",
		"callbackUrl":  start.Redirect + "?code=google-code&state=" + start.State,
		"state":        start.State,
		"codeVerifier": start.Verifier,
		"redirectUri":  start.Redirect,
	})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/oauth/adcode/exchange", strings.NewReader(string(body)))
	req.SetPathValue("provider", "adcode")
	h.HandleGenericExchange(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange: %d %s", rec.Code, rec.Body.String())
	}

	if got := gotPostBody.Load().(string); !strings.Contains(got, "providerId=google.com") || !strings.Contains(got, "id_token=") {
		t.Errorf("Firebase hand-off = %q, want the Google id_token", got)
	}
	if got := gotSecret.Load().(string); got == "" {
		t.Error("Google token exchange went out without the client secret")
	}

	conns, err := repo.GetProviderConnections("adcode", false)
	if err != nil || len(conns) != 1 {
		t.Fatalf("connections = %d, err %v", len(conns), err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conns[0].Data), &data); err != nil {
		t.Fatalf("decode connection: %v", err)
	}
	if data["refreshToken"] != "firebase-google-refresh" {
		t.Fatalf("stored %v, want the Firebase refresh token", data)
	}
	if conns[0].Name == nil || *conns[0].Name != "boz@gmail.com" {
		t.Errorf("connection name = %v, want the Google account email", conns[0].Name)
	}
}
