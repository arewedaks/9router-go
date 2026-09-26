package dashboard

import (
	"strings"
	"testing"
)

// The proxy card must expose a way to import a list. Before this, the only way
// to populate a pool was the DB (or the Next.js dashboard), so an operator with
// a vendor's 100-proxy export had nowhere to paste it.
func TestUIProxyCardOffersListImport(t *testing.T) {
	ui := readEmbeddedUI(t)

	card := extractFunction(t, ui, "renderNoAuthProxyCard")
	for _, want := range []string{"proxy-import-list", "importProxyList("} {
		if !strings.Contains(card, want) {
			t.Errorf("proxy card is missing %q", want)
		}
	}

	// The import must be offered for keyless providers specifically: they own no
	// connection row, so this card is their only configurable surface.
	if !strings.Contains(card, "proxy-import-assign") {
		t.Error("proxy card does not offer attaching the new pool to this provider")
	}
}

func TestUIImportProxyListPostsToPoolsEndpoint(t *testing.T) {
	ui := readEmbeddedUI(t)

	body := extractFunction(t, ui, "importProxyList")

	if !strings.Contains(body, `/api/dashboard/proxy-pools`) {
		t.Fatal("importProxyList does not POST to the proxy-pools endpoint")
	}
	if !strings.Contains(body, "method: \"POST\"") {
		t.Error("importProxyList does not use POST")
	}
	// Attaching in the same request is the point: a pool nobody targets routes
	// nothing, and a strategy pointing at a missing pool silently falls back to
	// the host IP.
	if !strings.Contains(body, "provider:") {
		t.Error("importProxyList does not send the provider to attach the pool to")
	}
	// The pool list must be refetched, or the new pool never appears in the
	// dropdown and the operator's next save picks a stale value.
	if !strings.Contains(body, "loadProxyPools(true)") {
		t.Error("importProxyList does not refresh the pool cache after importing")
	}
	if !strings.Contains(body, "renderProviderDetailRefresh()") {
		t.Error("importProxyList must repaint in place, not refetch the page")
	}
	// Skipped lines are reported rather than hidden: silently dropping input is
	// how an operator ends up with fewer proxies than they pasted.
	if !strings.Contains(body, "data.skipped") {
		t.Error("importProxyList does not report skipped entries")
	}
}
