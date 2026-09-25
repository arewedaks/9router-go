package keyless

import (
	"context"
	"path/filepath"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func newRepo(t *testing.T) *db.Repo {
	t.Helper()
	database, err := db.OpenDatabase(filepath.Join(t.TempDir(), "keyless.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := db.EnsureSchema(database); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return db.NewRepo(database)
}

// The bug: on a fresh database a keyless provider owns no connection row, so
// the model cache is empty and OpenCode Free advertised zero models even though
// it needs no credential at all.
func TestSeedFillsKeylessProviderOnFreshInstall(t *testing.T) {
	repo := newRepo(t)

	if got, _ := repo.ListCachedModels("oc", "opencode"); len(got) != 0 {
		t.Fatalf("precondition: fresh cache must be empty, got %d", len(got))
	}

	if err := SeedIfEmpty(context.Background(), repo); err != nil {
		t.Fatalf("seed: %v", err)
	}

	models, err := repo.ListCachedModels("oc", "opencode")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected the keyless catalogue to be seeded, got 0 models")
	}

	want := providers.KeylessModelIDs("opencode")
	if len(models) != len(want) {
		t.Fatalf("seeded %d models, want %d", len(models), len(want))
	}
	have := make(map[string]bool, len(models))
	for _, m := range models {
		have[m.ModelID] = true
	}
	for _, id := range want {
		if !have[id] {
			t.Errorf("catalogued keyless model %q was not seeded", id)
		}
	}
}

// Re-running must not duplicate rows, and a provider the operator has
// deliberately pruned must not be topped back up.
func TestSeedIsIdempotentAndDoesNotRefillPrunedProviders(t *testing.T) {
	repo := newRepo(t)
	if err := SeedIfEmpty(context.Background(), repo); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	before, _ := repo.ListCachedModels("oc", "opencode")

	if err := SeedIfEmpty(context.Background(), repo); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	after, _ := repo.ListCachedModels("oc", "opencode")
	if len(before) != len(after) {
		t.Fatalf("re-seeding changed the row count: %d -> %d", len(before), len(after))
	}

	// Simulate the operator removing everything: the cache stays empty, so the
	// "only seed when empty" guard would refill it. They must be able to keep
	// it empty by hiding the models, which is what RemoveModel records.
	for _, m := range after {
		if err := repo.HideModel([]string{"oc", "opencode"}, m.ModelID); err != nil {
			t.Fatalf("hide %s: %v", m.ModelID, err)
		}
	}
	if _, err := repo.DB().Exec("DELETE FROM cachedProviderModels"); err != nil {
		t.Fatalf("clear cache: %v", err)
	}
	if err := SeedIfEmpty(context.Background(), repo); err != nil {
		t.Fatalf("third seed: %v", err)
	}
	refilled, _ := repo.ListCachedModels("oc", "opencode")
	if len(refilled) != 0 {
		t.Fatalf("hidden models were resurrected: %d rows", len(refilled))
	}
}

func TestSeedIgnoresNilRepo(t *testing.T) {
	if err := SeedIfEmpty(context.Background(), nil); err != nil {
		t.Fatalf("nil repo must be a no-op, got: %v", err)
	}
}
