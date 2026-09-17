package db

import (
	"path/filepath"
	"testing"

)

func setupHiddenRepo(t *testing.T) *Repo {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "hidden.sqlite")
	database, err := OpenDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := EnsureSchema(database); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return NewRepo(database)
}

func TestHideModelRoundTrip(t *testing.T) {
	repo := setupHiddenRepo(t)

	if err := repo.HideModel([]string{"deepseek", "ds"}, "deepseek-chat"); err != nil {
		t.Fatalf("HideModel: %v", err)
	}

	all, err := repo.GetAllHiddenModels()
	if err != nil {
		t.Fatalf("GetAllHiddenModels: %v", err)
	}
	for _, want := range []string{"deepseek|deepseek-chat", "ds|deepseek-chat"} {
		if !all[want] {
			t.Errorf("expected marker %q, got %v", want, all)
		}
	}

	// Idempotent: hiding twice must not error or duplicate.
	if err := repo.HideModel([]string{"deepseek"}, "deepseek-chat"); err != nil {
		t.Fatalf("HideModel twice: %v", err)
	}

	// Filtered lookup restricts to the requested provider keys.
	only, err := repo.GetHiddenModels([]string{"ds"})
	if err != nil {
		t.Fatalf("GetHiddenModels: %v", err)
	}
	if !only["ds|deepseek-chat"] {
		t.Errorf("filtered lookup missed ds key: %v", only)
	}
	if only["deepseek|deepseek-chat"] {
		t.Errorf("filtered lookup leaked deepseek key: %v", only)
	}

	// Unhide removes exactly the given keys.
	if err := repo.UnhideModel([]string{"deepseek", "ds"}, "deepseek-chat"); err != nil {
		t.Fatalf("UnhideModel: %v", err)
	}
	after, err := repo.GetAllHiddenModels()
	if err != nil {
		t.Fatalf("GetAllHiddenModels after unhide: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("expected empty hidden set, got %v", after)
	}
}

func TestHideModelRejectsEmptyID(t *testing.T) {
	repo := setupHiddenRepo(t)
	if err := repo.HideModel([]string{"deepseek"}, "  "); err == nil {
		t.Errorf("HideModel with blank id must fail")
	}
	if err := repo.UnhideModel([]string{"deepseek"}, ""); err == nil {
		t.Errorf("UnhideModel with blank id must fail")
	}
}

// TestHiddenModelSurvivesReopen proves the marker is persisted to the DB, not
// kept in memory, so a removal survives a restart.
func TestHiddenModelSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.sqlite")

	database, err := OpenDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := EnsureSchema(database); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	repo := NewRepo(database)
	if err := repo.HideModel([]string{"groq", "gq"}, "llama-3.3-70b-versatile"); err != nil {
		t.Fatalf("HideModel: %v", err)
	}
	database.Close()

	reopened, err := OpenDatabase(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer reopened.Close()
	all, err := NewRepo(reopened).GetAllHiddenModels()
	if err != nil {
		t.Fatalf("GetAllHiddenModels: %v", err)
	}
	if !all["gq|llama-3.3-70b-versatile"] {
		t.Errorf("hidden marker did not survive reopen: %v", all)
	}
}
