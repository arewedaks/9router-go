package dashboard

import (
	"os"
	"strings"
	"testing"
)

// A single-entry test is diagnostic: it must report one URL's health without
// touching the pool's active flag, or testing one dead proxy would take the
// whole pool offline while the other entries still work.
func TestProxyPoolSingleURLTestDoesNotFlipPoolState(t *testing.T) {
	src, err := os.ReadFile("proxy_pool_admin_handler.go")
	if err != nil {
		t.Fatalf("read handler: %v", err)
	}
	body := string(src)

	idx := strings.Index(body, `if target := strings.TrimSpace(payload.URL); target != "" {`)
	if idx == -1 {
		t.Fatal("HandleTestProxyPool must branch on a per-URL payload")
	}
	// The branch ends at the next top-level return, which is where the pool-wide
	// path (and its recordAndRespond call) resumes.
	arm := body[idx:]
	if end := strings.Index(arm, "\n\ttarget := firstProxyURL(pool)"); end != -1 {
		arm = arm[:end]
	}
	if strings.Contains(arm, "recordAndRespond") {
		t.Error("a single-URL test must not record a pool-wide status")
	}
	if !strings.Contains(arm, "probeProxy") {
		t.Error("a single-URL test must still run the CONNECT probe")
	}
}

// The details modal must offer a per-entry test that sends the URL, since the
// pool-level Test button only ever probes the first entry.
func TestProxyDetailsOffersPerURLTest(t *testing.T) {
	ui := readEmbeddedUI(t)
	fn := extractFunction(t, ui, "testProxyEditList")
	if fn == "" {
		t.Fatal("UI lacks testProxyEditList for the pool details modal")
	}
	if !strings.Contains(fn, "/test") {
		t.Error("per-URL test must call the pool test endpoint")
	}
	if !strings.Contains(fn, "url: urls[i]") {
		t.Error("per-URL test must send the specific URL being tested")
	}
	if !strings.Contains(ui, `id="proxy-edit-test"`) {
		t.Error("the details modal needs a Test all button")
	}
	// The verdict must land on the row it describes. Rendering it into a
	// separate list is what made the old modal hard to read: row N of the
	// results had to be matched to line N of the textarea by eye.
	if !strings.Contains(fn, "ppo-badge") {
		t.Error("each row needs its own badge slot for the test result")
	}
	if !strings.Contains(ui, `id="proxy-edit-rows"`) {
		t.Error("the details modal needs the per-row proxy editor")
	}
	if strings.Contains(ui, `id="proxy-edit-results"`) {
		t.Error("the detached results list must be gone")
	}
}

// The entry rows must not reuse the settings-card .proxy-row class. Both used
// to share the name, so the settings rule .proxy-row + .proxy-row (18px margin
// plus a divider) was applied to every pool entry: the list rendered with a
// wide gap between rows and the numbers pushed away from their URLs.
func TestProxyEntryRowsDoNotCollideWithSettingsCards(t *testing.T) {
	ui := readEmbeddedUI(t)

	render := extractFunction(t, ui, "renderProxyRows")
	if render == "" {
		t.Fatal("UI lacks renderProxyRows")
	}
	if strings.Contains(render, `class="proxy-row"`) {
		t.Error("entry rows must not reuse the settings-card .proxy-row class")
	}
	if !strings.Contains(render, `class="ppo-row"`) {
		t.Error("entry rows must use their own ppo-row class")
	}

	// The settings cards keep their own spacing rule; the entry rows must not
	// inherit it, or the list goes airy again.
	if !strings.Contains(ui, ".proxy-row + .proxy-row") {
		t.Log("note: the settings-card spacing rule is gone; the collision risk is moot")
	}
	if strings.Contains(ui, ".ppo-row + .ppo-row") {
		t.Error("entry rows must not carry the settings-card sibling rule")
	}
}

// The entry list must read as a compact card list matching the combo model
// editor: a row shows text until asked to edit, with the combo button set.
func TestProxyRowsMatchComboCardStyle(t *testing.T) {
	ui := readEmbeddedUI(t)
	render := extractFunction(t, ui, "renderProxyRows")
	if render == "" {
		t.Fatal("UI lacks renderProxyRows")
	}
	if !strings.Contains(render, `class="ppo-text"`) {
		t.Error("rows must render their URL as text by default")
	}
	if strings.Contains(render, `type="text"`) {
		t.Error("rows must not all render as inputs — that is the spacing problem")
	}
	// Same button vocabulary as the combo model rows, so both editors match.
	if !strings.Contains(render, `class="btn btn-sm btn-danger"`) {
		t.Error("the remove button must use the combo list's btn-danger style")
	}
	if !strings.Contains(render, `ppo-idx`) {
		t.Error("rows must be numbered like the combo model rows")
	}

	edit := extractFunction(t, ui, "editProxyRow")
	if edit == "" {
		t.Fatal("UI lacks editProxyRow")
	}
	if !strings.Contains(edit, "createElement(\"input\")") {
		t.Error("Edit must swap the row into an input")
	}

	// Editing must not leave a verdict that describes the old value, and
	// clearing a row must remove it rather than save a blank entry.
	commit := extractFunction(t, ui, "commitOnBlur")
	if commit == "" {
		t.Fatal("UI lacks commitOnBlur")
	}
	if !strings.Contains(commit, `badge.textContent = ""`) {
		t.Error("editing a row must clear its stale test verdict")
	}
	if !strings.Contains(commit, "row.remove()") {
		t.Error("clearing a row must remove it, not save a blank entry")
	}

	// Adding must append, never rebuild: a rebuild would drop in-progress edits.
	add := extractFunction(t, ui, "addProxyRow")
	if add == "" {
		t.Fatal("UI lacks addProxyRow")
	}
	if strings.Contains(add, "renderProxyRows(") {
		t.Error("Add proxy must append a row, not re-render the whole list")
	}

	// The row style itself must match the combo card, not a bordered box.
	if !strings.Contains(ui, ".ppo-row {") {
		t.Error("proxy rows must keep their own ppo-row style")
	}
	// The class must not collide with the settings-card .ppo-row, whose
	// .ppo-row + .ppo-row rule added 18px margin and a divider to every
	// entry — that collision is what made the list look so airy.
	if !strings.Contains(ui, ".ppo-row + .ppo-row") && !strings.Contains(ui, "gap: 3px") {
		t.Error("entry rows must be spaced by the list gap alone")
	}
	if strings.Contains(ui, ".ppo-rows-foot") {
		t.Error("proxy rows must use the combo card background and border")
	}
}

// Saving must read the rows back into the newline-joined list the API takes,
// and blank rows must not become empty proxy entries.
func TestProxyEditSavesFromRows(t *testing.T) {
	ui := readEmbeddedUI(t)
	submit := extractFunction(t, ui, "submitProxyEdit")
	if !strings.Contains(submit, "proxyEditURLs()") {
		t.Error("submitProxyEdit must read the URL list from the row editor")
	}
	reader := extractFunction(t, ui, "proxyEditURLs")
	if reader == "" {
		t.Fatal("UI lacks proxyEditURLs")
	}
	if !strings.Contains(reader, ".filter(Boolean)") {
		t.Error("blank rows must be dropped before saving")
	}
}
