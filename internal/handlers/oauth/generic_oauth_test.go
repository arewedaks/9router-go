package oauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A provider with a spec must report its flow kind without starting a flow.
// Probing /authorize instead would mint a device code just to learn the kind.
func TestHandleGenericFlowInfo_ReportsKind(t *testing.T) {
	cases := []struct {
		provider  string
		wantKind  flowKind
		wantApp   bool
	}{
		{"kimi", flowDeviceCode, false},
		{"grok-cli", flowDeviceCode, false},
		{"claude", flowAuthCode, false},
		{"iflow", flowAuthCode, false},
		{"xai", flowAuthCode, false},
		{"cursor", flowImport, false},
		{"codex", flowImport, false},
		// gitlab ships no built-in OAuth app, so the panel must ask for one.
		{"gitlab", flowAuthCode, true},
	}

	h := NewOAuthHandler(nil)
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/api/oauth/"+c.provider+"/flow", nil)
			req.SetPathValue("provider", c.provider)
			h.HandleGenericFlowInfo(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, `"kind":"`+string(c.wantKind)+`"`) {
				t.Errorf("body %s does not report kind %s", body, c.wantKind)
			}
			hasApp := strings.Contains(body, `"needsApp":true`)
			if hasApp != c.wantApp {
				t.Errorf("needsApp = %v, want %v (body %s)", hasApp, c.wantApp, body)
			}
		})
	}
}

// A provider with no spec must 404 rather than pretend it has a flow: the panel
// falls back to the paste form, which is the honest answer.
func TestHandleGenericFlowInfo_UnknownProvider(t *testing.T) {
	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/oauth/not-a-provider/flow", nil)
	req.SetPathValue("provider", "not-a-provider")
	h.HandleGenericFlowInfo(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// Qoder, kilocode and zed deliberately have no spec: their flows do not fit the
// generic shapes (locally-generated identity, GET-poll-by-status, RSA keypair).
// A spec they cannot execute would be worse than none, so this pins the absence.
func TestGenericSpecs_OmitUnsupportedFlows(t *testing.T) {
	for _, provider := range []string{"qoder", "kilocode", "zed"} {
		if _, ok := specFor(provider); ok {
			t.Errorf("%s has a spec, but its flow is not a generic shape", provider)
		}
	}
}

// An import provider must reject /authorize: there is nothing to authorize, and
// a 200 with an empty body would leave the panel waiting forever.
func TestHandleGenericAuthorize_ImportProviderRejected(t *testing.T) {
	h := NewOAuthHandler(nil)
	for _, provider := range []string{"cursor", "codex"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/oauth/"+provider+"/authorize", nil)
		req.SetPathValue("provider", provider)
		h.HandleGenericAuthorize(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s authorize status = %d, want 400", provider, rec.Code)
		}
	}
}

// A provider needing an operator-registered app must fail with an actionable 400
// when no client id is supplied, not a 500 that reads as a server fault.
func TestHandleGenericAuthorize_NeedsAppWithoutClientID(t *testing.T) {
	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/oauth/gitlab/authorize", nil)
	req.SetPathValue("provider", "gitlab")
	h.HandleGenericAuthorize(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "clientId") {
		t.Errorf("message does not tell the operator what to supply: %s", rec.Body.String())
	}
}

// With a client id the consent URL must carry it, the scopes, PKCE, and the
// operator's base URL.
func TestHandleGenericAuthorize_GitLabWithClientID(t *testing.T) {
	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/oauth/gitlab/authorize?clientId=my-app&baseUrl=https://gitlab.example.com", nil)
	req.SetPathValue("provider", "gitlab")
	h.HandleGenericAuthorize(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"gitlab.example.com/oauth/authorize",
		"client_id=my-app",
		"code_challenge=",
		"api+read_user",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("consent URL missing %q: %s", want, body)
		}
	}
}

// Cursor's API needs a machine id; saving a token without one would produce a
// connection that can never authenticate.
func TestHandleGenericExchange_CursorRequiresMachineID(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/oauth/cursor/exchange",
		strings.NewReader(`{"accessToken":"tok"}`))
	req.SetPathValue("provider", "cursor")
	h.HandleGenericExchange(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "machineId") {
		t.Errorf("message does not name the missing field: %s", rec.Body.String())
	}
}

// A complete cursor import is saved with both halves of the credential.
func TestHandleGenericExchange_CursorImportSaves(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/oauth/cursor/exchange",
		strings.NewReader(`{"accessToken":"tok-abc","machineId":"abc123","name":"work"}`))
	req.SetPathValue("provider", "cursor")
	h.HandleGenericExchange(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var id, data string
	if err := repo.DB().QueryRow(
		"SELECT id, data FROM providerConnections WHERE provider = 'cursor'",
	).Scan(&id, &data); err != nil {
		t.Fatalf("read connection: %v", err)
	}
	if !strings.Contains(data, "tok-abc") {
		t.Errorf("stored data lost the token: %s", data)
	}
	if !strings.Contains(data, "abc123") {
		t.Errorf("stored data lost the machineId: %s", data)
	}
}

// An unknown provider must not be silently treated as a flow it does not have.
func TestHandleGenericExchange_UnknownProvider(t *testing.T) {
	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/oauth/nope/exchange", strings.NewReader(`{}`))
	req.SetPathValue("provider", "nope")
	h.HandleGenericExchange(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// isPendingProgress must accept every spelling the providers use, and reject a
// real token-bearing status.
func TestIsPendingProgress(t *testing.T) {
	for _, s := range []string{"authorization_pending", "slow_down", "pending"} {
		if !isPendingProgress(s) {
			t.Errorf("isPendingProgress(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "access_denied", "expired_token", "success"} {
		if isPendingProgress(s) {
			t.Errorf("isPendingProgress(%q) = true, want false", s)
		}
	}
}

// The PKCE verifier must survive the round trip between authorize and exchange,
// since the exchange request may arrive without one.
func TestGenericFlowStore_RoundTrip(t *testing.T) {
	storeGenericFlow("state-1", genericFlow{provider: "claude", verifier: "v1", redirectURI: "http://x/cb"})
	flow, ok := takeGenericFlow("state-1")
	if !ok {
		t.Fatal("stored flow not found")
	}
	if flow.verifier != "v1" || flow.redirectURI != "http://x/cb" {
		t.Errorf("flow = %+v, want verifier v1 and the stored redirect", flow)
	}
	// A state is one-shot: replaying it must not resurrect the verifier.
	if _, ok := takeGenericFlow("state-1"); ok {
		t.Error("flow was returned twice; a replayed state must not work")
	}
}
