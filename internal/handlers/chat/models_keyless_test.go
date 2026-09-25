package chat

import (
	"context"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/keyless"
	"9router/proxy/internal/providers"
)

// A keyless provider owns no connection row, so it can never appear in the
// activeConnections loop that drives /v1/models. That left OpenCode Free
// invisible in the model list even though the very same id answered a chat
// request with HTTP 200 — routing resolves the alias independently of
// connections. Its models must be advertised from the imported cache, falling
// back to the registry.
func TestHandleModelsListsKeylessProviderModels(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	// Some ordinary connection must exist, or the static-registry fallback
	// branch runs instead and this test would not exercise the keyless path.
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('cb-conn', 'codebuddy', 'apikey', 'CB', 0, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	// The importer stores OpenCode's catalogue under its "oc" alias.
	if err := repo.AddCachedModelWithName("oc", "muse-spark-1.2-contributor-free", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}

	ids := decodeModelIDs(t, h)
	if _, ok := ids["oc/muse-spark-1.2-contributor-free"]; !ok {
		t.Errorf("keyless OpenCode model is not advertised: %v", keysOf(ids))
	}
}

// A keyless provider advertises only what is in its imported cache — the
// registry is a hardcoded table that knows nothing about the account, so it is
// never the fallback (it listed 794 models across 12 connections, most of which
// answered model_not_supported).
//
// That cache must not be empty on a fresh install, though: the startup seeder
// fills it from the shipped keyless catalogue, because OpenCode Free needs no
// credential and so had nothing for the operator to configure before the
// provider worked. What must NOT happen is the registry leaking back in.
func TestHandleModelsKeylessProviderAdvertisesSeededCatalogueNotRegistry(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('cb-conn', 'codebuddy', 'apikey', 'CB', 0, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	// Nothing imported yet — this is the fresh-install state. The fixture DB
	// carries unrelated combos, so only the oc/* namespace is meaningful here.
	for id := range decodeModelIDs(t, h) {
		if strings.HasPrefix(id, "oc/") {
			t.Fatalf("precondition: an unseeded keyless cache must advertise no oc/* model, got %q", id)
		}
	}

	if err := keyless.SeedIfEmpty(context.Background(), repo); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ids := decodeModelIDs(t, h)
	for _, m := range providers.KeylessModelIDs("oc") {
		if _, ok := ids["oc/"+m]; !ok {
			t.Errorf("seeded keyless model oc/%s is not advertised", m)
		}
	}
	// The registry fallback must stay dead: every advertised oc/* model has to
	// be one the seeder actually wrote.
	seeded := make(map[string]bool)
	for _, m := range providers.KeylessModelIDs("oc") {
		seeded["oc/"+m] = true
	}
	for id := range ids {
		if strings.HasPrefix(id, "oc/") && !seeded[id] {
			t.Errorf("advertised unseeded model %q — the registry fallback is back", id)
		}
	}
}

// The keyless branch must not promote providers that cannot serve a chat
// request. The local TTS and search servers are also AuthType "none", and a card
// — or a model id — for them is noise an operator cannot act on.
func TestHandleModelsExcludesNonChatKeylessProviders(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	ids := decodeModelIDs(t, h)
	for _, prov := range []string{"coqui", "sdwebui", "comfyui", "searxng", "tortoise", "edge-tts", "google-tts", "local-device"} {
		for id := range ids {
			if len(id) > len(prov)+1 && id[:len(prov)+1] == prov+"/" {
				t.Errorf("non-chat keyless provider %q leaked into /v1/models as %q", prov, id)
			}
		}
	}
}

// A keyless provider that later gains a real connection must be listed through
// that connection, not twice. The dedupe guard is what prevents a duplicate.
func TestHandleModelsDoesNotDuplicateKeylessProviderWithConnection(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('oc-conn', 'opencode', 'apikey', 'OC', 0, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if err := repo.AddCachedModelWithName("oc", "union-alpha", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}

	ids := decodeModelIDs(t, h)
	if _, ok := ids["oc/union-alpha"]; !ok {
		t.Errorf("connected OpenCode lost its model: %v", keysOf(ids))
	}
}

// A brand-new install has no connections at all. The keyless branch used to be
// gated on len(activeConnections) > 0, so OpenCode Free advertised nothing in
// /v1/models on a fresh database even though the startup seeder had filled its
// catalogue — and the same model answered chat requests fine.
func TestHandleModelsAdvertisesKeylessWithZeroConnections(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	if err := keyless.SeedIfEmpty(context.Background(), repo); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ids := decodeModelIDs(t, h)
	found := 0
	for id := range ids {
		if strings.HasPrefix(id, "oc/") {
			found++
		}
	}
	if found == 0 {
		t.Fatalf("a zero-connection install advertised no keyless models: %v", keysOf(ids))
	}
}
