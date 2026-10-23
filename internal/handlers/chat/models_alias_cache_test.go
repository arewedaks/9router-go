package chat

import (
	"testing"

	"9router/proxy/internal/db"
)

// Import writes cachedProviderModels under whichever provider key the caller
// used, and /v1/models renders under GetProviderAlias. When those two disagree
// — Perch was stored as "perchai" while the primary alias resolves to "pch" —
// the list silently dropped every imported model. `cachedModelIDs` must fan out
// over every alias so the two can never drift apart again.
func TestCachedModelIDsFindsEveryAlias(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	for _, key := range []string{"perch", "pch", "perchai"} {
		if err := repo.AddCachedModel(key, "model-"+key, "llm", "perch"); err != nil {
			t.Fatalf("AddCachedModel(%s): %v", key, err)
		}
	}

	h := &ChatHandler{Repo: repo}
	// Render alias "pch" is what GetProviderAlias returns; the rows live under
	// all three keys and every one must be reachable.
	ids := h.cachedModelIDs("perch", "pch")
	if len(ids) != 3 {
		t.Fatalf("cachedModelIDs = %v, want 3 entries (one per alias)", ids)
	}
}
