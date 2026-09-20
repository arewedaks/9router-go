package dashboard

import (
	"strings"
	"testing"
)

// Material Symbols is a ligature font: the element's text ('api', 'dns') is
// swapped for a glyph only when the 'liga' feature is on.
//
// -webkit-font-feature-settings is an alias Firefox does not implement, so a
// rule carrying only the prefixed form looked correct in Chrome while Firefox
// rendered the literal words "api", "dns", "merge" and "settings" down the
// sidebar instead of icons. Both declarations have to be present.
//
// Scope note: this asserts the two rules that select the ligature family. A
// repo-wide emoji ban is deliberately NOT written, because the UI still uses
// emoji on purpose in several places (status dots chosen from data, warning
// prefixes in paragraphs, the Rust/crab glyphs in the token-optimizer cards),
// and a blanket rule would fail on all of them without anything being wrong.
func TestLigatureFontsDeclareStandardFeatureSettings(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, cls := range []string{".msym {", ".nav-icon {"} {
		at := strings.Index(ui, cls)
		if at < 0 {
			t.Fatalf("stylesheet no longer defines %s", cls)
		}
		block := ui[at:]
		if end := strings.Index(block, "}"); end >= 0 {
			block = block[:end]
		}
		if !strings.Contains(block, `font-family: 'Material Symbols`) {
			t.Errorf("%s no longer selects the Material Symbols family; this "+
				"test guards the ligature declarations inside it", cls)
		}
		if !strings.Contains(block, "font-feature-settings: 'liga'") {
			t.Errorf("%s has no standard font-feature-settings: 'liga'; with "+
				"only the -webkit- alias Firefox shows the ligature name as "+
				"plain text instead of an icon", cls)
		}
	}
}
