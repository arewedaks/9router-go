package chat

import (
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// An operator removal must hold under every spelling of the provider.
//
// nace has two aliases (nc, drex) and the primary one is chosen per process, so
// a model hidden while "drex" was primary was written as drex|drex-max. When the
// build later served nc/, the old two-key check ({providerID, alias}) missed it
// and /v1/models advertised nc/drex-max — which then answered 400 "model
// drex-max was removed from provider nace" when called. Listing and serving must
// agree.
func TestHiddenModelHonouredUnderEveryAlias(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	const model = "drex-max"

	// Ask the list to hide it under a spelling that is NOT the render alias.
	if err := repo.HideModel([]string{"drex"}, model); err != nil {
		t.Fatalf("HideModel: %v", err)
	}
	if !h.isModelHidden("nc", model) {
		// isModelHidden fans out over hiddenModelKeys, so this alone should pass
		// even before the list is rebuilt — it is the list that used to miss it.
		t.Fatal("isModelHidden(nc, drex-max) = false; a removal under another alias must still apply")
	}

	// And the request-resolution route must reject it, so listing and serving
	// agree: a model the operator removed must not answer 200 either.
	if _, err := h.resolveModel("nc/" + model); err == nil {
		t.Error("resolveModel accepted a hidden model; the request would then 400 at the upstream")
	}
}

// AliasesFor must name every alias, or a fan-out built on it is incomplete.
func TestAliasesForCoversNace(t *testing.T) {
	got := providers.AliasesFor("nace")
	if len(got) < 2 {
		t.Fatalf("AliasesFor(nace) = %v, want both nc and drex", got)
	}
	seen := map[string]bool{}
	for _, a := range got {
		seen[a] = true
	}
	for _, want := range []string{"nc", "drex"} {
		if !seen[want] {
			t.Errorf("AliasesFor(nace) missing %q: %v", want, got)
		}
	}
}