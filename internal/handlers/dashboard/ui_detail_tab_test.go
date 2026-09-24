package dashboard

import (
	"regexp"
	"strings"
	"testing"
)

// TestUIDetailTabsHideEveryPanel guards the fix for a quota panel that leaked
// onto other tabs.
//
// showDetailTab hid a hardcoded list of panel ids, and dt-quota was missing from
// it. Opening Quota and then switching to Connections left the quota rows
// rendered underneath, because nothing ever set dt-quota back to display:none.
// The list is now derived from the DOM, so a panel added to the markup cannot be
// forgotten — this asserts that property rather than the current id set, which
// is what let the bug through the first time.
func TestUIDetailTabsHideEveryPanel(t *testing.T) {
	ui := readEmbeddedUI(t)

	// Every panel the markup declares, i.e. every id showDetailTab must hide.
	panels := regexp.MustCompile(`id="(dt-[a-z]+)"`).FindAllStringSubmatch(ui, -1)
	if len(panels) < 2 {
		t.Fatalf("found %d detail panels, expected the tabbed set", len(panels))
	}
	declared := map[string]bool{}
	for _, m := range panels {
		declared[m[1]] = true
	}
	// The quota panel is the one that leaked; a regression that renames it away
	// would make this test vacuous.
	if !declared["dt-quota"] {
		t.Fatal("dt-quota panel not found in the markup")
	}

	// showDetailTab must not enumerate ids by hand: a literal "dt-quota" inside
	// the hide list is exactly the bug (a hand-maintained list that omitted it).
	body := functionBody(t, ui, "function showDetailTab(")
	if strings.Contains(body, `"dt-conn"`) || strings.Contains(body, `"dt-quota"`) {
		t.Errorf("showDetailTab still enumerates panel ids by hand:\n%s", body)
	}
	// It must instead select the whole family, so every declared panel is hidden.
	if !strings.Contains(body, `[id^="dt-"]`) {
		t.Errorf("showDetailTab does not select all dt- panels:\n%s", body)
	}
}

// functionBody returns the source of a JS function, from its signature to the
// matching closing brace at column 0. Enough to assert on a handler's body
// without pulling in a JS parser.
func functionBody(t *testing.T, src, signature string) string {
	t.Helper()
	i := strings.Index(src, signature)
	if i < 0 {
		t.Fatalf("signature %q not found", signature)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n  }\n"); j >= 0 {
		return rest[:j]
	}
	if j := strings.Index(rest, "\n}"); j >= 0 {
		return rest[:j]
	}
	return rest
}
