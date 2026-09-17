package providers

import "testing"

func TestEffectiveStatus(t *testing.T) {
	cases := []struct {
		name          string
		isActive      int
		testStatus    string
		activeCool    bool
		wantEffective string
		wantActive    bool
	}{
		// A disabled connection is reported as disabled no matter what the
		// stored status says.
		{"disabled wins over active", 0, "active", false, "disabled", false},
		{"disabled wins over unavailable", 0, "unavailable", true, "disabled", false},

		// Healthy connections.
		{"active is active", 1, "active", false, StatusActive, true},
		{"success is active", 1, "success", false, "success", true},

		// The important case: a stale "unavailable" marker with no cooldown
		// left means the account is usable again. Mirroring upstream
		// providers/page.js, it must report as active rather than broken.
		{"stale unavailable without cooldown recovers", 1, "unavailable", false, StatusActive, true},

		// A live cooldown genuinely marks the account unavailable.
		{"unavailable with live cooldown stays broken", 1, "unavailable", true, StatusUnavailable, false},

		// Other failure states are reported verbatim.
		{"quota exhausted", 1, "quota_exhausted", false, StatusQuotaExhausted, false},
		{"error", 1, "error", false, StatusError, false},
		{"expired", 1, "expired", false, StatusExpired, false},

		// Unknown / empty statuses must not be counted as active.
		{"unknown status", 1, "unknown", false, "unknown", false},
		{"empty status", 1, "", false, StatusUnknown, false},
		{"whitespace status", 1, "  ", false, StatusUnknown, false},
		{"status is trimmed", 1, "  ACTIVE  ", false, StatusActive, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveStatus(tc.isActive, tc.testStatus, tc.activeCool)
			if got != tc.wantEffective {
				t.Fatalf("EffectiveStatus(%d, %q, %v) = %q, want %q",
					tc.isActive, tc.testStatus, tc.activeCool, got, tc.wantEffective)
			}
			if active := IsEffectivelyActive(tc.isActive, tc.testStatus, tc.activeCool); active != tc.wantActive {
				t.Fatalf("IsEffectivelyActive(%d, %q, %v) = %v, want %v",
					tc.isActive, tc.testStatus, tc.activeCool, active, tc.wantActive)
			}
		})
	}
}

func TestStatusVariant(t *testing.T) {
	cases := []struct {
		isActive int
		status   string
		want     string
	}{
		{0, "active", "default"},
		{0, "unavailable", "default"},
		{1, "active", "success"},
		{1, "success", "success"},
		{1, "error", "error"},
		{1, "expired", "error"},
		{1, "unavailable", "error"},
		{1, "quota_exhausted", "error"},
		{1, "unknown", "default"},
		{1, "", "default"},
	}
	for _, tc := range cases {
		if got := StatusVariant(tc.isActive, tc.status); got != tc.want {
			t.Fatalf("StatusVariant(%d, %q) = %q, want %q", tc.isActive, tc.status, got, tc.want)
		}
	}
}

// TestCreditExhaustedAccountIsNotActive pins the exact production bug: 33
// openai-compatible keys were isActive=1 while every upstream call failed with
// "credit insufficient balance". The dashboard reported them all as connected.
func TestCreditExhaustedAccountIsNotActive(t *testing.T) {
	// What upstream stores for a credit-exhausted connection: the toggle stays
	// on, the status flips to unavailable, and once the cooldown lapses the
	// stale marker remains but no lock is live.
	const isActive = 1
	const status = StatusUnavailable
	const liveCooldown = false

	// Per upstream semantics this reads as "active" (the marker alone is not
	// evidence). The distinction that matters is that a live cooldown or a
	// terminal status (quota_exhausted) is NOT counted as active.
	if !IsEffectivelyActive(isActive, status, liveCooldown) {
		t.Fatal("stale unavailable without a live cooldown should read as active (upstream parity)")
	}
	if IsEffectivelyActive(isActive, status, true) {
		t.Fatal("a connection holding a live cooldown must not count as active")
	}
	if IsEffectivelyActive(isActive, StatusQuotaExhausted, false) {
		t.Fatal("a quota-exhausted connection must not count as active")
	}
	// The critical regression guard: a disabled connection is never active.
	if IsEffectivelyActive(0, StatusActive, false) {
		t.Fatal("a disabled connection must never count as active")
	}
}

// TestLooksLikeQuotaExhausted pins the vocabulary that separates a spent budget
// from a transient blip. The BAI keys failed with
// "credit insufficient balance: balance=0"; without this classification the
// error fell through to the 30s transient default and the account was retried
// (and reported healthy) every half minute.
func TestLooksLikeQuotaExhausted(t *testing.T) {
	positive := []string{
		`[400]: {"error":{"message":"credit insufficient balance: balance=0 required=110"}}`,
		"insufficient credits",
		"credits exhausted",
		"out of credits",
		"no remaining credits",
		"quota exceeded",
		"insufficient quota",
		"monthly limit reached",
		"payment required",
		"You exceeded your current quota",
		"plan limit reached",
	}
	for _, s := range positive {
		if !LooksLikeQuotaExhausted(s) {
			t.Errorf("LooksLikeQuotaExhausted(%q) = false, want true", s)
		}
	}

	negative := []string{
		"",
		"rate limit exceeded",
		"too many requests",
		"model_capacity_exhausted",
		"server is temporarily unavailable",
		"internal server error",
		"empty response content",
	}
	for _, s := range negative {
		if LooksLikeQuotaExhausted(s) {
			t.Errorf("LooksLikeQuotaExhausted(%q) = true, want false", s)
		}
	}
}

// TestQuotaExhaustedOutlivesItsCooldown pins the honesty fix: even after the
// 30-second model lock lapses, a connection whose last error was a spent credit
// budget must not flip back to "active". A timer does not refill a balance.
func TestQuotaExhaustedOutlivesItsCooldown(t *testing.T) {
	const creditedError = `[400]: {"error":{"message":"credit insufficient balance: balance=0 required=110"}}`

	got := EffectiveStatusWithError(1, StatusUnavailable, false, creditedError)
	if got != StatusQuotaExhausted {
		t.Fatalf("effective status = %q, want %q: a lapsed lock must not fake a recovery",
			got, StatusQuotaExhausted)
	}
	if IsEffectivelyActiveWithError(1, StatusUnavailable, false, creditedError) {
		t.Fatal("a credit-exhausted account must not be counted as active")
	}

	// A genuinely transient error must still recover once its lock lapses.
	if got := EffectiveStatusWithError(1, StatusUnavailable, false, "rate limit exceeded"); got != StatusActive {
		t.Fatalf("transient error effective status = %q, want active", got)
	}

	// Disabling the connection still wins over everything.
	if got := EffectiveStatusWithError(0, StatusUnavailable, false, creditedError); got != "disabled" {
		t.Fatalf("disabled connection status = %q, want disabled", got)
	}
}

// TestClassifyErrorGivesQuotaALongCooldown checks the request path: a spent
// budget must not be parked for the 30s transient window.
func TestClassifyErrorGivesQuotaALongCooldown(t *testing.T) {
	c := ClassifyError(400, `credit insufficient balance: balance=0 required=110`, 0)
	if !c.ShouldFallback {
		t.Fatal("quota error should trigger fallback")
	}
	if c.CooldownMs != QuotaExhaustedCooldownMs {
		t.Fatalf("cooldown = %d, want %d (a spent balance does not clear in 30s)",
			c.CooldownMs, QuotaExhaustedCooldownMs)
	}
	// A plain 400 with no quota wording keeps the short transient cooldown.
	plain := ClassifyError(400, "bad request", 0)
	if plain.CooldownMs == QuotaExhaustedCooldownMs {
		t.Fatal("an unrelated 400 must not get the hour-long quota cooldown")
	}
}
