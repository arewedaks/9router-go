package providers

import (
	"net/http"
	"testing"
)

func TestClassifyError_CapacityExhausted(t *testing.T) {
	// Status 503 with MODEL_CAPACITY_EXHAUSTED
	cls := ClassifyError(http.StatusServiceUnavailable, "No capacity available for model gemini-3.8-flash-tiered on the server MODEL_CAPACITY_EXHAUSTED", 0)
	if !cls.ShouldFallback {
		t.Error("expected fallback for capacity exhausted error")
	}
	if cls.NewBackoffLevel != 1 {
		t.Errorf("expected backoff level 1, got %d", cls.NewBackoffLevel)
	}
	if cls.CooldownMs != 2000 {
		t.Errorf("expected 2000ms cooldown, got %d", cls.CooldownMs)
	}
}

func TestClassifyError_ResourceExhausted429(t *testing.T) {
	cls := ClassifyError(http.StatusTooManyRequests, "Resource has been exhausted (e.g. check quota). RESOURCE_EXHAUSTED", 0)
	if !cls.ShouldFallback {
		t.Error("expected fallback for 429 resource exhausted")
	}
	if cls.NewBackoffLevel != 1 {
		t.Errorf("expected backoff level 1, got %d", cls.NewBackoffLevel)
	}
}

func TestClassifyError_Status503Fallback(t *testing.T) {
	// Plain 503 without specific matching text should trigger backoff via status rule
	cls := ClassifyError(http.StatusServiceUnavailable, "", 0)
	if !cls.ShouldFallback {
		t.Error("expected fallback for 503")
	}
	if cls.NewBackoffLevel != 1 {
		t.Errorf("expected backoff level 1 for 503, got %d", cls.NewBackoffLevel)
	}
}

func TestClassifyError_FreeTierAllowanceSpent(t *testing.T) {
	// TokenHarbor returns 429 for a spent free tier. Without a text rule the
	// status alone produced a 2s backoff, so the exhausted account was retried
	// all window instead of being parked like the other spent-budget cases.
	const msg = "You've used this period's free allowance. Your next rolling " +
		"7-day period starts on 16 Oct 2026 at 15:05 UTC."

	cls := ClassifyError(http.StatusTooManyRequests, msg, 0)
	if !cls.ShouldFallback {
		t.Error("expected fallback for a spent free allowance")
	}
	if cls.CooldownMs != QuotaExhaustedCooldownMs {
		t.Errorf("cooldown = %dms, want %dms (quota-exhausted, not the transient backoff)",
			cls.CooldownMs, QuotaExhaustedCooldownMs)
	}
}
