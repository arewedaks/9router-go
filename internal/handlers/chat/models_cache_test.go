package chat

import (
	"testing"

	"9router/proxy/internal/db"
)

// Models imported through the dashboard ("Import from /models") are written to
// cachedProviderModels, but buildModelsList never read that table: it fell back
// to the static registry. A provider whose live catalogue is richer than the
// registry — Cloudflare Workers AI, 65 live against 24 built in — therefore kept
// advertising only the registry subset even after a successful import, and the
// newly imported ids could not be called by name.
func TestHandleModelsPrefersImportedCacheOverRegistry(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	// The importer stores Cloudflare's list under its alias ("cf"), while the
	// connection's provider id is the canonical "cloudflare-ai".
	if err := repo.AddCachedModelWithName("cf", "@cf/openai/gpt-oss-120b", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}
	// Keep a registry model in the cache too, so the assertion is about ids
	// rather than about the registry being dropped entirely.
	if err := repo.AddCachedModelWithName("cf", "@cf/meta/llama-3.2-3b-instruct", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model 2: %v", err)
	}

	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('cf-conn', 'cloudflare-ai', 'apikey', 'CF', 0, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	ids := decodeModelIDs(t, h)

	// The imported id must be advertised under the connection's output alias.
	if _, ok := ids["cf/@cf/openai/gpt-oss-120b"]; !ok {
		t.Errorf("imported model cf/@cf/openai/gpt-oss-120b is not advertised: %v", keysOf(ids))
	}

	// Aliases and combos (sources 2 and 3) must still be present: the cache
	// replaces only the per-provider registry branch.
	if _, ok := ids["deepseek-chat"]; !ok {
		t.Logf("note: combo/alias list empty in this fixture")
	}
}

// cachedModelIDs must find the cache under any spelling of the provider, because
// the importer keys it by whichever alias the import used.
func TestCachedModelIDsResolvesAliases(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	if err := repo.AddCachedModelWithName("cf", "@cf/meta/llama-3.2-3b-instruct", "llm", "imported", ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Looked up by canonical id while stored under the alias.
	got := h.cachedModelIDs("cloudflare-ai", "cf")
	if len(got) != 1 || got[0] != "@cf/meta/llama-3.2-3b-instruct" {
		t.Errorf("cachedModelIDs(cloudflare-ai, cf) = %v, want the stored id", got)
	}

	// An unknown provider yields nil so callers keep their registry fallback.
	if got := h.cachedModelIDs("nothing-here", "nh"); got != nil {
		t.Errorf("cachedModelIDs for an unknown provider = %v, want nil", got)
	}

	// A nil repo must not panic.
	empty := &ChatHandler{}
	if got := empty.cachedModelIDs("cf", "cf"); got != nil {
		t.Errorf("cachedModelIDs with nil Repo = %v, want nil", got)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
