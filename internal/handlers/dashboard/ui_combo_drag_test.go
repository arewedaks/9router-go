package dashboard

import (
	"strings"
	"testing"
)

// The combo model picker must support drag-to-reorder alongside the up/down
// buttons: rows carry draggable + index, the container wires native HTML5
// DnD once (dataset guard), the drop handler splices around removal, and
// paint classes show the drop position.
func TestUIComboDragReorder(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `draggable="true"`) || !strings.Contains(ui, `data-idx="${i}"`) {
		t.Error("combo model rows are not draggable with an index")
	}
	fn := extractFunction(t, ui, "wireComboDrag")
	for _, want := range []string{
		`box.dataset.dragWired`, // binds once, survives re-renders
		`dragstart`,             // remember source index
		`dragover`,              // paint drop position
		`splice(dragIdx, 1)`,    // remove dragged item first
		`overIdx > dragIdx ? overIdx - 1 : overIdx`, // downward insert shifts by one
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("wireComboDrag lacks %q", want)
		}
	}
	// Grip affordance exists for discoverability.
	if !strings.Contains(ui, "combo-model-drag") {
		t.Error("no drag grip element on the rows")
	}
}
