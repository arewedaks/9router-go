package dashboard

import (
	"strings"
	"testing"
)

// The Console tab must surface how many model ids /v1/models currently
// serves: a count strip under the Resources card, fetched once when the tab
// opens (the list only changes on import/hide actions) and refreshable via a
// button — not polled on the resource timer, which would rebuild the whole
// catalogue every 5 seconds for a number nobody is watching.
func TestUIConsoleShowsModelCount(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `id="res-model-count"`) {
		t.Error("Console tab has no /v1/models count element")
	}
	if !strings.Contains(ui, `refreshModelCountTile()`) {
		t.Error("model count strip is not wired to a refresh function")
	}
	fn := extractFunction(t, ui, "refreshModelCountTile")
	if !strings.Contains(fn, `/v1/models`) {
		t.Error("refreshModelCountTile does not read the /v1/models endpoint")
	}
	// Both spellings count: the endpoint returns {data:[...], models:[...]}.
	if !strings.Contains(fn, `data.data`) || !strings.Contains(fn, `data.models`) {
		t.Error("count must handle both the data and models response fields")
	}
	// Tab open triggers the initial fetch.
	start := extractFunction(t, ui, "startConsoleLogs")
	if !strings.Contains(start, "refreshModelCountTile()") {
		t.Error("opening the Console tab does not fetch the model count")
	}
}
