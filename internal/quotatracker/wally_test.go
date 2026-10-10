package quotatracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The live /v1/me payload is deliberately not OpenAI-shaped and reports no
// remaining balance at all; this pins the fields that do exist so a change in
// the handler cannot silently invent a total the provider never set.
func TestFetchWally_ReportsUsedTokensWithoutInventingALimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"email":"a@b.c","plan":"beta","tokens_this_month":88734758,"monthly_token_limit":0}`))
	}))
	defer srv.Close()

	res, err := fetchWally(context.Background(), srv.Client(), Credentials{
		APIKey:   "sk-test",
		UsageURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan != "beta" {
		t.Errorf("plan = %q, want beta", res.Plan)
	}
	q, ok := res.Quotas["monthly_tokens"]
	if !ok {
		t.Fatalf("missing monthly_tokens quota: %+v", res)
	}
	if q.Used != 88734758 {
		t.Errorf("used = %v, want 88734758", q.Used)
	}
	if q.Total != 0 {
		t.Errorf("total = %v; a zero limit must not be turned into a real total", q.Total)
	}
	if !q.Unlimited {
		t.Error("limit 0 must be marked unlimited, not rendered as 0/0")
	}
}

func TestFetchWally_RejectsBadCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	res, err := fetchWally(context.Background(), srv.Client(), Credentials{
		APIKey:   "sk-bad",
		UsageURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Message == "" {
		t.Error("401 must be reported as a message, not an empty success")
	}
}
