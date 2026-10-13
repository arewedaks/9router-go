package dashboard

import (
	"strings"
	"testing"
)

// TestUIProviderBulkDeleteAccounts ensures that the provider detail view
// has multi-select checkboxes, a select-all toggle, and a bulk delete handler.
func TestUIProviderBulkDeleteAccounts(t *testing.T) {
	ui := readEmbeddedUI(t)

	mustContain := []string{
		`conn-select-cb`,
		`conn-select-all`,
		`conn-bulk-delete-btn`,
		`deleteSelectedConns()`,
		`toggleSelectAllConns(`,
		`updateConnSelectionUI()`,
	}
	for _, item := range mustContain {
		if !strings.Contains(ui, item) {
			t.Errorf("expected embedded UI to contain %q, but missing", item)
		}
	}
}
