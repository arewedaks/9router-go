package dashboard

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Progressive lockout for the dashboard login, ported from VansRouter's
// lib/auth/loginLimiter.js. State lives in memory only (restart clears it),
// which matches the reference and keeps the feature cheap.
const (
	loginMaxFailsBeforeLock = 5
	// Escalating lock durations: 30s, 2m, 10m, 30m. The last step repeats.
	loginFailWindow = time.Hour
)

var loginLockSteps = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	30 * time.Minute,
}

type loginAttempt struct {
	fails       int
	lockUntil   time.Time
	lockLevel   int
	lastFailAt  time.Time
	lockedUntil time.Time
}

// loginLimiter tracks failed attempts per client IP.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*loginAttempt
	now      func() time.Time // seam for tests
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: make(map[string]*loginAttempt), now: time.Now}
}

// entry returns the tracked record, expiring it when the fail window has passed
// and no lock is outstanding (mirrors getEntry in the reference).
func (l *loginLimiter) entry(ip string) *loginAttempt {
	e := l.attempts[ip]
	if e == nil {
		return nil
	}
	now := l.now()
	if !e.lastFailAt.IsZero() && now.Sub(e.lastFailAt) > loginFailWindow && (e.lockUntil.IsZero() || !now.Before(e.lockUntil)) {
		delete(l.attempts, ip)
		return nil
	}
	return e
}

// checkLock reports whether the IP is currently locked out, and for how long.
func (l *loginLimiter) checkLock(ip string) (locked bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entry(ip)
	if e == nil || e.lockUntil.IsZero() {
		return false, 0
	}
	remaining := e.lockUntil.Sub(l.now())
	if remaining <= 0 {
		return false, 0
	}
	return true, remaining
}

// recordFail counts a failed attempt and applies an escalating lock once the
// threshold is crossed. It returns how many attempts remain before the next lock.
func (l *loginLimiter) recordFail(ip string) (remainingBeforeLock int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entry(ip)
	if e == nil {
		e = &loginAttempt{}
	}
	e.fails++
	e.lastFailAt = l.now()
	if e.fails >= loginMaxFailsBeforeLock {
		step := loginLockSteps[min(e.lockLevel, len(loginLockSteps)-1)]
		e.lockUntil = l.now().Add(step)
		e.lockLevel++
		e.fails = 0
	}
	l.attempts[ip] = e
	return max(0, loginMaxFailsBeforeLock-e.fails)
}

// recordSuccess clears the IP's failure history.
func (l *loginLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

// clientIP extracts a best-effort client identifier.
//
// Trust model, identical to the reference: X-Forwarded-For is only honoured when
// TRUST_PROXY=true, because otherwise a client could rotate the header to escape
// the limiter. When we cannot identify the peer we bucket everything under a
// single key, which is the safe failure mode (a shared lockout beats no lockout).
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		return host
	}
	return "unknown"
}
