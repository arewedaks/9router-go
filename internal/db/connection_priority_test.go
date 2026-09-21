package db

import (
	"os"
	"testing"
)

// newPriorityTestRepo builds an isolated DB with just the columns the ranking
// helper reads.
func newPriorityTestRepo(t *testing.T) *Repo {
	t.Helper()
	f, err := os.CreateTemp("", "prio_*.sqlite")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	database, err := OpenDatabase(f.Name())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if _, err := database.Exec(`CREATE TABLE providerConnections (
		id TEXT PRIMARY KEY, provider TEXT NOT NULL, authType TEXT,
		name TEXT, email TEXT, priority INTEGER, isActive INTEGER DEFAULT 1,
		data TEXT, createdAt TEXT, updatedAt TEXT,
		lastUsedAt TEXT, consecutiveUseCount INTEGER DEFAULT 0)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return NewRepo(database)
}

func seedPrio(t *testing.T, r *Repo, id, provider string, priority any) {
	t.Helper()
	if _, err := r.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, name, priority, isActive) VALUES (?, ?, ?, ?, 1)`,
		id, provider, id, priority,
	); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// An empty provider has no ranking to continue from, so the first account is 1.
func TestNextConnectionPriorityStartsAtOne(t *testing.T) {
	r := newPriorityTestRepo(t)
	got, err := r.NextConnectionPriority("antigravity")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1 {
		t.Errorf("first priority = %d, want 1", got)
	}
}

// The newcomer must land AFTER the existing accounts. It is appended to the
// queue, not prepended.
func TestNextConnectionPriorityAppendsAfterExisting(t *testing.T) {
	r := newPriorityTestRepo(t)
	for i, id := range []string{"a", "b", "c"} {
		seedPrio(t, r, id, "antigravity", i+1)
	}
	got, err := r.NextConnectionPriority("antigravity")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 4 {
		t.Errorf("next priority = %d, want 4 (max 3 + 1)", got)
	}
}

// This is the regression guard for the invisible-account bug: rows saved
// without a priority (NULL) must be IGNORED when computing the maximum. If they
// counted as 0 the newcomer would collide with the first ranked account, and if
// they somehow counted high the queue would jump. Either way a NULL row must not
// decide the answer.
func TestNextConnectionPriorityIgnoresNullRows(t *testing.T) {
	r := newPriorityTestRepo(t)
	seedPrio(t, r, "ranked-1", "antigravity", 1)
	seedPrio(t, r, "ranked-5", "antigravity", 5)
	seedPrio(t, r, "unranked", "antigravity", nil)

	got, err := r.NextConnectionPriority("antigravity")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 6 {
		t.Errorf("next priority = %d, want 6 (max ranked 5 + 1, NULL ignored)", got)
	}
}

// A provider whose accounts are ALL unranked still needs 1, not 0 or an error —
// that is the state a fresh install is in.
func TestNextConnectionPriorityAllNullIsOne(t *testing.T) {
	r := newPriorityTestRepo(t)
	seedPrio(t, r, "unranked-1", "antigravity", nil)
	seedPrio(t, r, "unranked-2", "antigravity", nil)

	got, err := r.NextConnectionPriority("antigravity")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1 {
		t.Errorf("next priority = %d, want 1", got)
	}
}

// Ranking is per provider: a crowded provider must not push another provider's
// first account off 1.
func TestNextConnectionPriorityIsPerProvider(t *testing.T) {
	r := newPriorityTestRepo(t)
	seedPrio(t, r, "ag-1", "antigravity", 7)
	seedPrio(t, r, "ag-2", "antigravity", 8)

	got, err := r.NextConnectionPriority("codex")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1 {
		t.Errorf("codex first priority = %d, want 1 (antigravity's 8 must not leak)", got)
	}
}

// A query error must surface, not be swallowed into a default rank: silently
// returning 1 would create a duplicate priority and reorder the rotation.
func TestNextConnectionPriorityReportsError(t *testing.T) {
	r := newPriorityTestRepo(t)
	if _, err := r.RawDB().Exec("DROP TABLE providerConnections"); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := r.NextConnectionPriority("antigravity"); err == nil {
		t.Error("expected an error when the table is missing, got nil")
	}
}
