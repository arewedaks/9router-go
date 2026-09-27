package dashboard

import (
	"os"
	"strings"
	"testing"
)

// The Import modal must present two categories — Free first, then Regular —
// with a labelled header per section, and keep the row shape that search,
// select-all and the checkbox handlers rely on. Models arrive alphabetized
// from the server, so each section renders in id order without client-side
// sorting.
func TestUIImportModalHasTwoCategories(t *testing.T) {
	ui := readEmbeddedUI(t)
	body := extractFunction(t, ui, "showImportModal")

	if !strings.Contains(body, `models.filter(m => m.isFree)`) {
		t.Error("free category is not derived from the isFree flag")
	}
	if !strings.Contains(body, `models.filter(m => !m.isFree)`) {
		t.Error("regular category is not derived from the isFree flag")
	}
	if !strings.Contains(body, `import-cat`) {
		t.Error("category headers are not rendered")
	}
	if !strings.Contains(body, `Free models (${free.length})`) || !strings.Contains(body, `Regular models (${regular.length})`) {
		t.Error("category headers must label and count both sections")
	}
	// Sections render before and after the shared row builder — the row shape
	// must be untouched so filterImportRows/toggleAllImport keep matching rows.
	if !strings.Contains(body, `class="import-row"`) || !strings.Contains(body, `class="import-cb"`) {
		t.Error("row shape changed; search/select-all would break")
	}
}

// The fetch handler must sort models alphabetically before responding, so
// both categories render in id order regardless of which fetcher produced
// them (registry fetchers, Grok CLI's live catalogue, static fallbacks).
// The sort site lives in the Go source, not the embedded UI.
func TestImportModelsSortedByHandler(t *testing.T) {
	src, err := os.ReadFile("dashboard.go")
	if err != nil {
		t.Fatalf("read dashboard.go: %v", err)
	}
	if !strings.Contains(string(src), "result.Models = sortUpstreamModels(result.Models)") {
		t.Error("HandleImportModels does not sort models before responding; import order would be fetcher-dependent")
	}
}
