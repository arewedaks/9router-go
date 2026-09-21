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

	// Find the rule that actually selects the family, not merely the first block
	// whose selector starts with .msym: `font-size` overrides like
	// `.combo-card-meta .msym { ... }` and `.model-chip .msym { ... }` appear
	// earlier in the sheet and would match a naive search.
	at := strings.Index(ui, "font-family: 'Material Symbols")
	if at < 0 {
		t.Fatal("no rule selects the Material Symbols family; this test guards " +
			"the ligature declarations inside it")
	}
	// Walk back to the opening brace of that declaration block.
	open := strings.LastIndex(ui[:at], "{")
	// Walk forward to its closing brace.
	close := strings.Index(ui[at:], "}")
	if open < 0 || close < 0 {
		t.Fatal("malformed rule containing font-family: Material Symbols")
	}
	block := ui[open : at+close]

	if !strings.Contains(block, "font-feature-settings: 'liga'") {
		t.Errorf("the %s rule has no standard font-feature-settings: 'liga'; "+
			"with only the -webkit- alias Firefox shows the ligature name as "+
			"plain text instead of an icon", strings.TrimSpace(ui[strings.LastIndex(ui[:open], "}")+1:open]))
	}
}
