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
	if !strings.Contains(fn, "proxy-badge") {
		t.Error("each row needs its own badge slot for the test result")
	}
	if !strings.Contains(ui, `id="proxy-edit-rows"`) {
		t.Error("the details modal needs the per-row proxy editor")
	}
	if strings.Contains(ui, `id="proxy-edit-results"`) {
		t.Error("the detached results list must be gone")
	}
}

// The entry list must read as a plain list, not a stack of form fields: a row
// shows text until asked to edit, and only then becomes an input.
func TestProxyRowsReadAsPlainList(t *testing.T) {
	ui := readEmbeddedUI(t)
	render := extractFunction(t, ui, "renderProxyRows")
	if render == "" {
		t.Fatal("UI lacks renderProxyRows")
	}
	if !strings.Contains(render, `class="proxy-row-text"`) {
		t.Error("rows must render their URL as text by default")
	}
	if strings.Contains(render, `type="text"`) {
		t.Error("rows must not all render as inputs — that is the spacing problem")
	}

	edit := extractFunction(t, ui, "editProxyRow")
	if edit == "" {
		t.Fatal("UI lacks editProxyRow")
	}
	if !strings.Contains(edit, "createElement(\"input\")") {
		t.Error("Edit must swap the row into an input")
	}
	if !strings.Contains(render, "editProxyRow(") {
		t.Error("each row needs an Edit button")
	}

	// Editing must not leave a verdict that describes the old value.
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
