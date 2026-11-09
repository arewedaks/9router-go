package quotatracker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// wallyStub serves both halves of the Wally read on one URL: the GET profile and
// the POST completion whose headers carry the caps, so the handler is exercised
// end to end without touching the network.
func wallyStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth = %q, want Bearer sk-test", got)
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"email":"a@b.c","plan":"beta","tokens_this_month":88734758,"monthly_token_limit":0}`))
		case http.MethodPost:
			// The probe must be the cheapest non-streaming call: stream:false is
			// what makes the caps appear, and max_tokens:1 bounds the cost.
			var body struct {
				MaxTokens int  `json:"max_tokens"`
				Stream    bool `json:"stream"`
			}
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("probe body unparseable: %v", err)
			}
			if body.Stream {
				t.Error("probe requested streaming; caps are only sent on non-streaming responses")
			}
			if body.MaxTokens != 1 {
				t.Errorf("probe max_tokens = %d, want 1", body.MaxTokens)
			}
			w.Header().Set("x-ratelimit-limit-requests", "600")
			w.Header().Set("x-ratelimit-limit-tokens", "100000000")
			w.Header().Set("x-ratelimit-limit-concurrent-requests", "32")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[]}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
}

// The live profile reports no ceiling (monthly_token_limit is 0 on every
// account), so the handler must report consumption without inventing a total —
// and must pick up the caps that only exist as completion headers.
func TestFetchWally_ReportsConsumptionAndHeaderCaps(t *testing.T) {
	srv := wallyStub(t)
	defer srv.Close()

	res, err := fetchWally(context.Background(), srv.Client(), Credentials{
		APIKey:   "sk-test",
		UsageURL: srv.URL,
		BaseURL:  srv.URL,
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
	if q.Total != 0 || !q.Unlimited {
		t.Errorf("limit 0 must stay unlimited with no total, got total=%v unlimited=%v", q.Total, q.Unlimited)
	}

	for _, tc := range []struct {
		key   string
		total float64
	}{
		{"requests", 600},
		{"tokens", 100000000},
		{"concurrent-requests", 32},
	} {
		cap, ok := res.Quotas[tc.key]
		if !ok {
			t.Errorf("missing cap %q", tc.key)
			continue
		}
		if cap.Total != tc.total {
			t.Errorf("%s total = %v, want %v", tc.key, cap.Total, tc.total)
		}
		if !cap.LimitOnly {
			t.Errorf("%s must be LimitOnly: a published cap with no used counter", tc.key)
		}
	}
}

func TestFetchWally_RejectsBadCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	res, err := fetchWally(context.Background(), srv.Client(), Credentials{
		APIKey:   "sk-test",
		UsageURL: srv.URL,
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Message == "" {
		t.Error("401 must be reported as a message, not an empty success")
	}
}
