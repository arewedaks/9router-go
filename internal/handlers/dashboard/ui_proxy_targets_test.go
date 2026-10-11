package dashboard

import (
	"strings"
	"testing"
)

// Checkboxes for pool targets must carry onchange so clicking them persists the
// selection to settings.providerStrategies immediately without requiring a full reload.
func TestUIProxyTargetCheckboxesTriggerSave(t *testing.T) {
	ui := readEmbeddedUI(t)

	card := extractFunction(t, ui, "renderNoAuthProxyCard")
	if card == "" {
		t.Fatal("renderNoAuthProxyCard missing from UI")
	}

	if !strings.Contains(card, `data-pool-target=`) {
		t.Error("card must render data-pool-target checkboxes")
	}
	if !strings.Contains(card, `onchange="onPoolTargetToggle(`) {
		t.Error("target checkboxes must trigger onPoolTargetToggle onchange")
	}

	toggleFn := extractFunction(t, ui, "onPoolTargetToggle")
	if toggleFn == "" {
		t.Fatal("onPoolTargetToggle handler must be defined")
	}
	if !strings.Contains(toggleFn, "saveNoAuthProxyStrategy(") {
		t.Error("onPoolTargetToggle must persist via saveNoAuthProxyStrategy")
	}

	saveFn := extractFunction(t, ui, "saveNoAuthProxyStrategy")
	if saveFn == "" {
		t.Fatal("saveNoAuthProxyStrategy missing")
	}
	// Switching strategy must preserve prior targets when checkboxes are not yet in DOM
	if !strings.Contains(saveFn, "targetProxyPoolIds") {
		t.Error("saveNoAuthProxyStrategy must capture targetProxyPoolIds")
	}
}
