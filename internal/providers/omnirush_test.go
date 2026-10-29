package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The device flow is the only way in: the gateway hands out a device token plus a
// rotating refresh token, and every call carries two identity headers whose
// exact shapes the gateway validates.
func TestOmniRushDeviceFlowAndRefresh(t *testing.T) {
	var headersOK atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/device/authorize", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == OmniRushUserAgent && strings.HasPrefix(r.Header.Get("X-OmniRush-Client"), "cli/") {
			headersOK.Store(true)
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["device_name"] == "" || body["platform"] == "" {
			t.Errorf("authorize body missing device_name/platform: %v", body)
		}
		_, _ = io.WriteString(w, `{"device_code":"dev-1","user_code":"ABCD-1234","verification_uri":"https://omnirush.ai/device","verification_uri_complete":"https://omnirush.ai/device?code=ABCD-1234","interval":5,"expires_in":600}`)
	})
	mux.HandleFunc("/device/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionRequired)
		_, _ = io.WriteString(w, `{"detail":"authorization_pending"}`)
	})
	mux.HandleFunc("/device/refresh", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["refresh_token"] != "old-refresh" {
			t.Errorf("refresh body = %v", body)
		}
		_, _ = io.WriteString(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	orig := OmniRushBase
	OmniRushBase = srv.URL
	t.Cleanup(func() { OmniRushBase = orig })

	code, err := OmniRushAuthorize(context.Background(), srv.Client(), "9router@box", "linux")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if code.DeviceCode != "dev-1" || code.UserCode != "ABCD-1234" || code.Interval != 5 {
		t.Fatalf("device code = %+v", code)
	}
	if !headersOK.Load() {
		t.Fatal("authorize did not carry the gateway identity headers")
	}

	// A pending poll must be reported as pending, not as a failure: the CLI
	// polls until the operator approves.
	res, err := OmniRushPollToken(context.Background(), srv.Client(), code.DeviceCode)
	if err != nil {
		t.Fatalf("poll while pending: %v", err)
	}
	if !res.Pending {
		t.Fatalf("428 authorization_pending was not reported as pending: %+v", res)
	}

	access, refresh, expiresIn, err := OmniRushRefresh(context.Background(), srv.Client(), "old-refresh")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if access != "new-access" || refresh != "new-refresh" || expiresIn != 3600 {
		t.Fatalf("refresh = %q/%q/%d", access, refresh, expiresIn)
	}
}

// The provider must route through the Responses transport: chat completions are
// 404 on this gateway.
func TestOmniRushRegistrationUsesResponsesTransport(t *testing.T) {
	cfg, ok := KnownProviders["omnirush"]
	if !ok {
		t.Fatal("omnirush is not registered")
	}
	if cfg.Format != FormatOpenAIResponses {
		t.Errorf("Format = %q, want %q", cfg.Format, FormatOpenAIResponses)
	}
	if !strings.HasSuffix(cfg.BaseURL, "/v1/responses") {
		t.Errorf("BaseURL = %q, want the Responses route", cfg.BaseURL)
	}
	if cfg.StaticHeaders["User-Agent"] != OmniRushUserAgent {
		t.Errorf("User-Agent = %q, want the bare product name", cfg.StaticHeaders["User-Agent"])
	}
	if !strings.HasPrefix(cfg.StaticHeaders["X-OmniRush-Client"], "cli/") {
		t.Errorf("X-OmniRush-Client = %q", cfg.StaticHeaders["X-OmniRush-Client"])
	}
}
