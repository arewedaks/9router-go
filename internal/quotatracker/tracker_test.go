package quotatracker

import (
	"context"
	"encoding/json/jsontext"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Real payload captured from https://www.codebuddy.ai/v2/billing/meter/get-user-resource
// (trimmed to the fields the tracker reads). Two accounts: a Bonus Pack whose
// cycle ends long before the resource expires, and a monthly Free Plan whose
// cycle is the calendar month.
const livePayload = `{
  "code": 0,
  "msg": "OK",
  "data": {
    "Response": {
      "Data": {
        "TotalCount": 2,
        "Accounts": [
          {
            "PackageName": "Bonus Pack",
            "SubProductName": "Tencent Cloud CodeBuddy （IDE）",
            "CapacityUnit": "credit",
            "CycleStartTime": "2026-09-11 00:53:50",
            "CycleEndTime": "2026-09-25 00:53:49",
            "DeductionEndTime": 1790268829000,
            "CapacityUsedPrecise": "218.93999993",
            "CapacityUsed": 218,
            "CapacitySizePrecise": "250",
            "CapacitySize": 250,
            "CycleCapacityUsedPrecise": "218.93999993",
            "CycleCapacityUsed": 218,
            "CycleCapacitySizePrecise": "250",
            "CycleCapacitySize": 250
          },
          {
            "PackageName": "Free Plan Subscription",
            "SubProductName": "Tencent Cloud CodeBuddy（IDE）",
            "CapacityUnit": "credits",
            "CycleStartTime": "2026-09-01 00:00:00",
            "CycleEndTime": "2026-09-30 23:59:59",
            "DeductionEndTime": 2049382430000,
            "CapacityUsedPrecise": "0",
            "CapacityUsed": 0,
            "CapacitySizePrecise": "100",
            "CapacitySize": 100,
            "CycleCapacityUsedPrecise": "0",
            "CycleCapacityUsed": 0,
            "CycleCapacitySizePrecise": "100",
            "CycleCapacitySize": 100
          }
        ]
      }
    }
  }
}`

func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer tok")
		}
		if got := r.Header.Get("X-Product"); got != "SaaS" {
			t.Errorf("static header X-Product = %q; the billing endpoint rejects requests without the client fingerprint", got)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func credsFor(url string) Credentials {
	return Credentials{
		Provider:     "codebuddy-intl",
		AccessToken:  "tok",
		UsageURL:     url,
		StaticHeader: map[string]string{"X-Product": "SaaS"},
	}
}

// The two package kinds must not be merged: the bonus pack is one-shot and the
// free plan refills monthly. Reporting the bonus as recurring would promise a
// refill that never comes.
func TestFetchCodeBuddy_SeparatesRefillFromBonus(t *testing.T) {
	srv := serve(t, http.StatusOK, livePayload)

	res, err := Fetch(context.Background(), srv.Client(), credsFor(srv.URL))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Message != "" {
		t.Fatalf("unexpected message: %s", res.Message)
	}
	if len(res.Quotas) != 2 {
		t.Fatalf("got %d quotas, want 2: %+v", len(res.Quotas), res.Quotas)
	}

	monthly, ok := res.Quotas["Monthly"]
	if !ok {
		t.Fatalf("expected a Monthly refill pack, got %+v", res.Quotas)
	}
	if !monthly.Recurring {
		t.Error("the Free Plan Subscription refills each month, but Recurring is false")
	}
	if monthly.Used != 0 || monthly.Total != 100 {
		t.Errorf("Monthly used/total = %v/%v, want 0/100", monthly.Used, monthly.Total)
	}

	bonus, ok := res.Quotas["Bonus Pack 1"]
	if !ok {
		t.Fatalf("expected a Bonus Pack 1 entry, got %+v", res.Quotas)
	}
	if bonus.Recurring {
		t.Error("a bonus pack never refills, but Recurring is true")
	}
	// The exact field must win over the rounded one.
	if bonus.Used != 218.93999993 {
		t.Errorf("bonus used = %v, want the precise 218.93999993 rather than the rounded 218", bonus.Used)
	}
	if bonus.Total != 250 {
		t.Errorf("bonus total = %v, want 250", bonus.Total)
	}
}

// Reset times are stated in the provider's own zone (UTC+8) with no offset in
// the string. Parsing them as UTC would shift every reset by 8 hours.
func TestFetchCodeBuddy_ResetTimeUsesProviderZone(t *testing.T) {
	srv := serve(t, http.StatusOK, livePayload)

	res, err := Fetch(context.Background(), srv.Client(), credsFor(srv.URL))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	bonus := res.Quotas["Bonus Pack 1"]
	if bonus.ResetAt == "" {
		t.Fatal("bonus resetAt is empty")
	}
	// 2026-09-25 00:53:49 UTC+8 == 2026-09-24T16:53:49Z
	if bonus.ResetAt != "2026-09-24T16:53:49Z" {
		t.Errorf("resetAt = %q, want 2026-09-24T16:53:49Z (UTC+8 converted to UTC)", bonus.ResetAt)
	}
}

// An expired credential is a normal outcome with an explanation, not an error:
// the dashboard renders the reason rather than a failure toast.
func TestFetchCodeBuddy_ExpiredCredentialReportsMessage(t *testing.T) {
	srv := serve(t, http.StatusUnauthorized, `{}`)

	res, err := Fetch(context.Background(), srv.Client(), credsFor(srv.URL))
	if err != nil {
		t.Fatalf("Fetch returned an error for a 401; it should report a message: %v", err)
	}
	if res.Message == "" {
		t.Fatal("expected an explanatory message for the 401")
	}
}

// Providers without a handler must say so instead of being probed, which would
// send auth headers to a host that has no quota API.
func TestFetch_UnsupportedProviderIsNotProbed(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	creds := credsFor(srv.URL)
	creds.Provider = "openai"

	res, err := Fetch(context.Background(), srv.Client(), creds)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if called {
		t.Error("an unsupported provider was probed; the request should never be sent")
	}
	if res.Message == "" {
		t.Error("expected an explanatory message for an unsupported provider")
	}
}

// The exact values are quoted strings on the wire; a float64 field would fail
// to unmarshal every real response. This pins the decoding of both forms.
func TestFlexNum_AcceptsNumberAndQuotedNumber(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{`"218.93999993"`, 218.93999993, true},
		{`250`, 250, true},
		{`"250"`, 250, true},
		{`""`, 0, false},
		{`null`, 0, false},
	}
	for _, c := range cases {
		got, ok := flexNum(jsontext.Value(c.in))
		if ok != c.ok {
			t.Errorf("flexNum(%s) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("flexNum(%s) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseProviderTime_AcceptsBothShapes(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"2026-09-25 00:53:49", "2026-09-24T16:53:49Z", true},
		{"2026-09-01 00:00:00", "2026-08-31T16:00:00Z", true},
		{"1790268829000", "2026-09-24T16:53:49Z", true},
		{"", "", false},
		{"not a date", "", false},
	}
	for _, c := range cases {
		got, ok := parseProviderTime(c.in)
		if ok != c.ok {
			t.Errorf("parseProviderTime(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.UTC().Format("2006-01-02T15:04:05Z") != c.want {
			t.Errorf("parseProviderTime(%q) = %v, want %s", c.in, got.UTC(), c.want)
		}
	}
}
