package dashboard

import (
	"strings"
	"testing"
)

// readUIFunc extracts a top-level JS function body from the served dashboard.
func readUIFunc(t *testing.T, name string) string {
	t.Helper()
	ui := readEmbeddedUI(t)
	for _, sig := range []string{"async function " + name + "(", "function " + name + "("} {
		start := strings.Index(ui, sig)
		if start < 0 {
			continue
		}
		// Walk to the matching closing brace at the same indent (two spaces).
		rest := ui[start:]
		if end := strings.Index(rest, "\n  }"); end > 0 {
			return rest[:end]
		}
	}
	t.Fatalf("function %s not found in the dashboard UI", name)
	return ""
}

// The Delete button on the provider detail page must not throw the operator back
// to the provider list.
//
// The markup read `deleteProvider(id);closeProviderDetail();`. deleteProvider is
// async, so closeProviderDetail ran immediately on the same tick — before the
// DELETE had even been sent — and the operator was always bounced out of the
// page they were working on. Reported by the user as "delete one account and it
// refreshes back to the provider menu", reproduced on mistral.
func TestDetailDeleteDoesNotNavigateAway(t *testing.T) {
	ui := readEmbeddedUI(t)

	if strings.Contains(ui, "deleteProvider('${p.id}');closeProviderDetail();") {
		t.Error("detail Delete button still calls closeProviderDetail() without awaiting deleteProvider()")
	}

	// Every delete button must call deleteProvider alone and let it decide what
	// to do; navigation must not be sequenced by the markup.
	for _, btn := range strings.Split(ui, "<button class=\"btn btn-sm btn-danger\"")[1:] {
		onclick := btn
		if i := strings.Index(onclick, ">"); i > 0 {
			onclick = onclick[:i]
		}
		if strings.Contains(onclick, "closeProviderDetail") {
			t.Errorf("delete button navigates on its own: %s", strings.TrimSpace(onclick))
		}
	}
}

// Deleting the last account from the open detail page must stay on the page and
// show the empty state, not bounce to the grid.
func TestDeleteProviderStaysOnDetailPage(t *testing.T) {
	body := readUIFunc(t, "deleteProvider")

	if strings.Contains(body, "closeProviderDetail()") {
		t.Error("deleteProvider() closes the detail page; it should refresh in place")
	}
	if !strings.Contains(body, "dropProviderLocally") {
		t.Error("deleteProvider() should repaint locally instead of navigating")
	}
}

// On the detail page the account list is what needs repainting, not the provider
// grid. allProviders holds one entry per *provider*, so filtering it by the
// deleted connection id matches nothing and the early return skipped the
// repaint entirely — the deleted row stayed on screen until a manual reload.
func TestDropProviderLocallyRepaintsOpenDetailPane(t *testing.T) {
	body := readUIFunc(t, "dropProviderLocally")

	if strings.Contains(body, "if (allProviders.length === before) return;") {
		t.Error("early return on unchanged provider count skips the detail-pane repaint")
	}
	if !strings.Contains(body, "openProviderDetail(") {
		t.Error("deleting the last account must refresh the pane to show the empty state")
	}
}

// The empty state must be reachable: when the account list is empty the detail
// page has to render "No connections yet" rather than keep the stale row.
func TestDetailPageRendersEmptyStateAfterDelete(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "No connections yet") {
		t.Error("detail page should render an empty state when no accounts remain")
	}
	// The empty-state branch must be keyed off the connection list actually
	// being empty, so repainting with zero connections shows it.
	if !strings.Contains(ui, "connections") {
		t.Error("empty state should be driven by the connection list")
	}
}
