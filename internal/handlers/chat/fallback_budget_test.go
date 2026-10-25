package chat

import (
	"testing"
	"time"
)

func TestFallbackBudget(t *testing.T) {
	t.Setenv("NINEROUTER_FALLBACK_BUDGET_MS", "")
	if got := fallbackBudget(); got != 60*time.Second {
		t.Fatalf("default budget = %v, want 60s", got)
	}
	t.Setenv("NINEROUTER_FALLBACK_BUDGET_MS", "5000")
	if got := fallbackBudget(); got != 5*time.Second {
		t.Fatalf("override budget = %v, want 5s", got)
	}
	t.Setenv("NINEROUTER_FALLBACK_BUDGET_MS", "abc")
	if got := fallbackBudget(); got != 60*time.Second {
		t.Fatalf("invalid override = %v, want default 60s", got)
	}
}
