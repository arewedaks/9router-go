package chat

import (
	"testing"

	"9router/proxy/internal/db"
)

// TestHandleModelsHidesRemovedModel pins the fix for the "Remove model" button:
// a model removed from a provider page must disappear from /v1/models. Before
// the fix, RemoveModel only deleted a cachedProviderModels row that the engine
// never reads, so the model kept being advertised.
//
// The baseline is an imported model rather than a registry one: the registry
// fallback is gone (see TestUnimportedProviderDoesNotAdvertiseRegistryModels),
// so an imported id is what the operator can actually see and remove.
func TestHandleModelsHidesRemovedModel(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('ds-conn', 'deepseek', 'apikey', 'DS', 0, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if err := repo.AddCachedModelWithName("ds", "deepseek-chat", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}
	// A sibling, so the "only the removed model disappears" assertion below
	// has something to compare against.
	if err := repo.AddCachedModelWithName("ds", "deepseek-reasoner", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model 2: %v", err)
	}

	before := decodeModelIDs(t, h)
	if _, ok := before["ds/deepseek-chat"]; !ok {
		t.Fatalf("baseline: ds/deepseek-chat missing before removal: %v", before)
	}

	// Simulate the operator pressing "Remove model" for deepseek-chat. The UI
	// posts the provider id plus the bare model id.
	if err := repo.HideModel([]string{"deepseek", "ds"}, "deepseek-chat"); err != nil {
		t.Fatalf("HideModel: %v", err)
	}

	after := decodeModelIDs(t, h)
	if _, ok := after["ds/deepseek-chat"]; ok {
		t.Errorf("removed model ds/deepseek-chat is still advertised: %v", after)
	}
	// A sibling model on the same provider must remain.
	if _, ok := after["ds/deepseek-reasoner"]; !ok {
		t.Errorf("unrelated model ds/deepseek-reasoner was hidden too: %v", after)
	}

	// Re-adding the model must clear the marker so it comes back.
	if err := repo.UnhideModel([]string{"deepseek", "ds"}, "deepseek-chat"); err != nil {
		t.Fatalf("UnhideModel: %v", err)
	}
	restored := decodeModelIDs(t, h)
	if _, ok := restored["ds/deepseek-chat"]; !ok {
		t.Errorf("re-added model ds/deepseek-chat did not come back: %v", restored)
	}
}

// TestHandleModelsHidesRemovedCustomModel covers the customModels source, which
// is independent of the static registry.
func TestHandleModelsHidesRemovedCustomModel(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
		"ds|my-custom|llm", `{"id":"my-custom","providerAlias":"ds","type":"llm"}`,
	); err != nil {
		t.Fatalf("seed customModels: %v", err)
	}

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	before := decodeModelIDs(t, h)
	if _, ok := before["ds/my-custom"]; !ok {
		t.Fatalf("baseline: custom model ds/my-custom missing: %v", before)
	}

	if err := repo.HideModel([]string{"ds", "deepseek"}, "my-custom"); err != nil {
		t.Fatalf("HideModel: %v", err)
	}
	after := decodeModelIDs(t, h)
	if _, ok := after["ds/my-custom"]; ok {
		t.Errorf("removed custom model ds/my-custom is still advertised: %v", after)
	}
	// The underlying customModels row must NOT be destroyed: hiding is a
	// presentation concern and must be reversible.
	if customs, err := repo.GetCustomModels(); err == nil {
		found := false
		for _, cm := range customs {
			if cm.ID == "my-custom" {
				found = true
			}
		}
		if !found {
			t.Errorf("HideModel destroyed the customModels row; it must remain stored")
		}
	}
}

// TestHideModelMatchesAliasSpellings ensures a removal recorded under one
// provider spelling is honoured when the list is built under another.
func TestHideModelMatchesAliasSpellings(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	// Record the removal under the canonical id only.
	if err := repo.HideModel([]string{"deepseek"}, "deepseek-reasoner"); err != nil {
		t.Fatalf("HideModel: %v", err)
	}
	// buildModelsList iterates by provider id ("deepseek") and outputs under the
	// alias ("ds"); either spelling must match the stored marker... which it
	// does because the marker key is the canonical id here.
	ids := decodeModelIDs(t, h)
	if _, ok := ids["ds/deepseek-reasoner"]; ok {
		t.Errorf("removal recorded as canonical id was not honoured: %v", ids)
	}
}

// TestHiddenModelRefusesToServe pins the other half of "Remove model".
//
// Hiding a model is a routing decision, not a display preference: the operator
// is saying "never send traffic here". The list endpoints honoured the marker,
// but resolveModelEntry did not, so a hidden model stayed callable by name and
// — worse — stayed in service inside a combo, which is exactly the place an
// operator would go to remove a bad route.
//
// The check lives in resolveModelEntry because every combo leaf and every
// direct "provider/model" request funnels through it.
func TestHiddenModelRefusesToServe(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	// Both spellings must resolve before the removal, or the test would pass
	// for the wrong reason (a missing model is not a filtered one).
	for _, entry := range []string{"workbuddy/some-model", "wb/some-model"} {
		if h.resolveModelEntry(entry) == nil {
			t.Fatalf("%s did not resolve before the removal; test setup is wrong", entry)
		}
	}

	if err := repo.HideModel([]string{"workbuddy", "wb"}, "some-model"); err != nil {
		t.Fatalf("HideModel: %v", err)
	}

	for _, entry := range []string{"workbuddy/some-model", "wb/some-model"} {
		if info := h.resolveModelEntry(entry); info != nil {
			t.Errorf("%s still resolves to %s/%s after being removed", entry, info.Provider, info.Model)
		}
	}

	// A sibling on the same provider must keep working: the removal is per
	// model, and blanking the whole provider would be a different bug.
	if h.resolveModelEntry("workbuddy/other-model") == nil {
		t.Error("removing one model also blocked its siblings on the same provider")
	}
}

// TestHiddenModelRefusesMainPath covers the inference path specifically.
//
// There are two resolvers: resolveModel (what every request handler calls) and
// resolveModelEntry (used while expanding combo leaves). Guarding only the
// combo side left the direct route wide open — hiding a model removed it from
// /v1/models and from combos, yet a plain "cl/model" request still got through
// and was billed. Both paths must refuse it.
func TestHiddenModelRefusesMainPath(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	for _, entry := range []string{"workbuddy/some-model", "wb/some-model"} {
		if _, err := h.resolveModel(entry); err != nil {
			t.Fatalf("%s did not resolve before the removal: %v", entry, err)
		}
	}

	if err := repo.HideModel([]string{"workbuddy", "wb"}, "some-model"); err != nil {
		t.Fatalf("HideModel: %v", err)
	}

	for _, entry := range []string{"workbuddy/some-model", "wb/some-model"} {
		info, err := h.resolveModel(entry)
		if err == nil || info != nil {
			t.Errorf("%s still resolves on the main path after removal (info=%v, err=%v)", entry, info, err)
		}
	}

	if _, err := h.resolveModel("workbuddy/other-model"); err != nil {
		t.Errorf("removing one model also blocked its siblings on the same provider: %v", err)
	}
}
