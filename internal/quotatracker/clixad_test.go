package quotatracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The CLI bills a chat turn, not a request: spendableAt() reads from
// /v1/wallet the same two numbers this handler copies — the spent balance
// (a point in time, LimitOnly) and the daily free grant (a recurring
// allowance). free.daily_credits is the grant ceiling, free.credits_left is
// what's left today; the difference is the used amount the dashboard bar
// divides by the ceiling, exactly like a per-day Theos grant.
func TestFetchClixad_ReportsBalanceAndDailyFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth = %q, want Bearer sk-test", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":125400,"free":{"daily_credits":6000,"credits_left":4100,
			"models":["gpt-5-nano","gemini-2.5-flash-lite"],"turns_left":{"gpt-5-nano":4},
			"recommended":"gpt-5-nano"},
			"premium":{"unlocked":false,"credits":0,"unlock_usd":3}}`))
	}))
	defer srv.Close()

	res, err := fetchClixad(context.Background(), srv.Client(), Credentials{
		APIKey:   "sk-test",
		UsageURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	bal, ok := res.Quotas["credits"]
	if !ok {
		t.Fatalf("missing credits quota: %+v", res)
	}
	if bal.Total != 125400 || !bal.LimitOnly {
		t.Errorf("credits = total=%v limitOnly=%v, want total=125400 limitOnly=true", bal.Total, bal.LimitOnly)
	}

	daily, ok := res.Quotas["daily_free"]
	if !ok {
		t.Fatalf("missing daily_free quota: %+v", res)
	}
	if daily.Total != 6000 || daily.Used != 1900 || !daily.Recurring || daily.Unit != "credits" {
		t.Errorf("daily_free = total=%v used=%v recurring=%v unit=%q, want 6000/1900/true/credits",
			daily.Total, daily.Used, daily.Recurring, daily.Unit)
	}
	if daily.ResetAt == "" || daily.DisplayName != "Daily free credits" {
		t.Errorf("daily_free resetAt=%q displayName=%q", daily.ResetAt, daily.DisplayName)
	}
}

func TestFetchClixad_RejectsBadCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer srv.Close()

	res, err := fetchClixad(context.Background(), srv.Client(), Credentials{
		APIKey:   "sk-test",
		UsageURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Message == "" {
		t.Error("401 must be reported as a message, not an empty success")
	}
}

func TestFetchClixad_ZeroBalanceHasNoInventedTotal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":0,"free":{"daily_credits":6000,"credits_left":6000,"models":[]}}`))
	}))
	defer srv.Close()

	res, err := fetchClixad(context.Background(), srv.Client(), Credentials{
		APIKey:   "sk-test",
		UsageURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	bal, ok := res.Quotas["credits"]
	if !ok {
		t.Fatalf("missing credits quota on zero balance: %+v", res)
	}
	if bal.Total != 0 {
		t.Errorf("zero balance must not be invented as a spendable total: %v", bal.Total)
	}
}
