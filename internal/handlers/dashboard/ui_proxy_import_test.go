package dashboard

import (
	"strings"
	"testing"
)

// Importing a list must remain possible for keyless providers: they own no
// connection row, so the proxy card is their only configurable surface. The
// inline textarea that used to live on that card was removed as a duplicate of
// the Proxies tab, so the card must now point at the tab instead of leaving the
// operator with no route to import at all.
func TestUIProxyCardPointsAtImportSurface(t *testing.T) {
	ui := readEmbeddedUI(t)

	card := extractFunction(t, ui, "renderNoAuthProxyCard")

	// The duplicate textarea must be gone: two import UIs sharing one endpoint
	// meant two places to fix whenever the import rules changed.
	for _, gone := range []string{"proxy-import-list", "importProxyList("} {
		if strings.Contains(card, gone) {
			t.Errorf("proxy card still carries the removed inline importer %q", gone)
		}
	}

	// But the card must not simply drop the capability: it has to send the
	// operator somewhere that can still do the job.
	if !strings.Contains(card, "openProxyBatchModal") && !strings.Contains(card, "navigateTab('proxies')") {
		t.Error("proxy card offers no path to import proxies now that the inline box is gone")
	}
}

func TestUIBatchImportPostsToPoolsEndpoint(t *testing.T) {
	ui := readEmbeddedUI(t)

	body := extractFunction(t, ui, "submitProxyBatch")

	if !strings.Contains(body, `/api/dashboard/proxy-pools`) {
		t.Fatal("submitProxyBatch does not POST to the proxy-pools endpoint")
	}
	if !strings.Contains(body, "method: \"POST\"") {
		t.Error("submitProxyBatch does not use POST")
	}
	// Attaching in the same request is the point: a pool nobody targets routes
	// nothing, and a strategy pointing at a missing pool silently falls back to
	// the host IP. This is the capability the removed inline importer had, so it
	// must have survived the move.
	if !strings.Contains(body, "provider:") {
		t.Error("submitProxyBatch does not send the provider to attach the pool to")
	}
	// The pool list must be refetched, or the new pool never appears in the
	// provider panel's picker and the operator's next save picks a stale value.
	if !strings.Contains(body, "loadProxyPools(true)") {
		t.Error("submitProxyBatch does not refresh the pool cache after importing")
	}
	// Skipped lines are reported rather than hidden: silently dropping input is
	// how an operator ends up with fewer proxies than they pasted.
	if !strings.Contains(body, "data.skipped") {
		t.Error("submitProxyBatch does not report skipped entries")
	}
}

// Rotating across pools is a no-op below two pools, so the rotating options are
// disabled and the reason is stated. VansRouter does the same
// (canRotate = proxyPools.length >= 2); without it an operator picks
// "Round-Robin", sees no error, and cannot tell that every request is still
// going through the single pool they already had.
func TestUIProxyCardDisablesRotationBelowTwoPools(t *testing.T) {
	ui := readEmbeddedUI(t)

	card := extractFunction(t, ui, "renderNoAuthProxyCard")

	if !strings.Contains(card, "canRotate") {
		t.Fatal("proxy card does not compute whether rotation is possible")
	}
	if !strings.Contains(card, "disabled") {
		t.Error("rotating strategies are not disabled when there is nothing to rotate")
	}
	if !strings.Contains(card, "activePoolCount") {
		t.Error("proxy card does not count eligible pools")
	}
	// Crucially the card must say that several proxies in ONE pool already
	// rotate per request — otherwise the fix reads as "buy more pools".
	if !strings.Contains(card, "Multiple proxies in a pool rotate per request") {
		t.Error("card does not explain per-request rotation within a single pool")
	}
}

// `const` is not hoisted, so reading a binding above its declaration throws
// "Cannot access 'X' before initialization" and blanks the entire provider page.
// That is exactly what happened when canRotate was introduced next to the hint
// text but read while building the strategy options above it.
func TestUIProxyCardDeclaresBindingsBeforeUse(t *testing.T) {
	ui := readEmbeddedUI(t)

	card := extractFunction(t, ui, "renderNoAuthProxyCard")

	for _, name := range []string{"canRotate", "activePoolCount"} {
		decl := strings.Index(card, "const "+name+" ")
		if decl < 0 {
			t.Fatalf("%s is not declared in the proxy card", name)
		}
		if first := strings.Index(card, name); first != decl+len("const ") {
			t.Errorf("%s is used at offset %d but declared at %d — it is read before initialisation",
				name, first, decl)
		}
	}
}
