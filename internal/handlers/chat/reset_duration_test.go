package chat

import (
	"testing"
	"time"
)

// CodeBuddy reports its rate-limit reset as an ABSOLUTE local timestamp with a
// UTC offset — "your usage will reset at 2026-09-23 10:37:27 UTC+8" — not the
// relative "resets in 2h" the parser was written for. Missing it left the
// account locked for the 2s backoff floor while the upstream said hours, so
// every following request walked straight back into the same 429.
//
// The stamp is generated relative to now rather than hardcoded: a literal date
// silently becomes a past timestamp as the clock advances, and the assertion
// then fails for a reason that has nothing to do with the parser.
func TestExtractResetDuration_CodebuddyAbsoluteTimestamp(t *testing.T) {
	resetAt := time.Now().In(time.FixedZone("UTC+8", 8*3600)).Add(2 * time.Hour)
	body := []byte(`{"code":6004,"msg":"usage exceeds frequency limit, but don't worry, your usage will reset at ` +
		resetAt.Format("2006-01-02 15:04:05") +
		` UTC+8, alternatively, you can switch to the other models to continue using it.","requestId":"fc9efb08-42b4-4021-a846-193561f199a7"}`)

	got, ok := extractResetDuration(body)
	if !ok {
		t.Fatal("extractResetDuration found no reset time in the CodeBuddy 429 body")
	}
	if got <= minResetCooldown {
		t.Errorf("duration = %v; a timestamp hours out must not collapse to the backoff floor", got)
	}
	if got > maxResetCooldown {
		t.Errorf("duration = %v, want <= %v (clamped)", got, maxResetCooldown)
	}
	// Two hours out is exactly the clamp ceiling, so assert the value is close
	// to what was written rather than merely non-trivial: a dropped UTC offset
	// would still land inside the window by accident.
	if got < 90*time.Minute {
		t.Errorf("duration = %v, want ~2h; a much smaller value means the offset was mishandled", got)
	}
}

// A timestamp already in the past must not produce a negative or zero lock.
func TestExtractResetDuration_PastTimestampIsClamped(t *testing.T) {
	body := []byte(`{"code":6004,"msg":"your usage will reset at 2020-01-01 00:00:00 UTC+8"}`)

	got, ok := extractResetDuration(body)
	if !ok {
		t.Fatal("expected the past timestamp to still be recognised")
	}
	if got < minResetCooldown {
		t.Errorf("duration = %v, want >= %v so the lock is never zero-length", got, minResetCooldown)
	}
}

// The relative form the parser already supported must keep working.
func TestExtractResetDuration_RelativeStillWorks(t *testing.T) {
	body := []byte(`{"error":{"message":"Quota exceeded. Resets in 1h12m28s."}}`)

	got, ok := extractResetDuration(body)
	if !ok {
		t.Fatal("relative reset form regressed")
	}
	if got != time.Hour+12*time.Minute+28*time.Second {
		t.Errorf("duration = %v, want 1h12m28s", got)
	}
}

// The offset hour in Tencent's stamp is NOT zero-padded ("UTC+8", not
// "UTC+08"). Feeding that to time.Parse as "-07:00" fails, which silently read
// the stamp as UTC and pushed the cooldown to the 2-hour ceiling. Assert the
// offset is honoured by comparing against the same wall-clock time in UTC.
func TestParseUTCOffset_UnpaddedHourIsHonoured(t *testing.T) {
	utcSec, ok := parseUTCOffset("+8")
	if !ok {
		t.Fatal(`parseUTCOffset("+8") failed`)
	}
	if utcSec != 8*3600 {
		t.Errorf("parseUTCOffset(\"+8\") = %d s, want %d", utcSec, 8*3600)
	}

	// "+08:00" and "+8" must denote the same zone, and "-5" the negative one.
	if padded, _ := parseUTCOffset("+08:00"); padded != utcSec {
		t.Errorf(`"+08:00" = %d s, "+8" = %d s; they must agree`, padded, utcSec)
	}
	if neg, _ := parseUTCOffset("-5"); neg != -5*3600 {
		t.Errorf(`parseUTCOffset("-5") = %d s, want %d`, neg, -5*3600)
	}
}

// A CodeBuddy stamp 10 minutes out must yield roughly 10 minutes, not the
// clamped ceiling. This is the assertion the first version of the parser
// passed while being wrong: 2h is exactly the clamp, so only a value well
// inside the window proves the offset was applied.
func TestExtractResetDuration_HonoursOffsetNotClamp(t *testing.T) {
	resetAt := time.Now().In(time.FixedZone("", 8*3600)).Add(10 * time.Minute)
	body := []byte(`{"code":6004,"msg":"your usage will reset at ` +
		resetAt.Format("2006-01-02 15:04:05") + ` UTC+8"}`)

	got, ok := extractResetDuration(body)
	if !ok {
		t.Fatal("reset stamp was not recognised")
	}
	if got > 11*time.Minute || got < 9*time.Minute {
		t.Errorf("duration = %v, want ~10m; a value at the 2h clamp means the UTC offset was dropped", got)
	}
}
