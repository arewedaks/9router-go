package oauth

import (
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newStoreTestDB builds an in-memory providerConnections table with the columns
// RefreshStoredConnection reads and writes.
func newStoreTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	_, err = database.Exec(`CREATE TABLE providerConnections (
		id TEXT PRIMARY KEY,
		provider TEXT NOT NULL,
		authType TEXT,
		name TEXT,
		priority INTEGER,
		isActive INTEGER,
		data TEXT,
		createdAt TEXT,
		updatedAt TEXT
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// TestRefreshLockFor_ReturnsSameMutexPerConnection pins the core invariant the
// whole fix rests on: the same connection ID must always map to the same lock,
// and different connections must NOT share one. If this regressed, refreshes
// could race (or unrelated accounts would block each other).
func TestRefreshLockFor_ReturnsSameMutexPerConnection(t *testing.T) {
	a1 := refreshLockFor("conn-a")
	a2 := refreshLockFor("conn-a")
	b := refreshLockFor("conn-b")

	if a1 != a2 {
		t.Fatal("same connection id must return the identical mutex")
	}
	if a1 == b {
		t.Fatal("different connection ids must not share a mutex")
	}
}

// TestWithRefreshLock_Serialises verifies the lock actually excludes concurrent
// critical sections. Without it, two refreshes would overlap and both would send
// the same single-use refresh token.
func TestWithRefreshLock_Serialises(t *testing.T) {
	var inFlight int32
	var maxInFlight int32
	var wg sync.WaitGroup

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = WithRefreshLock("conn-serial", func() error {
				n := atomic.AddInt32(&inFlight, 1)
				for {
					cur := atomic.LoadInt32(&maxInFlight)
					if n <= cur || atomic.CompareAndSwapInt32(&maxInFlight, cur, n) {
						break
					}
				}
				time.Sleep(2 * time.Millisecond) // widen the race window
				atomic.AddInt32(&inFlight, -1)
				return nil
			})
		}()
	}
	wg.Wait()

	if maxInFlight > 1 {
		t.Fatalf("critical section was entered %d times concurrently; lock did not serialise", maxInFlight)
	}
}

// TestWithRefreshLock_DifferentConnectionsDoNotBlock proves the lock is
// per-connection, not global: a slow refresh on account A must not stall
// account B. A global lock would serialise an entire provider's accounts.
func TestWithRefreshLock_DifferentConnectionsDoNotBlock(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})

	go func() {
		_ = WithRefreshLock("conn-slow", func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	// While conn-slow holds its lock, conn-fast must still acquire its own.
	done := make(chan struct{})
	go func() {
		_ = WithRefreshLock("conn-fast", func() error { return nil })
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("a different connection's lock blocked; lock is not per-connection")
	}
	close(release)
}

// TestBuildConnectionUpdate_SetsExpiry pins the persisted shape: a refresh must
// write a future expiresAt, otherwise the next request would see the token as
// still expired and refresh again on every call.
func TestBuildConnectionUpdate_SetsExpiry(t *testing.T) {
	update := BuildConnectionUpdate(&TokenResult{
		AccessToken:  "new-token",
		ExpiresIn:    3600,
		RefreshToken: "new-refresh",
	})
	if update["accessToken"] != "new-token" {
		t.Fatalf("accessToken not persisted: %#v", update)
	}
	if update["refreshToken"] != "new-refresh" {
		t.Fatalf("rotated refreshToken not persisted: %#v", update)
	}
	exp, ok := update["expiresAt"].(string)
	if !ok || exp == "" {
		t.Fatalf("expiresAt missing: %#v", update)
	}
	parsed, err := time.Parse(time.RFC3339, exp)
	if err != nil {
		t.Fatalf("expiresAt is not RFC3339: %v", err)
	}
	if !parsed.After(time.Now().Add(30 * time.Minute)) {
		t.Fatalf("expiresAt is not in the near future: %s", exp)
	}
}
