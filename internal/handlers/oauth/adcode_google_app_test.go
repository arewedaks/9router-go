package oauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// A release build cannot carry the vendor's Google OAuth pair (the repo is
// public), so a host that never received the environment used to fail at
// authorize time with "adcode needs an OAuth application" — the error a managed
// host showed. The flow must instead tell the panel to ask for the operator's
// own client.
func TestAdCodeGoogleFlowAsksForAppWhenUnconfigured(t *testing.T) {
	origID, origSecret := providers.AdCodeGoogleClientID, providers.AdCodeGoogleClientSecret
	providers.AdCodeGoogleClientID, providers.AdCodeGoogleClientSecret = "", ""
	t.Cleanup(func() {
		providers.AdCodeGoogleClientID, providers.AdCodeGoogleClientSecret = origID, origSecret
	})

	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/oauth/adcode/flow?method=google", nil)
	req.SetPathValue("provider", "adcode")
	h.HandleGenericFlowInfo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"needsApp":true`) {
		t.Fatalf("flow body %s does not ask for an OAuth app", body)
	}
	if !strings.Contains(body, "127.0.0.1:53682") {
		t.Errorf("appHint should name the redirect this flow uses: %s", body)
	}
}

// With the pair present the panel must stay on the plain Sign in path.
func TestAdCodeGoogleFlowNeedsNoAppWhenConfigured(t *testing.T) {
	origID, origSecret := providers.AdCodeGoogleClientID, providers.AdCodeGoogleClientSecret
	providers.AdCodeGoogleClientID = "test-client.apps.googleusercontent.com"
	providers.AdCodeGoogleClientSecret = "test-secret"
	t.Cleanup(func() {
		providers.AdCodeGoogleClientID, providers.AdCodeGoogleClientSecret = origID, origSecret
	})

	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/oauth/adcode/flow?method=google", nil)
	req.SetPathValue("provider", "adcode")
	h.HandleGenericFlowInfo(rec, req)

	if strings.Contains(rec.Body.String(), `"needsApp":true`) {
		t.Fatalf("configured install still asks for an app: %s", rec.Body.String())
	}
}
