package chat

import "testing"

// The ad gate must not be mistaken for a failing account.
//
// Surfacing the gate as a 403 put it on the generic forbidden path, which locks
// the connection for a cooldown — and the next request then answered "provider
// is unhealthy" instead of the gate's own remedy. These cases pin the marker
// that keeps the two apart, because the whole value of the exemption rests on it
// never matching an ordinary error.
func TestIsAdGateBody(t *testing.T) {
	gate := []string{
		`{"error":{"code":"ad_gate","message":"free-pi is free because it shows ads, and this session hasn't loaded any."}}`,
		`{"code": "ad_gate"}`,
		`{"error":{"message":"free-pi is free because it shows ads, and this session hasn't loaded any. Requests stay paused until ads load in the official free-pi CLI."}}`,
		`{"error":{"message":"Ads aren't showing for this account"}}`,
	}
	for _, body := range gate {
		if !isAdGateBody(body) {
			t.Errorf("gate body not recognised: %s", body)
		}
	}

	notGate := []string{
		"",
		`{"error":{"code":"unauthorized","message":"missing bearer token"}}`,
		`{"error":{"message":"upstream returned 502"}}`,
		`{"error":{"code":"concurrent_session","message":"too many sessions"}}`,
		// A model answering ABOUT the ads is a normal response, not a gate: the
		// marker must be the code, and the phrase alone is deliberately weak
		// enough that it only fires when nothing else matches.
		`{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`,
	}
	for _, body := range notGate {
		if isAdGateBody(body) {
			t.Errorf("non-gate body matched: %s", body)
		}
	}
}
