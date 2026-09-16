package oauth

import (
	"sync"
)

// Single-flight refresh coordination.
//
// WHY THIS EXISTS
// Refresh tokens are single-use at most providers: once a refresh token is
// exchanged, the old one is invalidated. If two goroutines refresh the SAME
// connection concurrently — e.g. a chat request and a "Test Connection" click
// landing in the same second — both send the same refresh token, the loser gets
// `refresh_token_reused` / `invalid_grant`, and a healthy account is bricked
// until the user signs in again.
//
// The chat path already refreshes on demand (refreshOAuthTokenIfExpired), but
// it had no per-connection lock. This file adds one, keyed by connection ID, and
// is the ONLY entry point the app should use to refresh a stored connection. The
// dashboard "Test Connection" probe reuses it too, so a manual test and a live
// chat request can never race each other.
//
// The lock is per connection, not global: refreshing account A never blocks
// account B.

var (
	refreshLocksMu sync.Mutex
	refreshLocks   = map[string]*sync.Mutex{}
)

// refreshLockFor returns the mutex guarding refreshes for a single connection.
// The map holds one small mutex per connection ID and is never pruned; the key
// space is bounded by the number of connections an operator actually has, which
// is small (dozens, not millions), so the memory cost is negligible.
func refreshLockFor(connectionID string) *sync.Mutex {
	refreshLocksMu.Lock()
	defer refreshLocksMu.Unlock()
	mu, ok := refreshLocks[connectionID]
	if !ok {
		mu = &sync.Mutex{}
		refreshLocks[connectionID] = mu
	}
	return mu
}

// WithRefreshLock runs fn while holding the per-connection refresh lock.
//
// The chat path and the dashboard probe both refresh through
// RefreshStoredConnection, which calls this. Keeping the lock helper unexported
// would force those callers to reach into the map, so it stays exported for the
// rare case a caller must hold the lock across a multi-step refresh.
func WithRefreshLock(connectionID string, fn func() error) error {
	mu := refreshLockFor(connectionID)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}
