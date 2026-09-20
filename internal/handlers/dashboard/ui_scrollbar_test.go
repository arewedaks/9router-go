package dashboard

import (
	"strings"
	"testing"
)

// The dashboard is dark, and an unstyled scrollbar is the one element the
// palette cannot reach: the OS paints a pale grey channel that reads as a seam
// down every panel.
//
// The rules are the webkit pseudo-element family, which Chrome, Edge, Safari
// and Firefox 64+ all honour and which is the only syntax that can inset a
// rounded, tinted thumb.
func TestScrollbarsAreThemed(t *testing.T) {
	ui := readEmbeddedUI(t)

	required := []struct {
		need string
		why  string
	}{
		{"::-webkit-scrollbar {", "no scrollbar sizing rule"},
		{"::-webkit-scrollbar-track", "the default track is painted opaque"},
		{"::-webkit-scrollbar-thumb", "the thumb keeps the OS grey"},
		{"::-webkit-scrollbar-corner", "the corner square between two " +
			"scrollbars is left white"},
		{"::-webkit-scrollbar-button", "default arrow buttons are still drawn"},
	}
	for _, r := range required {
		if !strings.Contains(ui, r.need) {
			t.Errorf("missing %q: %s", r.need, r.why)
		}
	}

	// The thumb must be tinted with the theme accent, not left neutral. A grey
	// thumb on a warm page is the specific thing this replaced.
	if !strings.Contains(ui, "rgba(255, 138, 42, 0.30)") &&
		!strings.Contains(ui, "rgba(255, 138, 42, 0.3)") {
		t.Error("the scrollbar thumb is not tinted with the accent colour, so " +
			"it does not match the theme")
	}

	// background-clip is what insets the thumb inside its channel; without it
	// the rounded corners are clipped by the 10px gutter and the pill looks
	// square on the outer edge.
	if !strings.Contains(ui, "background-clip: content-box") {
		t.Error("no background-clip: content-box, so the thumb is not inset " +
			"and its rounded corners are cut off by the scrollbar channel")
	}
}

// The standard scrollbar properties must stay inside @supports, never global.
//
// Chrome 153 drops the entire ::-webkit-scrollbar-* family for an element that
// also has scrollbar-width or scrollbar-color set, and paints its own thin
// native bar instead — arrow buttons included. That combination was the
// original bug: the themed thumb vanished and the arrows reappeared. Measured
// directly: with the standard properties present the thumb colour disappeared
// from the scrollbar column entirely; removing them restored it.
func TestStandardScrollbarPropsAreScopedToNonWebkit(t *testing.T) {
	ui := readEmbeddedUI(t)

	gate := strings.Index(ui, "@supports not selector(::-webkit-scrollbar)")
	if gate < 0 {
		t.Fatal("no @supports guard around the standard scrollbar properties, so " +
			"they are applied globally and Chrome ignores the webkit rules")
	}

	// Everything before the guard must be free of the standard properties:
	// a global declaration anywhere above it would defeat the scoping.
	before := ui[:gate]
	for _, prop := range []string{"scrollbar-width:", "scrollbar-color:"} {
		if n := strings.Count(before, prop); n > 0 {
			t.Errorf("%s is declared %d time(s) outside @supports; Chrome then "+
				"ignores ::-webkit-scrollbar-* and draws its native scrollbar "+
				"(arrows and all) instead of the themed thumb", prop, n)
		}
	}

	// And the guard must actually contain both properties, or the fallback for
	// engines without the webkit pseudo-elements is gone.
	after := ui[gate:]
	if end := strings.Index(after, "}"); end >= 0 {
		// Only the immediate block matters, but the rules follow the opening
		// brace, so look at a bounded window instead of the first brace.
	}
	window := after
	if len(window) > 400 {
		window = window[:400]
	}
	for _, prop := range []string{"scrollbar-width:", "scrollbar-color:"} {
		if !strings.Contains(window, prop) {
			t.Errorf("@supports block does not set %s, so engines without "+
				"::-webkit-scrollbar get an unstyled bar", prop)
		}
	}
}

