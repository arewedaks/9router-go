package quotatracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchDeepAI_ReadsStringDecimals proves the Theos payload is parsed from
// its real wire shape: balance/granted arrive as JSON *strings* ("48.51650"),
// not numbers. Declaring them float64 — the obvious reading — makes every real
// response fail to unmarshal, so this pins the string form.
func TestFetchDeepAI_ReadsStringDecimals(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Api-Key"); got != "theos-key" {
			t.Errorf("Api-Key header = %q, want theos-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization must not be set; DeepAI uses Api-Key, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"balance":"48.51650","granted":"50.00000","refresh_at":"2026-11-10T06:02:24+00:00","lane_version":4,"check_in":{"nonce":"fc7fb315e4389cd137d7b1bc1ea8b403","seconds":600}}`))
	}))
	defer upstream.Close()

	res, err := fetchDeepAI(context.Background(), upstream.Client(), Credentials{
		Provider:    "deepai",
		AccessToken: "theos-key",
		UsageURL:    upstream.URL,
	})
	if err != nil {
		t.Fatalf("fetchDeepAI: %v", err)
	}
	q, ok := res.Quotas["daily_theos"]
	if !ok {
		t.Fatalf("quotas = %+v, want daily_theos", res.Quotas)
	}
	if q.Total != 50 {
		t.Errorf("total = %v, want 50", q.Total)
	}
	if q.Used < 1.4 || q.Used > 1.6 {
		t.Errorf("used = %v, want ~1.48 (50 - 48.5165)", q.Used)
	}
	if !q.Recurring {
		t.Error("daily Theos recur; Recurring must be true so the UI says Resets in")
	}
	if q.ResetAt != "2026-11-10T06:02:24Z" {
		t.Errorf("resetAt = %q, want normalised UTC RFC3339", q.ResetAt)
	}
	if q.Unit != "Theos" {
		t.Errorf("unit = %q, want Theos", q.Unit)
	}
}

// TestFetchDeepAI_DisabledReportsMessage: a lane with Theos off is a normal
// outcome, not an allowance to display.
func TestFetchDeepAI_DisabledReportsMessage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"enabled":false}`))
	}))
	defer upstream.Close()

	res, err := fetchDeepAI(context.Background(), upstream.Client(), Credentials{
		Provider: "deepai", AccessToken: "k", UsageURL: upstream.URL,
	})
	if err != nil {
		t.Fatalf("fetchDeepAI: %v", err)
	}
	if res.Message == "" || res.Quotas != nil {
		t.Errorf("disabled lane must report a message, got %+v", res)
	}
}

// TestFetchDeepAI_AuthFailure: an expired key is a message, not an error.
func TestFetchDeepAI_AuthFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()

	res, err := fetchDeepAI(context.Background(), upstream.Client(), Credentials{
		Provider: "deepai", AccessToken: "k", UsageURL: upstream.URL,
	})
	if err != nil {
		t.Fatalf("fetchDeepAI: %v", err)
	}
	if res.Message == "" {
		t.Error("403 must surface as a message")
	}
}

// TestDeepAIIsRegistered proves the provider is wired into the tracker, so the
// dashboard's quota probe finds a handler instead of "not implemented".
func TestDeepAIIsRegistered(t *testing.T) {
	if !Supported("deepai") {
		t.Error("deepai not registered in quotatracker")
	}
}
