package dashboard

import (
	"regexp"
	"strings"
	"testing"
)

// Deleting one account must remove only that account.
//
// deleteProvider used to call loadProviders(), which refetches every provider
// and rebuilds the whole grid: the page flashed, the scroll position and any
// expanded card were lost, and the operator was dropped out of the context they
// were working in — for a change that touched a single row. The list is already
// in memory, so the row can be dropped locally and the view repainted.
func TestDeleteAccountDoesNotRefetchProviderList(t *testing.T) {
	ui := readEmbeddedUI(t)

	body := extractFunc(t, ui, "async function deleteProvider(id)")
	if strings.Contains(body, "loadProviders()") {
		t.Error("deleteProvider still calls loadProviders(): deleting one account refetches every provider and rebuilds the page")
	}
	if !strings.Contains(body, "dropProviderLocally(id)") {
		t.Error("deleteProvider should drop the row from the in-memory list")
	}

	helper := extractFunc(t, ui, "function dropProviderLocally(id)")
	if !strings.Contains(helper, "allProviders = allProviders.filter") {
		t.Error("dropProviderLocally must filter the cached list, not refetch it")
	}
	// A network call here would defeat the whole point of the change.
	if regexp.MustCompile(`\b(await\s+api\(|fetch\()`).MatchString(helper) {
		t.Error("dropProviderLocally must not perform a network request")
	}
	// Removing the last account of a provider still has to repaint, so the card
	// disappears instead of lingering with a stale count.
	if !strings.Contains(helper, "filterProviders()") {
		t.Error("dropProviderLocally must repaint the grid after the local removal")
	}
}

// extractFunc pulls a function body out of the embedded UI. It matches from the
// signature to the first line that closes the function at the same indent.
func extractFunc(t *testing.T, src, sig string) string {
	t.Helper()
	start := strings.Index(src, sig)
	if start < 0 {
		t.Fatalf("function not found in embedded UI: %s", sig)
	}
	rest := src[start:]
	// Functions in this file are two-space indented; the body ends at a line
	// that is exactly "  }".
	if end := strings.Index(rest, "\n  }"); end > 0 {
		return rest[:end]
	}
	t.Fatalf("could not find end of function: %s", sig)
	return ""
}
