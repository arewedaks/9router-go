package dashboard

import (
	"testing"
	"time"
)

func TestLoginLimiterLocksAfterMaxFails(t *testing.T) {
	l := newLoginLimiter()
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }

	// The first four failures report a shrinking budget and do not lock.
	for i := 1; i < loginMaxFailsBeforeLock; i++ {
		if got := l.recordFail("1.2.3.4"); got != loginMaxFailsBeforeLock-i {
			t.Fatalf("after %d fails remainingBeforeLock = %d, want %d", i, got, loginMaxFailsBeforeLock-i)
		}
		if locked, _ := l.checkLock("1.2.3.4"); locked {
			t.Fatalf("locked after only %d fails", i)
		}
	}

	// The fifth failure crosses the threshold and applies the first lock step.
	l.recordFail("1.2.3.4")
	locked, retryAfter := l.checkLock("1.2.3.4")
	if !locked {
		t.Fatal("expected a lock after reaching the failure threshold")
	}
	if retryAfter <= 0 || retryAfter > loginLockSteps[0] {
		t.Fatalf("retryAfter = %v, want within the first lock step", retryAfter)
	}
}

func TestLoginLimiterEscalatesLockDuration(t *testing.T) {
	l := newLoginLimiter()
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	ip := "9.9.9.9"

	var last time.Duration
	for round := 0; round < 3; round++ {
		for i := 0; i < loginMaxFailsBeforeLock; i++ {
			l.recordFail(ip)
		}
		locked, retryAfter := l.checkLock(ip)
		if !locked {
			t.Fatalf("round %d: expected a lock", round)
		}
		if retryAfter <= last {
			t.Fatalf("round %d: retryAfter %v did not escalate past %v", round, retryAfter, last)
		}
		last = retryAfter
		// Jump past the lock so the next round of failures is counted.
		now = now.Add(retryAfter + time.Second)
	}
}

func TestLoginLimiterLockExpires(t *testing.T) {
	l := newLoginLimiter()
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	ip := "5.5.5.5"

	for i := 0; i < loginMaxFailsBeforeLock; i++ {
		l.recordFail(ip)
	}
	if locked, _ := l.checkLock(ip); !locked {
		t.Fatal("expected a lock")
	}
	now = now.Add(loginLockSteps[0] + time.Second)
	if locked, _ := l.checkLock(ip); locked {
		t.Fatal("the lock must expire once its duration passes")
	}
}

func TestLoginLimiterSuccessClearsHistory(t *testing.T) {
	l := newLoginLimiter()
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	ip := "7.7.7.7"

	for i := 0; i < loginMaxFailsBeforeLock-1; i++ {
		l.recordFail(ip)
	}
	l.recordSuccess(ip)
	// After a success, a full fresh budget must be available again.
	if got := l.recordFail(ip); got != loginMaxFailsBeforeLock-1 {
		t.Fatalf("remainingBeforeLock = %d after a successful login, want a fresh budget", got)
	}
}

func TestLoginLimiterTracksIPsIndependently(t *testing.T) {
	l := newLoginLimiter()
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }

	for i := 0; i < loginMaxFailsBeforeLock; i++ {
		l.recordFail("1.1.1.1")
	}
	if locked, _ := l.checkLock("1.1.1.1"); !locked {
		t.Fatal("the offending IP must be locked")
	}
	if locked, _ := l.checkLock("2.2.2.2"); locked {
		t.Fatal("an unrelated IP must not be affected")
	}
}
