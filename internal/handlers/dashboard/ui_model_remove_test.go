package dashboard

import (
	"strings"
	"testing"
)

// Removing a model used to call openProviderDetail(), which blanks the pane to
// "Loading…" and re-fetches the whole provider. For an operator clearing a
// provider's dead models by hand that meant the page flashed and scrolled back
// to the top after every single click, so removing several in a row was a
// scroll-and-hunt exercise.
//
// The row must be dropped from the cached detail and repainted in place.
func TestUIRemoveModelDoesNotRefetchThePage(t *testing.T) {
	ui := readEmbeddedUI(t)

	body := extractFunction(t, ui, "removeModel")

	if !strings.Contains(body, "renderProviderDetailRefresh()") {
		t.Error("removeModel must repaint the models list in place")
	}
	// A refetch is still correct when the DELETE failed (the row must come back
	// rather than lie about what is stored) or when there is no cached detail to
	// edit. What must not happen is a refetch after a *successful* delete, and
	// the tell is that the in-place repaint comes first and returns.
	marker := "if (lastProviderDetail"
	at := strings.Index(body, marker)
	if at < 0 {
		marker = "if lastProviderDetail"
		at = strings.Index(body, marker)
	}
	if at < 0 {
		t.Fatal("removeModel has no in-place repaint branch")
	}
	happy := body[at:]
	refreshAt := strings.Index(happy, "renderProviderDetailRefresh()")
	if refreshAt < 0 {
		t.Fatal("the in-place branch never repaints")
	}
	if ret := strings.Index(happy[refreshAt:], "return"); ret < 0 {
		t.Error("the in-place branch must return, or it falls through to a refetch")
	}
	if !strings.Contains(body, "lastProviderDetail.provider === providerId") {
		t.Error("removeModel must verify the cached detail belongs to this provider before editing it")
	}
	if !strings.Contains(body, "filter(m => m.modelId !== modelId)") {
		t.Error("removeModel must drop the row from the cached payload, or the next re-render paints it back")
	}
	// Per-model Test state must go with the row, or a later import of the same
	// id would show a stale "failed" badge from the previous incarnation.
	for _, stale := range []string{"modelTestResults", "modelTestError", "modelTestLatency"} {
		if !strings.Contains(body, "delete "+stale+"[modelId]") {
			t.Errorf("removeModel does not clear %s for the removed model", stale)
		}
	}
}

// The auto-disable pass inside "Test All Models" deletes failing models
// server-side. It used to answer that with a full openProviderDetail(), so a
// run that took a minute ended by flashing the page and losing the operator's
// scroll position — the same complaint as the manual delete.
func TestUITestAllModelsAutoDisableRepaintsInPlace(t *testing.T) {
	ui := readEmbeddedUI(t)

	body := extractFunction(t, ui, "testAllModels")

	if !strings.Contains(body, "removedModelIds") {
		t.Fatal("auto-disable must remember which models it removed")
	}
	if !strings.Contains(body, "filter(m => !removedModelIds.has(m.modelId))") {
		t.Error("auto-disable must drop removed rows from the cached payload before repainting")
	}
	if strings.Contains(body, `removed > 0) {
        // Models were removed server-side, so re-fetch`) {
		t.Error("auto-disable still re-fetches the whole provider")
	}
	// Exactly one refresh call at the end of the run, shared by both branches.
	if n := strings.Count(body, "renderProviderDetailRefresh()"); n < 2 {
		t.Errorf("expected in-flight row repaints plus a final one, found %d refresh calls", n)
	}
}
