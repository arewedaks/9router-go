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

// A refresh token that exchanges cleanly must be stored under both keys the
// router reads: accessToken is what the executor forwards, refreshToken is what
// the account probe prefers. Storing only one of them leaves half the feature
// broken and it is invisible until the first chat or the first Test click.
func TestAdCodeImportStoresRefreshTokenUnderBothKeys(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id_token":"minted","expires_in":"3600","refresh_token":"rt-value"}`)
	}))
	t.Cleanup(tokenSrv.Close)
	origURL := providers.AdCodeTokenURL
	providers.AdCodeTokenURL = tokenSrv.URL
	t.Cleanup(func() { providers.AdCodeTokenURL = origURL })
	providers.ResetAdCodeTokenCache()

	repo := newDuplicateTestRepo(t)
	h := NewOAuthHandler(repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/adcode/exchange",
		strings.NewReader(`{"accessToken":"rt-value","name":"BOZ"}`))
	req.SetPathValue("provider", "adcode")
	h.HandleGenericExchange(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	conns, err := repo.GetProviderConnections("adcode", false)
	if err != nil {
		t.Fatalf("read connections: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("got %d connections, want 1", len(conns))
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conns[0].Data), &data); err != nil {
		t.Fatalf("decode connection data: %v", err)
	}
	for _, key := range []string{"accessToken", "refreshToken"} {
		if data[key] != "rt-value" {
			t.Errorf("%s = %v, want the pasted refresh token", key, data[key])
		}
	}
	if conns[0].AuthType != "oauth" {
		t.Errorf("authType = %q, want oauth", conns[0].AuthType)
	}
}
