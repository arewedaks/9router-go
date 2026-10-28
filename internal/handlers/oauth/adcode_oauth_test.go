package oauth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// ADCode signs in through Firebase inside its own desktop app, so the dashboard
// must offer the import panel (a refresh token), not an API-key field and not a
// device code this server cannot drive.
func TestAdCodeFlowIsImportWithARefreshTokenLabel(t *testing.T) {
	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/oauth/adcode/flow", nil)
	req.SetPathValue("provider", "adcode")
	h.HandleGenericFlowInfo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, needle := range []string{`"kind":"import_token"`, `"label":"Refresh token"`, "identity.json"} {
		if !strings.Contains(body, needle) {
			t.Errorf("flow body %s is missing %s", body, needle)
		}
	}
}

// A pasted refresh token is proven by exchanging it before the row is stored —
// otherwise the dashboard reports a healthy connection and every call then
// fails with 401 missing-token.
func TestAdCodeImportRejectsATokenThatCannotBeExchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"INVALID_REFRESH_TOKEN"}}`)
	}))
	t.Cleanup(srv.Close)
	origURL := providers.AdCodeTokenURL
	providers.AdCodeTokenURL = srv.URL
	t.Cleanup(func() { providers.AdCodeTokenURL = origURL })
	providers.ResetAdCodeTokenCache()

	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/adcode/exchange",
		strings.NewReader(`{"accessToken":"dead-refresh-token","name":"stale"}`))
	req.SetPathValue("provider", "adcode")
	h.HandleGenericExchange(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "INVALID_REFRESH_TOKEN") {
		t.Errorf("body %s does not name the upstream code", rec.Body.String())
	}
}
