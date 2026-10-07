package dashboard

import (
	"os"
	"strings"
	"testing"
)

// The providers list gives each card a switch that disables every account of
// that provider at once. Three things have to hold for that to be safe:
//
//  1. Clicking the switch must not also open the detail page. The card's
//     onclick covers the whole tile, so the control has to stop the event.
//  2. The switch must post the state the operator chose, not a flip. A card
//     rendered before another tab paused an account would otherwise write the
//     opposite of the intent.
//  3. A provider with some accounts paused must not render as off. It still
//     serves traffic, and showing it dimmed with the switch off would invite
//     the operator to "fix" a provider that is working.
func TestProviderCardOffersDisableSwitch(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `onchange="toggleProviderAll('${prov}', this.checked);"`) {
		t.Error("provider card is missing the bulk enable/disable switch")
	}

	// The switch lives inside the card, which opens the detail page on click.
	// Without stopPropagation the toggle would also navigate away. Anchor on the
	// markup, not the class name: the first "prov-card-switch" in the file is
	// its own stylesheet rule.
	i := strings.Index(ui, `<label class="switch prov-card-switch"`)
	if i < 0 {
		t.Fatal("provider card switch markup not found")
	}
	sw := ui[i:]
	sw = sw[:strings.Index(sw, "</label>")]
	if !strings.Contains(sw, "event.stopPropagation()") {
		t.Error("provider card switch does not stop propagation, so toggling it also opens the detail page")
	}

	// The partial state is derived from the per-account switch positions, and
	// only the all-off case may render the card as disabled. It must go through
	// isSwitchOff rather than reading isActive inline: the count is about the
	// switch, while account health is a separate question answered by
	// isEffectivelyActive (a credit-exhausted account stays switched on).
	if !strings.Contains(ui, "function isSwitchOff(p)") {
		t.Error("provider card has no switch-position helper")
	}
	if !strings.Contains(ui, "accounts.filter(a => !isSwitchOff(a)).length") {
		t.Error("provider card does not count switched-on accounts, so it cannot tell partial from off")
	}
	if !strings.Contains(ui, "const allOff = activeCount === 0 && !head.noConnection;") {
		t.Error("provider card off-state must require every account to be inactive and the provider to have accounts")
	}
	if !strings.Contains(ui, "prov-card-off") {
		t.Error("provider card has no dimmed style for a disabled provider")
	}
}

// The bulk endpoint must receive an explicit target, and the provider id has to
// be encoded because a custom node id contains characters that would otherwise
// break the path.
func TestProviderCardSwitchPostsExplicitState(t *testing.T) {
	ui := readEmbeddedUI(t)

	i := strings.Index(ui, "async function toggleProviderAll(")
	if i < 0 {
		t.Fatal("toggleProviderAll is not defined")
	}
	fn := ui[i:]
	fn = fn[:strings.Index(fn, "\n  }\n")]

	if !strings.Contains(fn, "/toggle-all") {
		t.Error("toggleProviderAll does not call the bulk endpoint")
	}
	if !strings.Contains(fn, "encodeURIComponent(prov)") {
		t.Error("toggleProviderAll does not encode the provider id")
	}
	if !strings.Contains(fn, "JSON.stringify({ active: active ? 1 : 0 })") {
		t.Error("toggleProviderAll must send the chosen state explicitly, not rely on a server-side flip")
	}
	// A failed toggle must not leave the switch showing a state the server
	// rejected.
	if !strings.Contains(fn, "loadProviders()") {
		t.Error("toggleProviderAll does not refresh after the request")
	}
	if !strings.Contains(fn, "catch(e) { toast(e.message, true); loadProviders(); }") {
		t.Error("toggleProviderAll must reload the cards when the request fails, to undo the optimistic switch")
	}
}

// The router has to skip a disabled provider. Without this the UI would be
// cosmetic: the switch would move and the provider would keep taking traffic.
func TestDisabledProviderIsSkippedByRouter(t *testing.T) {
	b, err := os.ReadFile("../chat/chat.go")
	if err != nil {
		t.Fatalf("read chat.go: %v", err)
	}
	src := string(b)

	if !strings.Contains(src, "disabledProviders[c.Provider] = true") {
		t.Error("router does not collect disabled providers")
	}
	if !strings.Contains(src, "GetProviderConnections(\"\", true)") {
		t.Error("router does not restrict the active list to enabled connections")
	}
	// Alias handling matters: a combo entry may name the alias, so marking only
	// the raw provider leaves the disabled provider reachable by its alias.
	if !strings.Contains(src, "disabledProviders[alias] = true") {
		t.Error("router does not mark the provider alias as disabled")
	}
}

// The Usage page draws a "Provider Topology" graph. It must not draw a
// provider the operator switched off: the graph answers "what can serve right
// now", and a disabled node with live edges beside it reads as a working route.
//
// The original check was `p.isActive === false`, but the API sends isActive as
// a NUMBER (0/1). Strict equality against false is always false for 0, so the
// filter never fired and every disabled provider stayed in the graph.
func TestTopologySkipsDisabledProviders(t *testing.T) {
	ui := readEmbeddedUI(t)

	if strings.Contains(ui, "p.isActive === false") {
		t.Error("topology compares isActive to the boolean false, but the API sends 0/1 — the filter can never match")
	}
	if !strings.Contains(ui, "function topologyProviders()") {
		t.Fatal("topologyProviders is missing")
	}

	i := strings.Index(ui, "function topologyProviders()")
	fn := ui[i:]
	fn = fn[:strings.Index(fn, "\n  }\n")]

	for _, need := range []string{
		"const isDisabled = (id) => {",
		"Number(x.isActive) === 0",
		// A provider with one live account out of three still serves, so only
		// the all-accounts-off case may hide it.
		"owned.every(x => Number(x.isActive) === 0)",
		// Usage history keys arrive as aliases (cl/…), so the lookup has to
		// match the canonical row too rather than only the raw provider id.
		"Array.isArray(x.aliases)",
		// A keyless provider (the free OpenCode tier) has no isActive flag and
		// is a real route, so it must stay in the graph.
		"owned.every(x => x.noConnection)",
	} {
		if !strings.Contains(fn, need) {
			t.Errorf("topologyProviders is missing: %q", need)
		}
	}

	// The filter has to run before the byProvider keys are pushed: those come
	// from usage history, so a provider that served yesterday and was switched
	// off today would otherwise reappear through that path.
	pushIdx := strings.Index(fn, "Object.keys(usageState.stats.byProvider || {}).forEach(push)")
	disIdx := strings.Index(fn, "const isDisabled = (id) => {")
	if pushIdx < 0 || disIdx < 0 || disIdx > pushIdx {
		t.Error("the disabled check must be defined before the usage-history keys are pushed")
	}
}
