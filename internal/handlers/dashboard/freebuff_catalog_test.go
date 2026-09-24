package dashboard

import (
	"testing"

	"9router/proxy/internal/proxy/executor"
)

// TestFreebuffCatalogMatchesExecutor guards the one property that matters for
// this catalogue: every model Import offers must be one the router can route.
//
// The two lists live in different packages and describe the same capability —
// the executor's freebuffRootAgentByModel decides what can actually run, and
// this catalogue decides what the operator sees. A model in the catalogue but
// absent from the map is a row that fails on the first request; a model in the
// map but absent from the catalogue is invisible capability. Both directions
// fail here rather than at request time.
func TestFreebuffCatalogMatchesExecutor(t *testing.T) {
	routable := map[string]bool{}
	for _, m := range executor.FreebuffSupportedModels() {
		routable[m] = true
	}
	if len(routable) == 0 {
		t.Fatal("executor reports no routable freebuff models")
	}

	seen := map[string]bool{}
	for _, m := range freebuffStaticModels() {
		if m.ID == "" {
			t.Error("catalogue contains an empty model id")
			continue
		}
		if seen[m.ID] {
			t.Errorf("catalogue contains %q twice", m.ID)
		}
		seen[m.ID] = true
		if !routable[m.ID] {
			t.Errorf("catalogue offers %q, which the executor cannot route", m.ID)
		}
		if m.Name == "" {
			t.Errorf("catalogue entry %q has no display name", m.ID)
		}
	}

	for id := range routable {
		if !seen[id] {
			t.Errorf("executor routes %q but the catalogue does not offer it", id)
		}
	}
}

// TestFreebuffImportIsSupported pins the user-visible outcome: a connected
// account must be able to import models. Before this catalogue existed the
// endpoint answered supported=false with "does not support models listing",
// which left a connected account with zero models.
func TestFreebuffImportIsSupported(t *testing.T) {
	if !isFreebuffProvider("freebuff") || !isFreebuffProvider("fb") {
		t.Fatal("freebuff and its alias fb must both resolve")
	}
	if isFreebuffProvider("codebuff-not-a-provider") {
		t.Error("an unrelated id must not resolve to freebuff")
	}
	if got := len(freebuffStaticModels()); got == 0 {
		t.Fatal("freebuff catalogue is empty; Import would report no models")
	}
}
