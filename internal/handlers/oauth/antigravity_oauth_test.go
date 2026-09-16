package oauth

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

func TestAntigravityAuthorize_BuildsGoogleConsentURL(t *testing.T) {
	h := &OAuthHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/antigravity/authorize?profile=cli", nil)

	h.HandleAntigravityAuthorize(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		AuthURL      string `json:"authUrl"`
		State        string `json:"state"`
		CodeVerifier string `json:"codeVerifier"`
		RedirectURI  string `json:"redirectUri"`
		Profile      string `json:"profile"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !strings.HasPrefix(out.AuthURL, antigravityAuthorizeURL+"?") {
		t.Errorf("authUrl should target Google consent, got %q", out.AuthURL)
	}
	parsed, err := url.Parse(out.AuthURL)
	if err != nil {
		t.Fatalf("parse authUrl: %v", err)
	}
	q := parsed.Query()
	if q.Get("client_id") == "" {
		t.Error("missing client_id")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("code_challenge") != antigravityPKCEChallenge(out.CodeVerifier) {
		t.Error("code_challenge does not match the returned verifier")
	}
	if q.Get("state") != out.State {
		t.Error("state mismatch between URL and response")
	}
	if q.Get("access_type") != "offline" {
		t.Errorf("access_type = %q, want offline (need a refresh token)", q.Get("access_type"))
	}
	// The openid scope routes Google into a hanging native-app consent flow.
	if strings.Contains(q.Get("scope"), "openid") {
		t.Errorf("scope must not contain openid, got %q", q.Get("scope"))
	}
	if out.Profile != "cli" {
		t.Errorf("profile = %q, want cli", out.Profile)
	}
	if out.State == "" || out.CodeVerifier == "" {
		t.Error("state and codeVerifier must be present")
	}
}

func TestAntigravityAuthorize_DefaultsToIDEAndLoopbackRedirect(t *testing.T) {
	h := &OAuthHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/antigravity/authorize", nil)

	h.HandleAntigravityAuthorize(rec, req)

	var out struct {
		AuthURL string `json:"authUrl"`
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Profile != "ide" {
		t.Errorf("default profile = %q, want ide", out.Profile)
	}
	parsed, _ := url.Parse(out.AuthURL)
	if got := parsed.Query().Get("redirect_uri"); !strings.HasPrefix(got, "http://localhost:") {
		t.Errorf("redirect_uri = %q, want a loopback URI", got)
	}
}

func TestAntigravityAuthorize_UnknownProfileFallsBackToIDE(t *testing.T) {
	h := &OAuthHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/antigravity/authorize?profile=bogus", nil)

	h.HandleAntigravityAuthorize(rec, req)

	var out struct {
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Profile != "ide" {
		t.Errorf("profile = %q, want ide fallback", out.Profile)
	}
}

func TestAntigravityExchange_RequiresCode(t *testing.T) {
	h := &OAuthHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/antigravity/exchange", strings.NewReader(`{}`))

	h.HandleAntigravityExchange(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAntigravityExchange_InvalidJSON(t *testing.T) {
	h := &OAuthHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/antigravity/exchange", strings.NewReader(`{not json`))

	h.HandleAntigravityExchange(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAntigravityExchange_UnknownStateWithoutVerifier(t *testing.T) {
	h := &OAuthHandler{}
	rec := httptest.NewRecorder()
	body := `{"code":"abc","state":"never-issued"}`
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/antigravity/exchange", strings.NewReader(body))

	h.HandleAntigravityExchange(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "codeVerifier") {
		t.Errorf("error should mention the missing verifier, got %s", rec.Body.String())
	}
}

func TestAntigravityExchange_ExtractsCodeFromCallbackURL(t *testing.T) {
	h := &OAuthHandler{}
	rec := httptest.NewRecorder()
	// No verifier anywhere → the handler must still get past code extraction
	// and fail on the verifier, proving it parsed the callback URL.
	body := `{"callbackUrl":"http://localhost:20128/oauth/antigravity/callback?code=xyz&state=s1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/antigravity/exchange", strings.NewReader(body))

	h.HandleAntigravityExchange(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no verifier)", rec.Code)
	}
	// Code extraction succeeded, so the failure must be about the verifier —
	// NOT about a missing/unparseable code.
	if !strings.Contains(rec.Body.String(), "missing codeVerifier") {
		t.Errorf("expected verifier error after successful extraction, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "no code in callbackUrl") {
		t.Errorf("code should have been extracted from callbackUrl, got %s", rec.Body.String())
	}
}

func TestAntigravityExchange_CallbackURLErrorParam(t *testing.T) {
	h := &OAuthHandler{}
	rec := httptest.NewRecorder()
	body := `{"callbackUrl":"http://localhost/cb?error=access_denied"}`
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/antigravity/exchange", strings.NewReader(body))

	h.HandleAntigravityExchange(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "access_denied") {
		t.Errorf("error should surface the upstream reason, got %s", rec.Body.String())
	}
}

func TestAntigravityFlow_StateIsSingleUse(t *testing.T) {
	storeAntigravityFlow("st-1", antigravityPendingFlow{verifier: "v1", profile: "cli"})
	if _, ok := takeAntigravityFlow("st-1"); !ok {
		t.Fatal("first take should succeed")
	}
	if _, ok := takeAntigravityFlow("st-1"); ok {
		t.Error("second take must fail: state is single-use")
	}
}

func TestAntigravityOnboardTierID_SelectionOrder(t *testing.T) {
	cases := []struct {
		name string
		data map[string]any
		want string
	}{
		{
			name: "paid tier wins",
			data: map[string]any{
				"paidTier":    map[string]any{"id": "paid-x"},
				"currentTier": map[string]any{"id": "cur-y"},
			},
			want: "paid-x",
		},
		{
			name: "current tier when no paid",
			data: map[string]any{"currentTier": map[string]any{"id": "cur-y"}},
			want: "cur-y",
		},
		{
			name: "default allowed tier",
			data: map[string]any{
				"allowedTiers": []any{
					map[string]any{"id": "free-z"},
					map[string]any{"id": "def", "isDefault": true},
				},
			},
			want: "def",
		},
		{
			name: "fallback legacy-tier",
			data: map[string]any{},
			want: "legacy-tier",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := antigravityOnboardTierID(tc.data); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractAntigravityProjectID(t *testing.T) {
	if got := extractAntigravityProjectID("proj-1"); got != "proj-1" {
		t.Errorf("string shape: got %q", got)
	}
	if got := extractAntigravityProjectID(map[string]any{"id": "proj-2"}); got != "proj-2" {
		t.Errorf("object shape: got %q", got)
	}
	if got := extractAntigravityProjectID(nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
	if got := extractAntigravityProjectID(map[string]any{"other": 1}); got != "" {
		t.Errorf("object without id: got %q", got)
	}
}

func TestAntigravityOAuthUserAgent_DiffersPerProfile(t *testing.T) {
	ide := antigravityOAuthUserAgent(providers.AntigravityProfileIDE)
	cli := antigravityOAuthUserAgent(providers.AntigravityProfileCLI)
	if ide == cli {
		t.Fatal("IDE and CLI must present different User-Agents")
	}
	if !strings.Contains(ide, "antigravity/ide/") {
		t.Errorf("IDE UA = %q", ide)
	}
	if !strings.Contains(cli, "antigravity/cli/") {
		t.Errorf("CLI UA = %q", cli)
	}
}

func TestAntigravityCallback_ErrorParam(t *testing.T) {
	h := &OAuthHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth/antigravity/callback?error=access_denied", nil)

	h.HandleAntigravityCallback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "access_denied") {
		t.Errorf("page should show the reason, got %s", rec.Body.String())
	}
}

func TestAntigravityCallback_MissingParams(t *testing.T) {
	h := &OAuthHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth/antigravity/callback?code=only", nil)

	h.HandleAntigravityCallback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAntigravityCallback_ExpiredState(t *testing.T) {
	h := &OAuthHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth/antigravity/callback?code=c&state=stale", nil)

	h.HandleAntigravityCallback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "expired") {
		t.Errorf("page should mention expiry, got %s", rec.Body.String())
	}
}

func TestAntigravityAuthorize_RedirectURIRoundTripsIntoFlow(t *testing.T) {
	h := &OAuthHandler{}
	redirect := "http://127.0.0.1:9999/oauth/antigravity/callback"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/antigravity/authorize?redirectUri="+url.QueryEscape(redirect), nil)

	h.HandleAntigravityAuthorize(rec, req)

	var out struct {
		AuthURL string `json:"authUrl"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	parsed, _ := url.Parse(out.AuthURL)
	if got := parsed.Query().Get("redirect_uri"); got != redirect {
		t.Errorf("redirect_uri = %q, want %q", got, redirect)
	}
	// The flow must remember the same URI so the token exchange matches.
	flow, ok := takeAntigravityFlow(out.State)
	if !ok {
		t.Fatal("flow not stored")
	}
	if flow.redirectURI != redirect {
		t.Errorf("stored redirectURI = %q, want %q", flow.redirectURI, redirect)
	}
}
