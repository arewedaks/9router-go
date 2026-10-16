package chat

import (
	"testing"
	"time"
)

func TestExtractResetDuration_GoogleRPCDetails(t *testing.T) {
	body := []byte(`{
  "error": {
    "code": 429,
    "message": "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h12m28s.",
    "status": "RESOURCE_EXHAUSTED",
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
        "reason": "QUOTA_EXHAUSTED",
        "domain": "cloudcode-pa.googleapis.com",
        "metadata": {
          "uiMessage": "true",
          "model": "gemini-3.8-flash-tiered",
          "quotaResetDelay": "1h12m28.109534319s"
        }
      }
    ]
  }
}`)

	dur, ok := extractResetDuration(body)
	if !ok {
		t.Fatalf("expected extractResetDuration to succeed")
	}

	expectedMin := 1*time.Hour + 12*time.Minute + 28*time.Second
	expectedMax := 1*time.Hour + 12*time.Minute + 29*time.Second
	if dur < expectedMin || dur > expectedMax {
		t.Errorf("expected duration around 1h12m28s, got %v", dur)
	}

	retryAfter := extractRetryAfter(body)
	if retryAfter == "" {
		t.Errorf("expected non-empty retryAfter from Google RPC body")
	}
}

func TestExtractResetDuration_TextMessage(t *testing.T) {
	body := []byte(`{"error":{"message":"Rate limit exceeded. Resets in 45m30s. Try again later."}}`)
	dur, ok := extractResetDuration(body)
	if !ok {
		t.Fatalf("expected extractResetDuration to succeed on message text")
	}
	expected := 45*time.Minute + 30*time.Second
	if dur != expected {
		t.Errorf("expected 45m30s, got %v", dur)
	}
}

// TokenHarbor phrases its 429 as "Please retry in about 30 seconds." — verb
// "retry", filler "about", unit spelled out. Until this was handled the reset
// hint was missed and the lock fell back to the 2s quota cooldown, so the same
// account was re-hit while upstream was still throttling it.
func TestExtractResetDuration_TokenHarborRetryPhrasing(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Duration
	}{
		{
			name: "retry in about N seconds / capacity",
			body: `{"error":{"message":"This model is at capacity for your account right now. Please retry in about 30 seconds.","type":"rate_limit_exceeded","code":"rate_limit_exceeded"}}`,
			want: 30 * time.Second,
		},
		{
			name: "retry in about N seconds / shared address",
			body: `{"error":{"message":"Rate limit reached: many free accounts are sharing this network address. For a stable, high-volume line, email support@tokenharbor.ai.","type":"rate_limit_exceeded","code":"rate_limit_exceeded"}}`,
			want: 0, // no hint at all in this phrasing
		},
		{
			name: "retry in N minutes",
			body: `{"error":{"message":"Please retry in 2 minutes."}}`,
			want: 2 * time.Minute,
		},
		{
			name: "retry after N seconds",
			body: `{"error":{"message":"Please retry after 45 seconds."}}`,
			want: 45 * time.Second,
		},
		{
			name: "retry in NhNmN s compact form",
			body: `{"error":{"message":"Please retry in 1h12m28s."}}`,
			want: 1*time.Hour + 12*time.Minute + 28*time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dur, ok := extractResetDuration([]byte(tc.body))
			if tc.want == 0 {
				if ok {
					t.Fatalf("expected no reset hint, got %v", dur)
				}
				return
			}
			if !ok {
				t.Fatalf("expected a reset hint, got none")
			}
			if dur != tc.want {
				t.Errorf("expected %v, got %v", tc.want, dur)
			}
		})
	}
}

func TestExtractResetDuration_ClampingSafety(t *testing.T) {
	// Case 1: Extremely long duration (> 2 hours) is clamped to maxResetCooldown (2 hours) to avoid deadlocks.
	bodyLong := []byte(`{"error":{"message":"Resets in 720h."}}`)
	durLong, ok := extractResetDuration(bodyLong)
	if !ok {
		t.Fatalf("expected ok for long duration")
	}
	if durLong != maxResetCooldown {
		t.Errorf("expected clamped to %v, got %v", maxResetCooldown, durLong)
	}

	// Case 2: Sub-5s duration is clamped to minResetCooldown (5s)
	bodyShort := []byte(`{"error":{"message":"Resets in 1s."}}`)
	durShort, ok := extractResetDuration(bodyShort)
	if !ok {
		t.Fatalf("expected ok for short duration")
	}
	if durShort != minResetCooldown {
		t.Errorf("expected clamped to %v, got %v", minResetCooldown, durShort)
	}
}

func TestBlockAntigravityModelUntil_Safety(t *testing.T) {
	ClearAntigravityQuotaCache()
	connID := "test-conn-123"
	model := "gemini-3.8-flash-low"

	// Initially not blocked
	if IsAntigravityModelBlocked(connID, model) {
		t.Errorf("expected not blocked initially")
	}

	// Block for 1 hour
	resetAt := time.Now().UTC().Add(1 * time.Hour)
	BlockAntigravityModelUntil(connID, model, resetAt)

	// Model should now be blocked
	if !IsAntigravityModelBlocked(connID, model) {
		t.Errorf("expected model %s to be blocked", model)
	}

	// Synonyms (e.g. gemini-3.8-flash-tiered) should also be blocked
	if !IsAntigravityModelBlocked(connID, "gemini-3.8-flash-tiered") {
		t.Errorf("expected canonical model gemini-3.8-flash-tiered to also be blocked")
	}

	// An expired resetAt should not block
	pastResetAt := time.Now().UTC().Add(-1 * time.Minute)
	BlockAntigravityModelUntil(connID, "gemini-3.8-flash-high", pastResetAt)
	if IsAntigravityModelBlocked(connID, "gemini-3.8-flash-high") {
		t.Errorf("expected expired resetAt not to block")
	}
}
