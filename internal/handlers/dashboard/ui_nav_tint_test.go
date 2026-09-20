package dashboard

import (
	"regexp"
	"strings"
	"testing"
)

// Every sidebar entry must carry its own --tint, because the tile's fill,
// border and drop shadow are all derived from it. An entry with no rule falls
// back to the default accent and silently becomes a second orange tile, which
// is exactly the "everything looks the same" problem the tints exist to solve.
func TestEverySidebarEntryHasATint(t *testing.T) {
	ui := readEmbeddedUI(t)

	// Collect the nav ids from the markup rather than from the stylesheet, so a
	// newly added button is covered even if nobody writes a rule for it.
	navRe := regexp.MustCompile(`<button class="nav-btn" id="(nav-[a-z-]+)"`)
	ids := navRe.FindAllStringSubmatch(ui, -1)
	if len(ids) < 7 {
		t.Fatalf("found %d nav buttons, expected at least 7", len(ids))
	}

	tintRe := regexp.MustCompile(`#(nav-[a-z-]+)\s+\.nav-icon\s*\{\s*--tint:`)
	tinted := map[string]bool{}
	for _, m := range tintRe.FindAllStringSubmatch(ui, -1) {
		tinted[m[1]] = true
	}

	for _, m := range ids {
		if !tinted[m[1]] {
			t.Errorf("%s has no --tint rule; its tile will fall back to the "+
				"default accent colour", m[1])
		}
	}

	// The palette is shared with the status colours used elsewhere, so a typo in
	// a hex value is worth catching: every tint must be a 6-digit hex.
	for _, h := range regexp.MustCompile(`--tint:\s*(#[0-9a-fA-F]{3,8})`).FindAllStringSubmatch(ui, -1) {
		if len(h[1]) != 7 {
			t.Errorf("--tint %s is not a 6-digit hex colour", h[1])
		}
	}
}

// The tinted tile uses color-mix(), which Safari < 16.2 and Firefox < 113 do
// not implement. Without the @supports fallback the gradient and border compute
// to nothing and the tile disappears into the rail.
func TestNavIconHasColorMixFallback(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "color-mix(in srgb") {
		t.Fatal("the nav tiles no longer use color-mix; this test guards the fallback for it")
	}
	if !strings.Contains(ui, "@supports not (color: color-mix(") {
		t.Error("color-mix is used with no @supports fallback, so the nav " +
			"tiles render with no background on older Safari and Firefox")
	}
}
