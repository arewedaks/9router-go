package dashboard

import (
	"strings"
	"testing"
)

// The model chips inside a card must still carry the per-provider colour and the
// split provider/model label: every chip used to be the same orange with the full
// provider-qualified id, which is what made a long combo unreadable.
func TestComboModelChipsKeepProviderTint(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []struct{ s, why string }{
		{"function modelChip(", "no chip renderer"},
		{"model-chip-prov", "the provider is not shown separately from the model name"},
		{"model-chip-dot", "no per-provider colour marker"},
		{"--chip-tint", "chips carry no per-provider colour variable"},
	} {
		if !strings.Contains(ui, need.s) {
			t.Errorf("missing %q: %s", need.s, need.why)
		}
	}

	// The provider prefix must be split on the FIRST slash. Aggregator ids look
	// like bm/nvidia/llama-…, and splitting on the last slash would label every
	// one of them with the inner vendor instead of the actual provider.
	at := strings.Index(ui, "function modelChip(")
	if at < 0 {
		t.Fatal("no modelChip function")
	}
	block := ui[at:]
	if end := strings.Index(block, "\n  }"); end >= 0 {
		block = block[:end]
	}
	if !strings.Contains(block, "indexOf(\"/\")") {
		t.Error("modelChip does not split on the first slash, so aggregator ids " +
			"like bm/nvidia/llama-… get the wrong provider label")
	}
}

// Combo targets are written with provider aliases (ag/…, cl/…, kr/…) because the
// model ids come from the router's prefix table, while the provider list the UI
// holds is keyed by canonical id. Resolving only on provider/id made every
// aliased model miss its registry entry, so its chip lost both the readable
// label and the colour and fell back to grey.
//
// The server already resolves aliases (providers.AliasesFor) and sends them as
// `aliases` on each provider, so providerEntry must consult that list rather
// than keeping a second alias table in the browser.
func TestProviderEntryResolvesAliases(t *testing.T) {
	ui := readEmbeddedUI(t)

	at := strings.Index(ui, "function providerEntry(")
	if at < 0 {
		t.Fatal("no providerEntry function")
	}
	block := ui[at:]
	if end := strings.Index(block, "\n  }"); end >= 0 {
		block = block[:end]
	}

	if !strings.Contains(block, "aliases") {
		t.Error("providerEntry does not consult the provider's `aliases` list, " +
			"so aliased combo targets (ag/…, cl/…, kr/…) resolve to no provider " +
			"and render grey with a bare alias as their label")
	}
}

// The combo list renders as cards, not a table. A table forced five columns on
// data with two useful fields (alias, strategy) and pushed the model list into a
// scrolling cell, which is what made a 28-model combo unreadable.
func TestCombosRenderAsCards(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []struct{ s, why string }{
		{"function comboCard(", "no card renderer"},
		{"class=\"combo-grid\"", "the cards are not laid out in a grid"},
		{"data-combo-card=", "cards are not addressable, so expanding one cannot work"},
	} {
		if !strings.Contains(ui, need.s) {
			t.Errorf("missing %q: %s", need.s, need.why)
		}
	}

	// The table body id must be gone: leaving it would mean a stale render path
	// silently writing into a container that no longer exists.
	if strings.Contains(ui, "combos-table-body") {
		t.Error("the old table body id is still referenced after the card rewrite")
	}
}

// Each card carries exactly the three icon actions that were asked for, and the
// copy action must copy the alias (the model id a client sends), not the row's
// internal uuid.
func TestComboCardActions(t *testing.T) {
	ui := readEmbeddedUI(t)

	at := strings.Index(ui, "function comboCard(")
	if at < 0 {
		t.Fatal("no comboCard function")
	}
	block := ui[at:]
	if end := strings.Index(block, "\n  }"); end >= 0 {
		block = block[:end]
	}

	for _, need := range []struct{ s, why string }{
		{"msym\">edit</span>", "no edit icon"},
		{"msym\">content_copy</span>", "no copy icon"},
		{"msym\">delete</span>", "no delete icon"},
		{"editCombo(", "edit is not wired"},
		{"copyCombo(", "copy is not wired"},
		{"deleteCombo(", "delete is not wired"},
	} {
		if !strings.Contains(block, need.s) {
			t.Errorf("combo card missing %q: %s", need.s, need.why)
		}
	}

	// copyCombo must write c.name, not c.id.
	copyAt := strings.Index(ui, "async function copyCombo(")
	if copyAt < 0 {
		t.Fatal("no copyCombo function")
	}
	copyBlock := ui[copyAt:]
	if end := strings.Index(copyBlock, "\n  }"); end >= 0 {
		copyBlock = copyBlock[:end]
	}
	if !strings.Contains(copyBlock, "clipboard.writeText(c.name)") {
		t.Error("copyCombo does not copy the alias; clients need the model id, " +
			"not the internal row uuid")
	}
}

// The model list is collapsed by default and revealed from the count, so a
// 28-model combo does not dominate the grid.
func TestComboCardCollapsesModelList(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []struct{ s, why string }{
		{"function toggleComboModels(", "no disclosure control"},
		{"combo-card-models\" hidden", "the model list is not collapsed by default"},
		{"aria-expanded", "the disclosure control does not expose its state"},
	} {
		if !strings.Contains(ui, need.s) {
			t.Errorf("missing %q: %s", need.s, need.why)
		}
	}

	// [hidden] loses to display:flex, so the attribute alone would not collapse
	// the list and every card would render its models.
	if !strings.Contains(ui, ".combo-card-models[hidden] { display: none; }") {
		t.Error("no [hidden] display override, so display:flex wins and the " +
			"model list stays visible on every card")
	}
}
