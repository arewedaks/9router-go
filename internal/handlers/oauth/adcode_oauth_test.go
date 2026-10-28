package oauth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/providers"
)

// ADCode signs in through the browser: the editor uses GitHub's device grant
// (code on github.com/login/device) and hands the result to Firebase. The router
// must drive the same flow — a paste field for a refresh token is the wrong
// shape, and an API-key form is further off still.
func TestAdCodeFlowIsGitHubDeviceCode(t *testing.T) {
	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/oauth/adcode/flow", nil)
	req.SetPathValue("provider", "adcode")
	h.HandleGenericFlowInfo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"kind":"device_code"`) {
		t.Fatalf("flow body %s does not report a device code", body)
	}
}

// The device grant needs the vendor's GitHub client id, not one of ours: the
// Firebase project only accepts tokens minted for its own OAuth app.
func TestAdCodeDeviceRequestUsesTheVendorGitHubApp(t *testing.T) {
	var gotClient, gotScope atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotClient.Store(r.Form.Get("client_id"))
		gotScope.Store(r.Form.Get("scope"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"device_code":"dev-123","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`)
	}))
	t.Cleanup(srv.Close)
	orig := adcodeDeviceCodeURL
	adcodeDeviceCodeURL = srv.URL
	t.Cleanup(func() { adcodeDeviceCodeURL = orig })

	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/adcode/authorize", nil)
	req.SetPathValue("provider", "adcode")
	h.HandleGenericAuthorize(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		UserCode string `json:"userCode"`
		AuthURL  string `json:"authUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.UserCode != "ABCD-1234" || !strings.Contains(resp.AuthURL, "github.com/login/device") {
		t.Fatalf("device grant not surfaced: %+v", resp)
	}
	if got, _ := gotClient.Load().(string); got != "Ov23lienZUTAiNKAM0UV" {
		t.Errorf("client_id = %q, want the vendor's GitHub OAuth app", got)
	}
	if got, _ := gotScope.Load().(string); !strings.Contains(got, "read:user") {
		t.Errorf("scope = %q, want read:user user:email", got)
	}
}

// End to end: GitHub approves, Firebase exchanges the token for a session, and
// the connection stores the refresh token the executor needs.
func TestAdCodeDeviceFlowStoresTheFirebaseRefreshToken(t *testing.T) {
	var polls int32
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "device/code") {
			_, _ = io.WriteString(w, `{"device_code":"dev-123","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`)
			return
		}
		// First poll: still waiting. Second: the approved token.
		if atomic.AddInt32(&polls, 1) == 1 {
			_, _ = io.WriteString(w, `{"error":"authorization_pending"}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"gh-token","token_type":"bearer","scope":"read:user"}`)
	}))
	t.Cleanup(ghSrv.Close)

	var gotPostBody atomic.Value
	idpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPostBody.Store(body["postBody"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"refreshToken":"firebase-refresh","idToken":"firebase-id","email":"boz@example.com"}`)
	}))
	t.Cleanup(idpSrv.Close)

	origDevice, origToken, origIDP := adcodeDeviceCodeURL, adcodeTokenURL, providers.AdCodeIDPURL
	adcodeDeviceCodeURL, adcodeTokenURL = ghSrv.URL+"/device/code", ghSrv.URL+"/access_token"
	providers.AdCodeIDPURL = idpSrv.URL
	t.Cleanup(func() {
		adcodeDeviceCodeURL, adcodeTokenURL = origDevice, origToken
		providers.AdCodeIDPURL = origIDP
	})

	repo := newDuplicateTestRepo(t)
	h := NewOAuthHandler(repo)

	// poll until the mock answers with a token
	var lastBody string
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/oauth/adcode/exchange",
			strings.NewReader(`{"deviceCode":"dev-123","name":""}`))
		req.SetPathValue("provider", "adcode")
		h.HandleGenericExchange(rec, req)
		lastBody = rec.Body.String()
		// A pending poll also answers 200, so the body decides — not the status.
		if strings.Contains(lastBody, `"success":true`) {
			break
		}
	}
	if !strings.Contains(lastBody, `"success":true`) {
		t.Fatalf("exchange never completed: %s", lastBody)
	}

	if got, _ := gotPostBody.Load().(string); !strings.Contains(got, "providerId=github.com") {
		t.Errorf("Firebase hand-off postBody = %q, want the GitHub provider id", got)
	}

	conns, err := repo.GetProviderConnections("adcode", false)
	if err != nil || len(conns) != 1 {
		t.Fatalf("connections = %d, err %v", len(conns), err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conns[0].Data), &data); err != nil {
		t.Fatalf("decode connection: %v", err)
	}
	if data["refreshToken"] != "firebase-refresh" || data["accessToken"] != "firebase-refresh" {
		t.Fatalf("stored data %v, want the Firebase refresh token under both keys", data)
	}
	if conns[0].AuthType != "oauth" {
		t.Errorf("authType = %q, want oauth", conns[0].AuthType)
	}
}
