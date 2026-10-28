package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Limits published by the upstream listing must survive an import that carries
// no numbers: a re-sync of /v1/models (which normally omits context_length)
// must not wipe a measured value the operator already has.
func TestCachedModelLimitsSurviveReimport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sqlite")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()
	if err := EnsureSchema(database); err != nil {
		t.Fatalf("schema: %v", err)
	}
	repo := NewRepo(database)

	if err := repo.AddCachedModelWithLimits("lr", "deepseek-4.1-flash", "llm", "imported", "DeepSeek 4.1 Flash", 1048576, 393216); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Second pass: a listing with no limits and no display name (the common
	// case for an OpenAI-compatible endpoint).
	if err := repo.AddCachedModelWithLimits("lr", "deepseek-4.1-flash", "llm", "imported", "", 0, 0); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	models, err := repo.ListCachedModels("lr")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d cached models, want 1", len(models))
	}
	got := models[0]
	if got.ContextWindow != 1048576 || got.MaxOutput != 393216 {
		t.Fatalf("limits = %d/%d, want 1048576/393216", got.ContextWindow, got.MaxOutput)
	}
	if got.DisplayName != "DeepSeek 4.1 Flash" {
		t.Fatalf("display name = %q, want the imported label preserved", got.DisplayName)
	}
}

// An explicit 0 is "unknown", not "zero tokens": a provider that publishes
// nothing must leave the columns empty so the static tables keep deciding.
func TestCachedModelWithoutLimitsStaysEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c2.sqlite")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()
	if err := EnsureSchema(database); err != nil {
		t.Fatalf("schema: %v", err)
	}
	repo := NewRepo(database)

	if err := repo.AddCachedModelWithLimits("oc", "mystery", "llm", "imported", "", 0, 0); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	models, err := repo.ListCachedModels("oc")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 0 || models[0].MaxOutput != 0 {
		t.Fatalf("limits = %+v, want zeroed", models)
	}
}
