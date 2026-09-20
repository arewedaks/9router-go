package oauth

import (
	"database/sql"
	json "encoding/json/v2"
	"os"
	"testing"

	"9router/proxy/internal/db"
)

// newDuplicateTestRepo builds an isolated DB with the providerConnections table
// the duplicate check reads.
func newDuplicateTestRepo(t *testing.T) *db.Repo {
	t.Helper()
	f, err := os.CreateTemp("", "dup_*.sqlite")
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

func insertConn(t *testing.T, repo *db.Repo, id, provider, name, email string, data map[string]any) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var emailVal any
	if email == "" {
		emailVal = nil
	} else {
		emailVal = email
	}
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, ?, 'oauth', ?, ?, 1, 1, ?, '2026-09-10T00:00:00Z', '2026-09-10T00:00:00Z')`,
		id, provider, name, emailVal, string(raw)); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

// TestFindDuplicateConnection_CodebuddyRealCase reproduces the exact situation
// found in the production database: two codebuddy-intl rows whose access tokens
// differ (issued six days apart) but whose JWT `sub` is identical.
func TestFindDuplicateConnection_CodebuddyRealCase(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	sub := "88e091b3-a29f-4ed5-992c-187ac660e262"
	oldToken := makeJWT(t, map[string]any{"sub": sub, "email": "tempeduai9@gmail.com", "iat": 1789059859})
	newToken := makeJWT(t, map[string]any{"sub": sub, "email": "tempeduai9@gmail.com", "iat": 1789299451})

	// The pre-existing connection (Account 1, created 2026-09-10).
	insertConn(t, repo, "a003682e-06c9-4b88-9940-22c6b22a950c", "codebuddy-intl", "Account 1", "",
		map[string]any{"accessToken": oldToken, "refreshToken": "old-refresh"})

	// The candidate is a re-login of the same account.
	candidate := IdentityForProvider("codebuddy-intl", newToken, "tempeduai9@gmail.com")
	dup, err := h.FindDuplicateConnection("codebuddy-intl", candidate)
	if err != nil {
		t.Fatalf("FindDuplicateConnection: %v", err)
	}
	if dup == nil {
		t.Fatal("expected the re-login to be detected as a duplicate; a fresh token hid the same account")
	}
	if dup.ExistingConnID != "a003682e-06c9-4b88-9940-22c6b22a950c" {
		t.Errorf("duplicate points at %q, want the pre-existing Account 1", dup.ExistingConnID)
	}
	if dup.ExistingName != "Account 1" {
		t.Errorf("ExistingName = %q, want Account 1", dup.ExistingName)
	}
}

// TestFindDuplicateConnection_DifferentAccountsAreAllowed is the counter-test:
// two genuinely different accounts must both be addable.
func TestFindDuplicateConnection_DifferentAccountsAreAllowed(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	insertConn(t, repo, "conn-a", "codebuddy-intl", "A", "",
		map[string]any{"accessToken": makeJWT(t, map[string]any{"sub": "sub-aaa"})})

	candidate := IdentityForProvider("codebuddy-intl", makeJWT(t, map[string]any{"sub": "sub-bbb"}), "")
	dup, err := h.FindDuplicateConnection("codebuddy-intl", candidate)
	if err != nil {
		t.Fatalf("FindDuplicateConnection: %v", err)
	}
	if dup != nil {
		t.Errorf("different accounts must not collide, got %+v", dup)
	}
}

// TestFindDuplicateConnection_ScopedToProvider makes sure a shared subject
// across two providers is not treated as a duplicate within one of them.
func TestFindDuplicateConnection_ScopedToProvider(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	sub := "same-subject"
	insertConn(t, repo, "cb", "codebuddy-intl", "CB", "",
		map[string]any{"accessToken": makeJWT(t, map[string]any{"sub": sub})})

	candidate := IdentityForProvider("codebuddy-intl", makeJWT(t, map[string]any{"sub": sub}), "")
	dup, err := h.FindDuplicateConnection("cline", candidate)
	if err != nil {
		t.Fatalf("FindDuplicateConnection: %v", err)
	}
	if dup != nil {
		t.Error("a codebuddy connection must not match a cline lookup")
	}
}

// TestFindDuplicateConnection_KindMustMatch stops a jwt_sub candidate from
// matching an email-only existing row, which could otherwise suppress a
// legitimate add.
func TestFindDuplicateConnection_KindMustMatch(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	insertConn(t, repo, "conn-email", "antigravity", "Gmail", "user@example.com",
		map[string]any{"providerSpecificData": map[string]any{"email": "user@example.com"}})

	// A JWT-based candidate for a provider that resolves jwt_sub.
	candidate := DuplicateIdentity{Kind: "jwt_sub", Value: "user@example.com"}
	dup, err := h.FindDuplicateConnection("antigravity", candidate)
	if err != nil {
		t.Fatalf("FindDuplicateConnection: %v", err)
	}
	if dup != nil {
		t.Error("kinds differ (jwt_sub vs email); must not match")
	}
}

func TestFindDuplicateConnection_EmptyCandidateSkips(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	insertConn(t, repo, "conn", "kiro", "Account 1", "", map[string]any{"apiKey": "sk-1"})

	dup, err := h.FindDuplicateConnection("kiro", DuplicateIdentity{})
	if err != nil {
		t.Fatalf("FindDuplicateConnection: %v", err)
	}
	if dup != nil {
		t.Error("an empty candidate must never match")
	}
}

// TestFindDuplicateConnection_GitHubByUserID covers the github shape, where the
// identity lives in providerSpecificData and the login may have been renamed.
func TestFindDuplicateConnection_GitHubByUserID(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	insertConn(t, repo, "gh-1", "github", "arewedaks", "",
		map[string]any{"providerSpecificData": map[string]any{
			"githubLogin": "arewedaks", "githubUserId": float64(273372247),
		}})

	dup, err := h.FindDuplicateConnection("github", IdentityForGitHub(273372247, "arewedaks-renamed", ""))
	if err != nil {
		t.Fatalf("FindDuplicateConnection: %v", err)
	}
	if dup == nil {
		t.Fatal("same numeric user id should be detected as a duplicate despite the rename")
	}
	if dup.ExistingConnID != "gh-1" {
		t.Errorf("existing = %q, want gh-1", dup.ExistingConnID)
	}
}

// TestFindDuplicateConnection_GitHubDistinctUsersAllowed verifies the two real
// GitHub connections in the DB (different ids) are not duplicates.
func TestFindDuplicateConnection_GitHubDistinctUsersAllowed(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	insertConn(t, repo, "gh-mohpizi", "github", "mohpizi", "",
		map[string]any{"providerSpecificData": map[string]any{"githubLogin": "mohpizi", "githubUserId": float64(82371418)}})

	dup, err := h.FindDuplicateConnection("github", IdentityForGitHub(273372247, "arewedaks", ""))
	if err != nil {
		t.Fatalf("FindDuplicateConnection: %v", err)
	}
	if dup != nil {
		t.Errorf("arewedaks and mohpizi are different accounts, got %+v", dup)
	}
}

// ensure the sql import is used even if the file evolves.
var _ = sql.ErrNoRows

// TestReplaceConnectionTokens_PreservesIdentityAndClearsLocks covers the
// replace=true path end-to-end: the existing row must keep its ID/priority while
// taking the new credential, and its stale model locks must be cleared so a
// now-healthy account is not left cooling down.
func TestReplaceConnectionTokens_PreservesIdentityAndClearsLocks(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	sub := "88e091b3-a29f-4ed5-992c-187ac660e262"
	oldToken := makeJWT(t, map[string]any{"sub": sub, "iat": 1789059859})
	newToken := makeJWT(t, map[string]any{"sub": sub, "iat": 1789299451})

	insertConn(t, repo, "acct-1", "codebuddy-intl", "Account 1", "", map[string]any{
		"accessToken":               oldToken,
		"refreshToken":              "old-refresh",
		"modelLock_deepseek-v4-pro": "2030-01-01T00:00:00Z",
		"backoffLevel":              float64(3),
	})
	// Give it a priority so we can prove it survives.
	if _, err := repo.RawDB().Exec(`UPDATE providerConnections SET priority = 7 WHERE id = 'acct-1'`); err != nil {
		t.Fatalf("set priority: %v", err)
	}

	dup, err := h.FindDuplicateConnection("codebuddy-intl", IdentityForProvider("codebuddy-intl", newToken, ""))
	if err != nil || dup == nil {
		t.Fatalf("expected duplicate, got %v / %v", dup, err)
	}

	if err := h.replaceConnectionTokens(dup.ExistingConnID, "Renamed", map[string]any{
		"accessToken":  newToken,
		"refreshToken": "new-refresh",
	}); err != nil {
		t.Fatalf("replaceConnectionTokens: %v", err)
	}

	// Exactly one connection must remain: replace must not insert a second row.
	var count int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM providerConnections WHERE provider = 'codebuddy-intl'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("connection count = %d, want 1 (replace must not duplicate)", count)
	}

	conns, err := repo.GetProviderConnections("codebuddy-intl", false)
	if err != nil || len(conns) != 1 {
		t.Fatalf("GetProviderConnections: %v (n=%d)", err, len(conns))
	}
	c := conns[0]
	if c.ID != "acct-1" {
		t.Errorf("id = %q, want acct-1 (identity must be preserved)", c.ID)
	}
	if c.Priority == nil || *c.Priority != 7 {
		t.Errorf("priority = %v, want 7 (must be preserved)", c.Priority)
	}
	if c.Name == nil || *c.Name != "Renamed" {
		t.Errorf("name = %v, want the updated name", c.Name)
	}

	// The stale lock must be gone, otherwise the refreshed account stays blocked.
	for _, model := range []string{"deepseek-v4-pro"} {
		locked, lerr := repo.IsConnectionModelLocked("acct-1", model)
		if lerr != nil {
			t.Fatalf("IsConnectionModelLocked: %v", lerr)
		}
		if locked {
			t.Errorf("model %s still locked after credential replace", model)
		}
	}
	if lvl := repo.GetConnectionBackoffLevel("acct-1"); lvl != 0 {
		t.Errorf("backoffLevel = %d, want 0 after replace", lvl)
	}
}

// TestReplaceConnectionTokens_DoesNotTouchOtherConnections guards that clearing
// locks is scoped to the replaced connection.
func TestReplaceConnectionTokens_DoesNotTouchOtherConnections(t *testing.T) {
	repo := newDuplicateTestRepo(t)
	h := &OAuthHandler{Repo: repo}

	insertConn(t, repo, "other", "codebuddy-intl", "Other", "", map[string]any{
		"accessToken":               makeJWT(t, map[string]any{"sub": "other-sub"}),
		"modelLock_deepseek-v4-pro": "2030-01-01T00:00:00Z",
	})
	insertConn(t, repo, "target", "codebuddy-intl", "Target", "", map[string]any{
		"accessToken":               makeJWT(t, map[string]any{"sub": "target-sub"}),
		"modelLock_deepseek-v4-pro": "2030-01-01T00:00:00Z",
	})

	if err := h.replaceConnectionTokens("target", "Target", map[string]any{"accessToken": "x"}); err != nil {
		t.Fatalf("replaceConnectionTokens: %v", err)
	}

	locked, err := repo.IsConnectionModelLocked("other", "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("IsConnectionModelLocked: %v", err)
	}
	if !locked {
		t.Error("an unrelated connection's lock must not be cleared")
	}
}
