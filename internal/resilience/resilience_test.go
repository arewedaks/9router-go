package resilience

import (
	"context"
	"errors"
	"testing"
	"time"
)

// newTestBreaker returns a breaker with a controllable clock.
func newTestBreaker(t *testing.T, opts BreakerOptions) (*Breaker, func(time.Duration)) {
	t.Helper()
	now := time.Now()
	b := &Breaker{
		name:  "test",
		opts:  opts.withDefaults(),
		state: StateClosed,
		now:   func() time.Time { return now },
	}
	advance := func(d time.Duration) { now = now.Add(d) }
	return b, advance
}

// A fresh breaker passes requests.
func TestBreaker_StartsClosedAndAllows(t *testing.T) {
	b, _ := newTestBreaker(t, BreakerOptions{})
	if b.State() != StateClosed {
		t.Fatalf("state = %s, want CLOSED", b.State())
	}
	if !b.Allow() {
		t.Fatal("closed breaker refused a request")
	}
}

// Failures below the threshold move the breaker to DEGRADED, which still
// admits requests — degraded is a warning, not a block.
func TestBreaker_DegradesBeforeOpening(t *testing.T) {
	b, _ := newTestBreaker(t, BreakerOptions{FailureThreshold: 5})
	for i := 0; i < 3; i++ {
		b.RecordFailure()
	}
	if b.State() != StateDegraded {
		t.Fatalf("state = %s, want DEGRADED after 3/5 failures", b.State())
	}
	if !b.Allow() {
		t.Fatal("degraded breaker must still admit requests")
	}
}

// Reaching the threshold opens the breaker, which then refuses requests.
func TestBreaker_OpensAtThreshold(t *testing.T) {
	b, _ := newTestBreaker(t, BreakerOptions{FailureThreshold: 5})
	for i := 0; i < 5; i++ {
		b.RecordFailure()
	}
	if b.State() != StateOpen {
		t.Fatalf("state = %s, want OPEN", b.State())
	}
	if b.Allow() {
		t.Fatal("open breaker admitted a request before the reset timeout")
	}
	if b.RetryAfter() <= 0 {
		t.Error("open breaker reported no retry delay")
	}
}

// After the reset timeout the breaker admits a single probe, and a successful
// probe closes it.
func TestBreaker_HalfOpenThenClosesOnSuccess(t *testing.T) {
	b, advance := newTestBreaker(t, BreakerOptions{FailureThreshold: 2, ResetTimeout: 30 * time.Second})
	b.RecordFailure()
	b.RecordFailure()
	if b.State() != StateOpen {
		t.Fatalf("state = %s, want OPEN", b.State())
	}

	advance(31 * time.Second)
	if !b.Allow() {
		t.Fatal("breaker refused the recovery probe after the reset timeout")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("state = %s, want HALF_OPEN", b.State())
	}

	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Fatalf("state = %s, want CLOSED after a successful probe", b.State())
	}
}

// A failed probe reopens the breaker and doubles the reset timeout.
func TestBreaker_FailedProbeReopensAndBacksOff(t *testing.T) {
	b, advance := newTestBreaker(t, BreakerOptions{
		FailureThreshold: 1, ResetTimeout: 30 * time.Second,
		BackoffEscalationCount: 1, MaxBackoffMultiplier: 16,
	})
	b.RecordFailure()
	base := b.RetryAfter()

	advance(base + time.Second)
	b.Allow() // transitions to HALF_OPEN
	b.RecordFailure()

	if b.State() != StateOpen {
		t.Fatalf("state = %s, want OPEN after a failed probe", b.State())
	}
	if got := b.RetryAfter(); got <= base {
		t.Errorf("retry after = %s, want > %s (backoff must escalate)", got, base)
	}
}

// A probe that is admitted but never reports back must not strand the breaker
// in HALF_OPEN: after the reset timeout it reopens and can probe again.
func TestBreaker_LostProbeDoesNotStallHalfOpen(t *testing.T) {
	b, advance := newTestBreaker(t, BreakerOptions{FailureThreshold: 1, ResetTimeout: 30 * time.Second})
	b.RecordFailure()

	advance(31 * time.Second)
	if !b.Allow() {
		t.Fatal("expected the recovery probe to be admitted")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("state = %s, want HALF_OPEN", b.State())
	}

	// The probe never records an outcome. A second request must be refused
	// while the probe is outstanding, but the breaker must not stay stuck.
	if b.Allow() {
		t.Fatal("second request admitted while a probe was outstanding")
	}

	advance(31 * time.Second)
	if b.Allow() {
		t.Fatal("stale probe: breaker admitted a request instead of reopening")
	}
	if b.State() != StateOpen {
		t.Fatalf("state = %s, want OPEN after the probe went unanswered", b.State())
	}

	// And it must recover from there.
	advance(200 * time.Second)
	if !b.Allow() {
		t.Fatal("breaker did not admit a fresh probe after reopening")
	}
	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Fatalf("state = %s, want CLOSED", b.State())
	}
}

// The backoff multiplier is capped, so repeated failures cannot push the reset
// timeout out indefinitely.
func TestBreaker_BackoffIsCapped(t *testing.T) {
	b, advance := newTestBreaker(t, BreakerOptions{
		FailureThreshold: 1, ResetTimeout: time.Second,
		BackoffEscalationCount: 1, MaxBackoffMultiplier: 4,
	})
	b.RecordFailure()
	for i := 0; i < 10; i++ {
		advance(1000 * time.Second)
		b.Allow()
		b.RecordFailure()
	}
	if got, max := b.RetryAfter(), 4*time.Second; got > max {
		t.Errorf("retry after = %s, want <= %s (cap)", got, max)
	}
}

// 429 must not count toward the breaker: it is per-account rate limiting, and
// counting it would open the breaker for every healthy account too.
func TestIsProviderFailureStatus_Excludes429(t *testing.T) {
	if IsProviderFailureStatus(429) {
		t.Error("429 must not count as a provider-level failure")
	}
	for _, code := range []int{408, 500, 502, 503, 504, 520, 524} {
		if !IsProviderFailureStatus(code) {
			t.Errorf("status %d must count as a provider-level failure", code)
		}
	}
	if IsProviderFailureStatus(400) || IsProviderFailureStatus(401) || IsProviderFailureStatus(403) {
		t.Error("client errors must not count as provider-level failures")
	}
}

// BreakerFor must return the same instance for a provider so state is shared.
func TestBreakerFor_IsStablePerProvider(t *testing.T) {
	ResetAllBreakers()
	t.Cleanup(ResetAllBreakers)
	a := BreakerFor("provider-x", BreakerOptions{})
	b := BreakerFor("provider-x", BreakerOptions{FailureThreshold: 99})
	if a != b {
		t.Fatal("BreakerFor returned a different breaker for the same provider")
	}
	if a.opts.FailureThreshold == 99 {
		t.Error("an existing breaker's options were overwritten by a later call")
	}
}

// A semaphore below its cap admits immediately and releases the slot.
func TestSemaphore_AdmitsUpToCap(t *testing.T) {
	ResetSemaphores()
	t.Cleanup(ResetSemaphores)

	key := SemaphoreKey("p", "acct", "")
	r1, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	stats := SemaphoreStats()
	if len(stats) != 1 || stats[0].Running != 1 {
		t.Fatalf("stats = %+v, want 1 running", stats)
	}
	r1()

	// The slot must be free again.
	r2, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	r2()
}

// A request beyond the cap waits, and is admitted when the holder releases.
func TestSemaphore_QueuesAndDrains(t *testing.T) {
	ResetSemaphores()
	t.Cleanup(ResetSemaphores)

	key := SemaphoreKey("p", "acct", "")
	release, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		r, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1, Timeout: 2 * time.Second})
		if err != nil {
			t.Errorf("queued acquire: %v", err)
			close(acquired)
			return
		}
		close(acquired)
		r()
	}()

	// The queued request must not be admitted while the slot is held.
	select {
	case <-acquired:
		t.Fatal("queued request was admitted before the slot was released")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("queued request was never admitted after the release")
	}
}

// A full queue is refused immediately rather than waiting for a timeout.
func TestSemaphore_RefusesWhenQueueFull(t *testing.T) {
	ResetSemaphores()
	t.Cleanup(ResetSemaphores)

	key := SemaphoreKey("p", "acct", "")
	release, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1, MaxQueueSize: 1, Timeout: time.Minute})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	// Fill the single queue slot.
	go func() {
		r, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1, MaxQueueSize: 1, Timeout: time.Minute})
		if err == nil {
			r()
		}
	}()
	time.Sleep(50 * time.Millisecond)

	if _, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1, MaxQueueSize: 1, Timeout: time.Minute}); !errors.Is(err, ErrSemaphoreCapacity) {
		t.Fatalf("err = %v, want ErrSemaphoreCapacity", err)
	}
}

// Waiting past the timeout fails rather than blocking forever.
func TestSemaphore_TimesOut(t *testing.T) {
	ResetSemaphores()
	t.Cleanup(ResetSemaphores)

	key := SemaphoreKey("p", "acct", "")
	release, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	start := time.Now()
	if _, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1, Timeout: 100 * time.Millisecond}); !errors.Is(err, ErrSemaphoreCapacity) {
		t.Fatalf("err = %v, want ErrSemaphoreCapacity", err)
	}
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Errorf("returned after %s, expected to wait for the timeout", elapsed)
	}
}

// A cancelled context releases the wait rather than hanging.
func TestSemaphore_RespectsContextCancel(t *testing.T) {
	ResetSemaphores()
	t.Cleanup(ResetSemaphores)

	key := SemaphoreKey("p", "acct", "")
	release, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	if _, err := Acquire(ctx, key, SemaphoreOptions{MaxConcurrency: 1, Timeout: time.Minute}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A blocked gate holds waiters until the block expires.
func TestSemaphore_BlockHoldsWaiters(t *testing.T) {
	ResetSemaphores()
	t.Cleanup(ResetSemaphores)

	key := SemaphoreKey("p", "acct", "")
	// Create the gate first.
	release, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()

	Block(key, 150*time.Millisecond)

	start := time.Now()
	r, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 1, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("acquire after block: %v", err)
	}
	defer r()
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("admitted after %s, expected to wait out the block", elapsed)
	}
}

// maxConcurrency <= 0 bypasses the limiter, so a provider that has not opted in
// is never delayed.
func TestSemaphore_ZeroConcurrencyBypasses(t *testing.T) {
	ResetSemaphores()
	t.Cleanup(ResetSemaphores)

	key := SemaphoreKey("p", "acct", "")
	for i := 0; i < 10; i++ {
		r, err := Acquire(context.Background(), key, SemaphoreOptions{MaxConcurrency: 0})
		if err != nil {
			t.Fatalf("bypassed acquire %d: %v", i, err)
		}
		r()
	}
	if stats := SemaphoreStats(); len(stats) != 0 {
		t.Errorf("bypassed acquires created gates: %+v", stats)
	}
}

// Different proxy hashes must not share a gate: one dead proxy's limit cannot
// throttle another's traffic.
func TestSemaphore_ProxyHashSeparatesGates(t *testing.T) {
	ResetSemaphores()
	t.Cleanup(ResetSemaphores)

	k1 := SemaphoreKey("p", "acct", "proxy-a")
	k2 := SemaphoreKey("p", "acct", "proxy-b")
	if k1 == k2 {
		t.Fatal("different proxies produced the same gate key")
	}

	r1, err := Acquire(context.Background(), k1, SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("acquire proxy-a: %v", err)
	}
	defer r1()

	// proxy-b has its own slot and must not be blocked by proxy-a's holder.
	r2, err := Acquire(context.Background(), k2, SemaphoreOptions{MaxConcurrency: 1, Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatalf("acquire proxy-b was blocked by proxy-a: %v", err)
	}
	r2()
}
