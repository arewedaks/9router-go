package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// qoderStub serves the two Qoder endpoints. approved flips mid-test so one
// handler covers "still waiting" and "signed in", which is how the real panel
// sees it.
type qoderStub struct {
	approved bool
}

func newQoderTestHandler(t *testing.T, stub *qoderStub) *OAuthHandler {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/deviceToken/poll":
			// The verifier is the only proof the poll belongs to the flow we
			// started; without it the upstream answers 404 for everyone.
			if r.URL.Query().Get("verifier") == "" || r.URL.Query().Get("nonce") == "" {
				t.Errorf("poll carried no nonce/verifier: %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("challenge_method") != "S256" {
				t.Errorf("challenge_method = %q, want S256", r.URL.Query().Get("challenge_method"))
			}
			if !stub.approved {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errorCode":"NotFound"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"dt-test","refresh_token":"rt-test","user_id":"u-1"}`))
		case "/api/v1/userinfo":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer dt-test") {
				t.Errorf("userinfo auth = %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"email":"op@example.com","nickname":"operator"}`))
		default:
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	oldPoll, oldUser := qoderPollURL, qoderUserInfoURL
	qoderPollURL, qoderUserInfoURL = srv.URL+"/api/v1/deviceToken/poll", srv.URL+"/api/v1/userinfo"
	t.Cleanup(func() { qoderPollURL, qoderUserInfoURL = oldPoll, oldUser })

	database, cleanup := setupOAuthTestDB(t)
	t.Cleanup(cleanup)
	return NewOAuthHandler(db.NewRepo(database))
}

// startQoderFlow drives /authorize and returns the nonce it handed out.
func startQoderFlow(t *testing.T, h *OAuthHandler) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.HandleQoderAuthorize(w, httptest.NewRequest("GET", "/api/oauth/qoder/authorize", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("authorize returned %d: %s", w.Code, w.Body.String())
	}
	var grant struct {
		DeviceCode string `json:"deviceCode"`
		AuthURL    string `json:"authUrl"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &grant); err != nil {
		t.Fatalf("decode grant: %v", err)
	}
	if grant.DeviceCode == "" || !strings.Contains(grant.AuthURL, "qoder.com/device/selectAccounts") {
		t.Fatalf("grant = %+v", grant)
	}
	for _, key := range []string{"challenge=", "challenge_method=S256", "machine_id=", "nonce="} {
		if !strings.Contains(grant.AuthURL, key) {
			t.Errorf("auth URL missing %s: %s", key, grant.AuthURL)
		}
	}
	return grant.DeviceCode
}

func TestQoderFlowInfoReportsDeviceCode(t *testing.T) {
	h := newQoderTestHandler(t, &qoderStub{})
	w := httptest.NewRecorder()
	h.HandleQoderFlowInfo(w, httptest.NewRequest("GET", "/api/oauth/qoder/flow", nil))

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "device_code") {
		t.Fatalf("flow info = %d %s", w.Code, w.Body.String())
	}
}

func TestQoderExchangePendsThenStoresToken(t *testing.T) {
	stub := &qoderStub{}
	h := newQoderTestHandler(t, stub)
	nonce := startQoderFlow(t, h)

	// 1. Not approved yet: a pending answer, not an error, so the panel waits.
	w := httptest.NewRecorder()
	h.HandleQoderExchange(w, httptest.NewRequest("POST", "/api/oauth/qoder/exchange",
		strings.NewReader(`{"deviceCode":"`+nonce+`"}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"pending":true`) {
		t.Fatalf("pending poll = %d %s", w.Code, w.Body.String())
	}

	// 2. Approved: the same nonce now stores a usable connection.
	stub.approved = true
	w = httptest.NewRecorder()
	h.HandleQoderExchange(w, httptest.NewRequest("POST", "/api/oauth/qoder/exchange",
		strings.NewReader(`{"deviceCode":"`+nonce+`"}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("approved poll = %d %s", w.Code, w.Body.String())
	}

	var name, provider, authType, data string
	err := h.Repo.RawDB().QueryRow(
		`SELECT name, provider, authType, data FROM providerConnections WHERE provider = 'qoder'`,
	).Scan(&name, &provider, &authType, &data)
	if err != nil {
		t.Fatalf("connection row: %v", err)
	}
	if name != "operator" || authType != "oauth" {
		t.Errorf("connection = %q/%q, want operator/oauth", name, authType)
	}
	for _, want := range []string{`"accessToken":"dt-test"`, `"machineId":"`, `"authMethod":"device"`} {
		if !strings.Contains(data, want) {
			t.Errorf("connection data missing %s: %s", want, data)
		}
	}

	// 3. The nonce is single-use once spent: a replayed poll must not store the
	// account a second time.
	w = httptest.NewRecorder()
	h.HandleQoderExchange(w, httptest.NewRequest("POST", "/api/oauth/qoder/exchange",
		strings.NewReader(`{"deviceCode":"`+nonce+`"}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("replayed poll = %d %s, want 400", w.Code, w.Body.String())
	}
	var count int
	if err := h.Repo.RawDB().QueryRow(`SELECT COUNT(*) FROM providerConnections WHERE provider = 'qoder'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("stored %d qoder connections, want 1", count)
	}
}
