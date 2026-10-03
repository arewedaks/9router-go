package providers

// Seeding hooks used by tests in other packages that need a pinned catalogue.
//
// They live in a normal file rather than a _test.go one because a test file's
// helpers are not visible outside its own package, and the combo-limits test in
// internal/handlers/chat needs to pin limits without downloading models.dev.
// Names say "ForTest" so no production caller reaches for them.

// SeedCatalogForTest installs a catalogue keyed by provider, then by model.
func SeedCatalogForTest(tiers map[string]map[string]SyncedModelLimits) {
	catalogMu.Lock()
	globalCatalog = &SyncedCatalog{
		SyncedAt:  "test",
		Models:    map[string]SyncedModelModalities{},
		Providers: tiers,
	}
	catalogMu.Unlock()
	InvalidateCapabilitiesCache()
}

// ClearCatalogForTest removes the seeded catalogue.
func ClearCatalogForTest() {
	catalogMu.Lock()
	globalCatalog = nil
	catalogMu.Unlock()
	InvalidateCapabilitiesCache()
}
