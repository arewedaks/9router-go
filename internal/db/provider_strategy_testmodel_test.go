package db

import (
	"path/filepath"
	"testing"
)

// A provider whose cheap models rotate needs a place to pin the probe model.
// The value lives in the shared providerStrategies blob, so it must survive a
// round-trip and must not be pruned as an "empty" strategy.
func TestProviderStrategyKeepsPinnedTestModel(t *testing.T) {
	database, err := OpenDatabase(filepath.Join(t.TempDir(), "testmodel.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := EnsureSchema(database); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	repo := NewRepo(database)

	if err := repo.saveSettings(&SettingsData{
		ProviderStrategies: map[string]ProviderStrategy{
			"openrouter": {TestModel: "z-ai/glm-4.5-air:free"},
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	got, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	strat, ok := got.ProviderStrategies["openrouter"]
	if !ok {
		t.Fatal("openrouter strategy dropped on read")
	}
	if strat.TestModel != "z-ai/glm-4.5-air:free" {
		t.Fatalf("TestModel = %q after reload, want the pinned free model", strat.TestModel)
	}
	if strat.IsEmpty() {
		t.Fatal("a strategy carrying only a pinned test model is configuration, not empty")
	}
	if !(ProviderStrategy{}).IsEmpty() {
		t.Fatal("an untouched strategy must still count as empty so it gets pruned")
	}
}
