package chat

import (
	"testing"

	"9router/proxy/internal/db"
)

// A custom model whose provider no longer exists must not be advertised.
//
// The orphan guard already existed, but aliveAliases seeded itself with the
// whole registry — every key in providers.KnownProviders and
// providers.ProviderAliasMap — so a provider name that merely appears in the
// registry counted as reachable. Deleting a connection therefore left its
// models listed: a live install advertised 164 ids from providers with no
// connection row and no node, and those ids answered 404 when called.
//
// Reachability means a connection or a node exists. The registry is a catalogue
// of names, not evidence that this install can reach any of them.
func TestOrphanedProviderModelsAreNotAdvertised(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	// A registry alias that has no connection row in this install. "gh" is an
	// alias for github and appears in ProviderAliasMap, which is exactly why the
	// old seeding made it look alive.
	if err := repo.AddCachedModelWithName("gh", "gpt-5.2", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}

	ids := decodeModelIDs(t, h)

	if _, ok := ids["gh/gpt-5.2"]; ok {
		t.Errorf("model from a provider with no connection was advertised: %v", keysOf(ids))
	}
}

// The same rule for a provider that keeps its own key in KnownProviders rather
// than an alias — "xkiro" is such a name in the live data.
func TestOrphanedKnownProviderModelsAreNotAdvertised(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	if err := repo.AddCachedModelWithName("xkiro", "some-model", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}

	ids := decodeModelIDs(t, h)

	if _, ok := ids["xkiro/some-model"]; ok {
		t.Errorf("model from a provider with no connection was advertised: %v", keysOf(ids))
	}
}

// The other half: a provider that does have a connection must still list its
// imported models. Without this the fix could pass by listing nothing.
func TestConnectedProviderModelsSurvive(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('gh-conn', 'github', 'oauth', 'GitHub', 0, 1, '{}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if err := repo.AddCachedModelWithName("gh", "gpt-5.2", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}

	ids := decodeModelIDs(t, h)

	if _, ok := ids["gh/gpt-5.2"]; !ok {
		t.Errorf("connected provider model missing: %v", keysOf(ids))
	}
}
