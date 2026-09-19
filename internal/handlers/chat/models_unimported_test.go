package chat

import (
	"testing"

	"9router/proxy/internal/db"
)

// A provider connection that has never had its catalogue imported must not
// advertise the static registry's model list.
//
// The registry is a hardcoded fallback table inside the binary (nvidia alone
// carries 11 ids, github 19). buildModelsList fell back to it whenever
// cachedProviderModels had no rows for a provider, so every configured account
// appeared to own models nobody had imported — a live install reported 794
// models across 12 providers. Calling most of them failed, because the ids came
// from the registry rather than from the account: github/gpt-5.2 answered
// model_not_supported.
//
// Listing a model is a promise that it can be called. A provider with no import
// must therefore contribute nothing until the operator imports from /models.
func TestUnimportedProviderDoesNotAdvertiseRegistryModels(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	// GitHub is connected but its catalogue was never imported.
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('gh-conn', 'github', 'oauth', 'GitHub', 0, 1, '{}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	ids := decodeModelIDs(t, h)

	// gpt-5.2 is in the static registry for "github" but was never imported.
	if _, ok := ids["gh/gpt-5.2"]; ok {
		t.Errorf("unimported provider advertised a registry model: gh/gpt-5.2 is listed")
	}
	// The fixture seeds a combo and an alias; only provider-prefixed ids are the
	// subject here.
	for id := range ids {
		if len(id) > 3 && id[:3] == "gh/" {
			t.Errorf("unimported provider advertised %s", id)
		}
	}
}

// A provider whose catalogue *was* imported must advertise exactly those ids.
// This is the other half of the rule: dropping the registry fallback must not
// also drop the imported models.
func TestImportedProviderAdvertisesOnlyImportedModels(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('gh-conn', 'github', 'oauth', 'GitHub', 0, 1, '{}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if err := repo.AddCachedModelWithName("github", "gpt-4o-mini", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}

	ids := decodeModelIDs(t, h)

	if _, ok := ids["gh/gpt-4o-mini"]; !ok {
		t.Errorf("imported model gh/gpt-4o-mini missing: %v", keysOf(ids))
	}
	if _, ok := ids["gh/gpt-5.2"]; ok {
		t.Errorf("registry model gh/gpt-5.2 leaked in alongside the import")
	}
}

// One provider must produce one prefix. The connection's configured prefix (or
// its canonical alias) wins; the registry spelling must not add a second set.
// A live install listed both nvidia/* and nv/* for the same account, so the
// same model answered under two names and the count roughly doubled.
func TestProviderAdvertisesUnderSinglePrefix(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-conn', 'nvidia', 'apikey', 'NVIDIA', 0, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if err := repo.AddCachedModelWithName("nv", "z-ai/glm-5.2", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}

	ids := decodeModelIDs(t, h)

	if _, ok := ids["nv/z-ai/glm-5.2"]; !ok {
		t.Errorf("imported model nv/z-ai/glm-5.2 missing: %v", keysOf(ids))
	}
	for id := range ids {
		if len(id) > 7 && id[:7] == "nvidia/" {
			t.Errorf("model advertised under a second prefix: %s", id)
		}
	}
}
