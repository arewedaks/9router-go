package dashboard

import (
	"net/http"
	"net/http/httptest"
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

// The service listens on its public port as well as behind the reverse proxy.
// On the direct path nothing overwrites X-Forwarded-For, so honouring the header
// there let an attacker send a fresh value per request and get a fresh limiter
// bucket every time — unlimited password guessing. Only a loopback peer may
// supply it, because that is the only hop that is actually the proxy.
func TestClientIPRefusesSpoofedHeaderFromPublicPeer(t *testing.T) {
	req := func(remote, xff string) string {
		r := httptest.NewRequest(http.MethodPost, "/api/dashboard/auth/login", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return clientIP(r, true)
	}

	// Direct public connection: the forged header must be ignored and the
	// spoofed values must never become distinct limiter keys.
	for _, xff := range []string{"1.2.3.4", "9.9.9.9", "203.0.113.7"} {
		if got := req("198.51.100.20:4444", xff); got != "198.51.100.20" {
			t.Errorf("public peer with XFF=%q resolved to %q, want the socket peer; "+
				"a rotating header would bypass the lockout", xff, got)
		}
	}

	// Behind the proxy the hop is loopback and Caddy replaces the header with
	// the real client, so that value is trusted.
	if got := req("127.0.0.1:5000", "203.0.113.9"); got != "203.0.113.9" {
		t.Errorf("proxied request resolved to %q, want the forwarded client", got)
	}
	if got := req("[::1]:5000", "203.0.113.10"); got != "203.0.113.10" {
		t.Errorf("IPv6 loopback proxy hop resolved to %q, want the forwarded client", got)
	}

	// TRUST_PROXY off means the header is ignored even from loopback.
	r := httptest.NewRequest(http.MethodPost, "/api/dashboard/auth/login", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.11")
	if got := clientIP(r, false); got != "127.0.0.1" {
		t.Errorf("with TRUST_PROXY off resolved to %q, want the socket peer", got)
	}
}
