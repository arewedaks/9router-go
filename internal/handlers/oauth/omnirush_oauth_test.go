package oauth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// The dashboard needs the flow kind before it renders anything, and it must be
// the device shape so the panel shows a code instead of a paste field.
func TestOmniRushFlowIsDeviceCode(t *testing.T) {
	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleOmniRushFlow(rec, httptest.NewRequest("GET", "/api/oauth/omnirush/flow", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"kind":"device_code"`) || !strings.Contains(body, "omnirush.ai") {
		t.Fatalf("flow body = %s", body)
	}
}

// Exchange must report pending while the operator has not approved, and store
// both tokens once they have.
func TestOmniRushExchangePendingThenStoresTokens(t *testing.T) {
	var approved bool
	mux := http.NewServeMux()
	mux.HandleFunc("/device/token", func(w http.ResponseWriter, r *http.Request) {
		if !approved {
			w.WriteHeader(http.StatusPreconditionRequired)
			_, _ = io.WriteString(w, `{"detail":"authorization_pending"}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"access-1","refresh_token":"refresh-1","gateway_url":"https://omnirush.ai/omnirush"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	orig := providers.OmniRushBase
	providers.OmniRushBase = srv.URL
	t.Cleanup(func() { providers.OmniRushBase = orig })

	repo := newDuplicateTestRepo(t)
	h := NewOAuthHandler(repo)

	poll := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/oauth/omnirush/exchange",
			strings.NewReader(`{"deviceCode":"dev-1","name":"OmniRush box"}`))
		h.HandleOmniRushExchange(rec, req)
		return rec
	}

	rec := poll()
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pending":true`) {
		t.Fatalf("pending poll = %d %s", rec.Code, rec.Body.String())
	}

	approved = true
	rec = poll()
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("approved poll = %d %s", rec.Code, rec.Body.String())
	}

	conns, err := repo.GetProviderConnections("omnirush", false)
	if err != nil || len(conns) != 1 {
		t.Fatalf("connections = %d, err %v", len(conns), err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conns[0].Data), &data); err != nil {
		t.Fatalf("decode connection: %v", err)
	}
	if data["accessToken"] != "access-1" || data["refreshToken"] != "refresh-1" {
		t.Fatalf("stored data = %v, want both device tokens", data)
	}
}
