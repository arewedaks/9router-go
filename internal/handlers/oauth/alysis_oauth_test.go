package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// newAlysisTestHandler points the device handshake at a stub and returns a
// handler over a throwaway DB. The stub answers /device-code and /device-token
// the way alysiscode.com does, so the flow is exercised without a real account.
func newAlysisTestHandler(t *testing.T, tokenStatus, tokenKey string) *OAuthHandler {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device-code":
			var body struct {
				ClientName     string `json:"client_name"`
				InstallationID string `json:"installation_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("device-code body: %v", err)
			}
			if body.InstallationID == "" {
				t.Error("device-code request carried no installation_id")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"device_code":"DEV123","user_code":"ABCD-EFGH",
				"verification_url":"https://alysiscode.com/activate",
				"verification_url_complete":"https://alysiscode.com/activate?code=ABCD-EFGH",
				"interval":5,"expires_in":900}`))
		case "/device-token":
			var body struct {
				DeviceCode string `json:"device_code"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("device-token body: %v", err)
			}
			if body.DeviceCode != "DEV123" {
				t.Errorf("device-token device_code = %q, want DEV123", body.DeviceCode)
			}
			w.Header().Set("Content-Type", "application/json")
			if tokenKey != "" {
				_, _ = w.Write([]byte(`{"status":"` + tokenStatus + `","key":"` + tokenKey + `","email":"op@example.com"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"` + tokenStatus + `"}`))
		default:
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	old := alysisCodeBase
	alysisCodeBase = srv.URL
	t.Cleanup(func() { alysisCodeBase = old })

	database, cleanup := setupOAuthTestDB(t)
	t.Cleanup(cleanup)
	return NewOAuthHandler(db.NewRepo(database))
}

func TestAlysisAuthorizeReturnsDeviceGrant(t *testing.T) {
	h := newAlysisTestHandler(t, "pending", "")

	rec := httptest.NewRecorder()
	h.HandleAlysisAuthorize(rec, httptest.NewRequest("GET", "/api/oauth/alysis/authorize", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var got alysisDeviceGrant
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The dashboard panel switches to device mode on deviceCode; without it the
	// provider falls through to the redirect form and sign-in dead-ends.
	if got.DeviceCode != "DEV123" || got.UserCode != "ABCD-EFGH" {
		t.Errorf("grant = %+v, want DEV123 / ABCD-EFGH", got)
	}
	if got.AuthURL != "https://alysiscode.com/activate?code=ABCD-EFGH" {
		t.Errorf("authUrl = %q, want the /activate page pinned to the product site", got.AuthURL)
	}
	if got.Interval != 5 || got.ExpiresIn != 900 {
		t.Errorf("interval/expiresIn = %d/%d, want 5/900", got.Interval, got.ExpiresIn)
	}
}

func TestAlysisExchangePendingKeepsPolling(t *testing.T) {
	h := newAlysisTestHandler(t, "pending", "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/oauth/alysis/exchange", strings.NewReader(`{"deviceCode":"DEV123"}`))
	h.HandleAlysisExchange(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("pending poll must be 200 so the panel keeps waiting, got %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["pending"] != true {
		t.Errorf("body = %v, want pending:true", got)
	}
}

func TestAlysisExchangeApprovedStoresConnection(t *testing.T) {
	h := newAlysisTestHandler(t, "approved", "slk_approved_key")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/oauth/alysis/exchange", strings.NewReader(`{"deviceCode":"DEV123"}`))
	h.HandleAlysisExchange(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Success    bool   `json:"success"`
		Connection string `json:"connection"`
		Email      string `json:"email"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Success || got.Connection == "" {
		t.Fatalf("body = %+v, want success and a connection id", got)
	}

	var provider, authType, data string
	if err := h.Repo.RawDB().QueryRow(
		`SELECT provider, authType, data FROM providerConnections WHERE id = ?`, got.Connection,
	).Scan(&provider, &authType, &data); err != nil {
		t.Fatalf("stored connection: %v", err)
	}
	if provider != "alysis" || authType != "oauth" {
		t.Errorf("stored %q/%q, want alysis/oauth", provider, authType)
	}
	// The executor prefers accessToken over apiKey (connections.go), so the key
	// must land there or every model call goes out unauthenticated.
	var connData struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal([]byte(data), &connData); err != nil {
		t.Fatalf("decode stored data: %v", err)
	}
	if connData.AccessToken != "slk_approved_key" {
		t.Errorf("stored accessToken = %q, want the gateway key", connData.AccessToken)
	}
}

func TestAlysisExchangeExpiredFails(t *testing.T) {
	h := newAlysisTestHandler(t, "expired", "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/oauth/alysis/exchange", strings.NewReader(`{"deviceCode":"DEV123"}`))
	h.HandleAlysisExchange(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expired code must fail the poll, got %d: %s", rec.Code, rec.Body.String())
	}
}
