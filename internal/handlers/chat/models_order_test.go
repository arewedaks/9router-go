package chat

import (
	"sort"
	"testing"

	"9router/proxy/internal/db"
)

// The model list must be stable and predictable: combos first, then everything
// else alphabetically.
//
// It used to follow the iteration order of a Go map, which the runtime
// randomises on purpose. Three consecutive calls returned three different
// orderings, so a client's model dropdown reshuffled on every refresh and two
// captures of the list could never be compared. The OpenAI contract does not
// promise an order, but an unstable one makes the list impossible to diff and
// makes any ordering assertion flaky.
func TestModelsListIsStableAcrossCalls(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	for _, p := range []string{"prov-b", "prov-a", "prov-c"} {
		if _, err := database.Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
			 VALUES (?, ?, 'apikey', ?, 1, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			p+"-conn", p, p,
		); err != nil {
			t.Fatalf("seed connection %s: %v", p, err)
		}
		if err := repo.AddCachedModelWithName(p, "model-"+p, "llm", "imported", ""); err != nil {
			t.Fatalf("seed model %s: %v", p, err)
		}
	}

	h := &ChatHandler{Repo: repo}

	first := h.buildModelsList()
	if len(first) < 3 {
		t.Fatalf("expected at least 3 models, got %d", len(first))
	}
	firstIDs := make([]string, 0, len(first))
	for _, m := range first {
		firstIDs = append(firstIDs, m.ID)
	}

	// Several calls in one process are enough to expose map-order dependence:
	// the runtime re-randomises the start offset each range.
	for i := 0; i < 8; i++ {
		next := h.buildModelsList()
		nextIDs := make([]string, 0, len(next))
		for _, m := range next {
			nextIDs = append(nextIDs, m.ID)
		}
		if len(nextIDs) != len(firstIDs) {
			t.Fatalf("call %d returned %d models, want %d", i+2, len(nextIDs), len(firstIDs))
		}
		for j := range firstIDs {
			if nextIDs[j] != firstIDs[j] {
				t.Fatalf("order changed between calls:\n  call 1: %v\n  call %d: %v",
					firstIDs, i+2, nextIDs)
			}
		}
	}
}

// Combos come before provider models, and each group is alphabetical. Combos
// are the operator's own named routes, so they are the entries they look for by
// name; provider models are bulk.
func TestModelsListPutsCombosFirstThenAlphabetical(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	// Two providers with models that would sort before the combo names.
	for _, p := range []string{"zzz", "aaa"} {
		if _, err := database.Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
			 VALUES (?, ?, 'apikey', ?, 1, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			p+"-conn", p, p,
		); err != nil {
			t.Fatalf("seed connection %s: %v", p, err)
		}
		if err := repo.AddCachedModelWithName(p, "model", "llm", "imported", ""); err != nil {
			t.Fatalf("seed model %s: %v", p, err)
		}
	}

	// Two combos, deliberately added in non-alphabetical order.
	for _, name := range []string{"zeta-combo", "alpha-combo"} {
		if _, err := database.Exec(
			`INSERT INTO combos (id, name, kind, models, strategy, createdAt, updatedAt)
			 VALUES (?, ?, 'llm', '[]', 'fallback', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			"combo-"+name, name,
		); err != nil {
			t.Fatalf("seed combo %s: %v", name, err)
		}
	}

	h := &ChatHandler{Repo: repo}
	list := h.buildModelsList()

	ids := make([]string, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.ID)
	}

	posOf := map[string]int{}
	for i, id := range ids {
		posOf[id] = i
	}
	if posOf["alpha-combo"] > posOf["zeta-combo"] {
		t.Errorf("combos are not alphabetical: %v", ids)
	}
	for _, name := range []string{"alpha-combo", "zeta-combo"} {
		for _, providerModel := range []string{"aaa/model", "zzz/model"} {
			if posOf[name] > posOf[providerModel] {
				t.Errorf("combo %s is not before provider model %s: %v", name, providerModel, ids)
			}
		}
	}
}

// isComboName reports whether id was contributed by the Combos source in the
// shared test fixture, which seeds "my-combo".
func isComboName(id string) bool {
	return id == "my-combo" || id == "alpha-combo" || id == "zeta-combo" || id == "zz-ordered-combo"
}

// The ordering must not depend on insertion order of the connections.
func TestModelsListAlphabeticalRegardlessOfInsertionOrder(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	// Insert in reverse alphabetical order.
	for _, p := range []string{"ccc", "bbb", "aaa"} {
		if _, err := database.Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
			 VALUES (?, ?, 'apikey', ?, 1, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			p+"-conn", p, p,
		); err != nil {
			t.Fatalf("seed connection %s: %v", p, err)
		}
		if err := repo.AddCachedModelWithName(p, "model", "llm", "imported", ""); err != nil {
			t.Fatalf("seed model %s: %v", p, err)
		}
	}

	h := &ChatHandler{Repo: repo}
	ids := []string{}
	for _, m := range h.buildModelsList() {
		if isComboName(m.ID) {
			continue
		}
		ids = append(ids, m.ID)
	}

	if !sort.StringsAreSorted(ids) {
		t.Errorf("expected alphabetical order, got %v", ids)
	}
}
func TestComboOrderingDoesNotAlterPayload(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	h := &ChatHandler{Repo: repo}
	list := h.buildModelsList()
	if len(list) == 0 || !isComboName(list[0].ID) {
		t.Fatalf("first entry is not a combo: %+v", list)
	}
	if list[0].OwnedBy != "system" {
		t.Errorf("combo OwnedBy changed to %q; the sort must not alter the payload", list[0].OwnedBy)
	}
	if list[0].Object != "model" {
		t.Errorf("combo Object changed to %q", list[0].Object)
	}
}
