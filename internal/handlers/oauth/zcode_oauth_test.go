package oauth

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestZcodeFlowInfo(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/zcode/flow", nil)
	w := httptest.NewRecorder()
	h.HandleZcodeFlowInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("flow info status = %d, want 200", w.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if res["kind"] != "device_code" {
		t.Errorf("kind = %v, want device_code", res["kind"])
	}
	if res["provider"] != "zcode" {
		t.Errorf("provider = %v, want zcode", res["provider"])
	}
}

func TestZcodeDeviceFlow_AuthorizeAndExchange(t *testing.T) {
	pollStatus := "pending"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/oauth/cli/poll/") {
			if r.Header.Get("Authorization") != "Bearer poll-token-123" {
				t.Errorf("poll auth = %q, want Bearer poll-token-123", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			if pollStatus == "pending" {
				_, _ = w.Write([]byte(`{"data":{"status":"pending"}}`))
			} else {
				_, _ = w.Write([]byte(`{
					"data": {
						"status": "ready",
						"token": "plan-jwt-sample-token",
						"user": {
							"email": "test@z.ai",
							"user_id": "user-uuid-987"
						},
						"zai": {
							"access_token": "zai-access",
							"refresh_token": "zai-refresh"
						}
					}
				}`))
			}
			return
		}
		switch r.URL.Path {
		case "/api/v1/oauth/cli/init":
			if r.Method != http.MethodPost {
				t.Errorf("init method = %s, want POST", r.Method)
			}
			if r.Header.Get("Authorization") == "" {
				t.Errorf("missing Authorization header on init")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"data": {
					"flow_id": "flow-test-123",
					"authorize_url": "https://zcode.z.ai/auth?flow=123",
					"expires_at": 9999999999,
					"poll_interval_sec": 2,
					"poll_token": "poll-token-123"
				}
			}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	origEnv := os.Getenv("ZCODE_OAUTH_ORIGIN")
	_ = os.Setenv("ZCODE_OAUTH_ORIGIN", srv.URL)
	defer func() {
		if origEnv != "" {
			_ = os.Setenv("ZCODE_OAUTH_ORIGIN", origEnv)
		} else {
			_ = os.Unsetenv("ZCODE_OAUTH_ORIGIN")
		}
	}()

	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	// 1. Authorize
	authReq := httptest.NewRequest(http.MethodGet, "/api/oauth/zcode/authorize", nil)
	authW := httptest.NewRecorder()
	h.HandleZcodeAuthorize(authW, authReq)

	if authW.Code != http.StatusOK {
		t.Fatalf("authorize status = %d: %s", authW.Code, authW.Body.String())
	}

	var authRes map[string]any
	if err := json.Unmarshal(authW.Body.Bytes(), &authRes); err != nil {
		t.Fatalf("unmarshal auth response: %v", err)
	}
	flowID, _ := authRes["deviceCode"].(string)
	if flowID != "flow-test-123" {
		t.Fatalf("deviceCode = %q, want flow-test-123", flowID)
	}

	// 2. Exchange while pending
	pollBody, _ := json.Marshal(map[string]string{"deviceCode": flowID})
	exchReq := httptest.NewRequest(http.MethodPost, "/api/oauth/zcode/exchange", bytes.NewReader(pollBody))
	exchW := httptest.NewRecorder()
	h.HandleZcodeExchange(exchW, exchReq)

	if exchW.Code != http.StatusOK {
		t.Fatalf("exchange status = %d: %s", exchW.Code, exchW.Body.String())
	}
	var exchRes map[string]any
	_ = json.Unmarshal(exchW.Body.Bytes(), &exchRes)
	if exchRes["pending"] != true {
		t.Fatalf("expected pending=true, got %v", exchRes)
	}

	// 3. Exchange when ready
	pollStatus = "ready"
	exchReq2 := httptest.NewRequest(http.MethodPost, "/api/oauth/zcode/exchange", bytes.NewReader(pollBody))
	exchW2 := httptest.NewRecorder()
	h.HandleZcodeExchange(exchW2, exchReq2)

	if exchW2.Code != http.StatusOK {
		t.Fatalf("exchange 2 status = %d: %s", exchW2.Code, exchW2.Body.String())
	}
	var exchRes2 map[string]any
	if err := json.Unmarshal(exchW2.Body.Bytes(), &exchRes2); err != nil {
		t.Fatalf("unmarshal ready response: %v", err)
	}
	if exchRes2["success"] != true {
		t.Fatalf("expected success=true, got %v", exchRes2)
	}

	// Verify row in DB
	var dbData, dbEmail string
	err := repo.RawDB().QueryRow("SELECT data, email FROM providerConnections WHERE provider = 'zcode' AND isActive = 1").Scan(&dbData, &dbEmail)
	if err != nil {
		t.Fatalf("query DB: %v", err)
	}
	if dbEmail != "test@z.ai" {
		t.Errorf("db email = %q, want test@z.ai", dbEmail)
	}
	var connParsed struct {
		APIKey      string         `json:"apiKey"`
		AccessToken string         `json:"accessToken"`
		Refresh     string         `json:"refreshToken"`
		Email       string         `json:"email"`
		PSD         map[string]any `json:"providerSpecificData"`
	}
	if err := json.Unmarshal([]byte(dbData), &connParsed); err != nil {
		t.Fatalf("parse db data: %v", err)
	}
	if connParsed.APIKey != "plan-jwt-sample-token" || connParsed.AccessToken != "plan-jwt-sample-token" {
		t.Errorf("token = %q, want plan-jwt-sample-token", connParsed.APIKey)
	}
	if connParsed.Refresh != "zai-refresh" {
		t.Errorf("refresh = %q, want zai-refresh", connParsed.Refresh)
	}
	if connParsed.PSD["deviceId"] == "" || connParsed.PSD["sessionSeed"] == "" {
		t.Errorf("missing deviceId or sessionSeed in PSD: %+v", connParsed.PSD)
	}
	if connParsed.PSD["accountUUID"] != "user-uuid-987" {
		t.Errorf("accountUUID = %v, want user-uuid-987", connParsed.PSD["accountUUID"])
	}

	// 4. Re-login of the same account preserves deviceId, sessionSeed, accountUUID
	origDeviceID := connParsed.PSD["deviceId"]
	origSessionSeed := connParsed.PSD["sessionSeed"]

	pollStatus = "ready"
	rememberZcodeFlow("flow-relogin-456", zcodePendingFlow{
		pollToken: "poll-token-123",
		expires:   time.Now().Add(5 * time.Minute),
	})
	pollBody2, _ := json.Marshal(map[string]string{"deviceCode": "flow-relogin-456"})
	exchReq3 := httptest.NewRequest(http.MethodPost, "/api/oauth/zcode/exchange", bytes.NewReader(pollBody2))
	exchW3 := httptest.NewRecorder()
	h.HandleZcodeExchange(exchW3, exchReq3)

	if exchW3.Code != http.StatusOK {
		t.Fatalf("exchange 3 status = %d: %s", exchW3.Code, exchW3.Body.String())
	}

	// Verify only 1 active connection exists and identity was preserved
	var count int
	if err := repo.RawDB().QueryRow("SELECT COUNT(*) FROM providerConnections WHERE provider = 'zcode' AND isActive = 1").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("active connection count = %d, want 1", count)
	}

	err = repo.RawDB().QueryRow("SELECT data FROM providerConnections WHERE provider = 'zcode' AND isActive = 1").Scan(&dbData)
	if err != nil {
		t.Fatalf("query DB after relogin: %v", err)
	}
	if err := json.Unmarshal([]byte(dbData), &connParsed); err != nil {
		t.Fatalf("parse db data: %v", err)
	}
	if connParsed.PSD["deviceId"] != origDeviceID {
		t.Errorf("deviceId changed on relogin: got %v, want %v", connParsed.PSD["deviceId"], origDeviceID)
	}
	if connParsed.PSD["sessionSeed"] != origSessionSeed {
		t.Errorf("sessionSeed changed on relogin: got %v, want %v", connParsed.PSD["sessionSeed"], origSessionSeed)
	}
}

// ensure strings is used
var _ = strings.TrimSpace
