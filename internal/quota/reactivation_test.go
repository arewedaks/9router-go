package quota

import (
	json "encoding/json/v2"
	"os"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

func newQuotaTestRepo(t *testing.T) *db.Repo {
	t.Helper()
	f, err := os.CreateTemp("", "quota_*.sqlite")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	database, err := db.OpenDatabase(f.Name())
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
	return db.NewRepo(database)
}

// seedConn inserts a connection with the given blob and active flag.
func seedConn(t *testing.T, repo *db.Repo, id, provider string, isActive int, data map[string]any) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, ?, 'apikey', ?, 1, ?, ?, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`,
		id, provider, id, isActive, string(raw)); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func readConn(t *testing.T, repo *db.Repo, id string) (int, map[string]any) {
	t.Helper()
	var isActive int
	var raw string
	if err := repo.DB().QueryRow(
		"SELECT isActive, data FROM providerConnections WHERE id = ?", id,
	).Scan(&isActive, &raw); err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("decode %s: %v", id, err)
	}
	return isActive, data
}

// An exhausted account whose cooldown has passed is restored, both the blob and
// the isActive flag — clearing only the blob would leave it excluded from the
// active-connection query.
func TestReactivateExpired_RestoresExpiredAccount(t *testing.T) {
	repo := newQuotaTestRepo(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	seedConn(t, repo, "kimchi-1", "kimchi", 0, map[string]any{
		"apiKey":           "k",
		"testStatus":       "quota_exhausted",
		"rateLimitedUntil": now.Add(-time.Hour).Format(time.RFC3339),
		"lastError":        "insufficient credits",
	})

	if n := ReactivateExpired(repo, now); n != 1 {
		t.Fatalf("reactivated = %d, want 1", n)
	}

	isActive, data := readConn(t, repo, "kimchi-1")
	if isActive != 1 {
		t.Error("isActive = 0, want 1 (a reactivated account must rejoin the active set)")
	}
	if data["testStatus"] != "active" {
		t.Errorf("testStatus = %v, want active", data["testStatus"])
	}
	if data["rateLimitedUntil"] != nil {
		t.Errorf("rateLimitedUntil = %v, want nil", data["rateLimitedUntil"])
	}
	if data["lastError"] != nil {
		t.Errorf("lastError = %v, want nil", data["lastError"])
	}
	if data["apiKey"] != "k" {
		t.Errorf("apiKey = %v, want the credential preserved", data["apiKey"])
	}
}

// An account whose cooldown has not passed stays parked.
func TestReactivateExpired_LeavesUnexpiredAlone(t *testing.T) {
	repo := newQuotaTestRepo(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	seedConn(t, repo, "kimchi-1", "kimchi", 0, map[string]any{
		"testStatus":       "quota_exhausted",
		"rateLimitedUntil": now.Add(time.Hour).Format(time.RFC3339),
	})

	if n := ReactivateExpired(repo, now); n != 0 {
		t.Fatalf("reactivated = %d, want 0 (cooldown still active)", n)
	}
	if isActive, _ := readConn(t, repo, "kimchi-1"); isActive != 0 {
		t.Error("account was reactivated before its cooldown expired")
	}
}

// An account parked for a reason other than quota must not be revived by a
// stale timestamp: the status check is what prevents that.
func TestReactivateExpired_IgnoresNonQuotaStatus(t *testing.T) {
	repo := newQuotaTestRepo(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	seedConn(t, repo, "kimchi-banned", "kimchi", 0, map[string]any{
		"testStatus":       "unavailable",
		"rateLimitedUntil": now.Add(-time.Hour).Format(time.RFC3339),
	})

	if n := ReactivateExpired(repo, now); n != 0 {
		t.Fatalf("reactivated = %d, want 0 (status is not quota_exhausted)", n)
	}
}

// A missing or unparseable cooldown leaves the account alone rather than
// reviving it on a guess.
func TestReactivateExpired_RequiresParseableCooldown(t *testing.T) {
	repo := newQuotaTestRepo(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	seedConn(t, repo, "no-timestamp", "kimchi", 0, map[string]any{
		"testStatus": "quota_exhausted",
	})
	seedConn(t, repo, "bad-timestamp", "kimchi", 0, map[string]any{
		"testStatus":       "quota_exhausted",
		"rateLimitedUntil": "not-a-timestamp",
	})

	if n := ReactivateExpired(repo, now); n != 0 {
		t.Fatalf("reactivated = %d, want 0", n)
	}
}

// Only the providers whose quota resets on a calendar boundary are swept.
func TestReactivateExpired_IgnoresOtherProviders(t *testing.T) {
	repo := newQuotaTestRepo(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	seedConn(t, repo, "deepseek-1", "deepseek", 0, map[string]any{
		"testStatus":       "quota_exhausted",
		"rateLimitedUntil": now.Add(-time.Hour).Format(time.RFC3339),
	})

	if n := ReactivateExpired(repo, now); n != 0 {
		t.Fatalf("reactivated = %d, want 0 (deepseek is not monthly-quota)", n)
	}
}

// A sweep with nothing to do must not fail or reactivate anything.
func TestReactivateExpired_EmptyDatabase(t *testing.T) {
	repo := newQuotaTestRepo(t)
	if n := ReactivateExpired(repo, time.Now()); n != 0 {
		t.Fatalf("reactivated = %d, want 0", n)
	}
}

// shouldReactivate is the decision function on its own, exercised against the
// blob shapes that reach it.
func TestShouldReactivate(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour).Format(time.RFC3339)
	future := now.Add(time.Hour).Format(time.RFC3339)

	cases := []struct {
		name string
		data map[string]any
		want bool
	}{
		{"expired quota", map[string]any{"testStatus": "quota_exhausted", "rateLimitedUntil": past}, true},
		{"still cooling", map[string]any{"testStatus": "quota_exhausted", "rateLimitedUntil": future}, false},
		{"wrong status", map[string]any{"testStatus": "active", "rateLimitedUntil": past}, false},
		{"no timestamp", map[string]any{"testStatus": "quota_exhausted"}, false},
		{"empty timestamp", map[string]any{"testStatus": "quota_exhausted", "rateLimitedUntil": ""}, false},
		{"malformed", map[string]any{"testStatus": "quota_exhausted", "rateLimitedUntil": "nope"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := json.Marshal(c.data)
			if got := shouldReactivate(string(raw), now); got != c.want {
				t.Errorf("shouldReactivate(%v) = %v, want %v", c.data, got, c.want)
			}
		})
	}
}

// A malformed blob must not panic or reactivate.
func TestShouldReactivate_MalformedJSON(t *testing.T) {
	if shouldReactivate("{not json", time.Now()) {
		t.Error("malformed JSON was treated as reactivatable")
	}
}
