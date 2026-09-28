package dashboard

import (
	"os"
	"strings"
	"testing"
)

// A refused delete must name the blocking providers (so the UI can offer a
// concrete "Unbind & delete"), and the UI flow must clear each provider's
// strategy pool via the __none__ sentinel — the server drops empty strategy
// entries, so the unbind leaves no residue — before retrying the delete.
func TestProxyPoolDeleteNamesBoundProviders(t *testing.T) {
	src, err := os.ReadFile("proxy_pool_admin_handler.go")
	if err != nil {
		t.Fatalf("read handler: %v", err)
	}
	if !strings.Contains(string(src), `"boundProviders":       boundProviders`) {
		t.Error("409 response does not carry the bound provider ids")
	}
	ui := readEmbeddedUI(t)
	fn := extractFunction(t, ui, "deleteProxyPool")
	if !strings.Contains(fn, `data.boundProviders`) {
		t.Error("UI does not read boundProviders from the 409 response")
	}
	if !strings.Contains(fn, `proxyPoolId: "__none__"`) {
		t.Error("UI unbind must use the __none__ sentinel the server normalizes")
	}
	if !strings.Contains(fn, `/api/dashboard/settings`) {
		t.Error("UI unbind must hit the settings endpoint")
	}
	dbSrc, err := os.ReadFile("../../db/proxyPools_admin.go")
	if err != nil {
		t.Fatalf("read repo: %v", err)
	}
	if !strings.Contains(string(dbSrc), "func (r *Repo) ProxyPoolBoundProviders") {
		t.Error("repo lacks ProxyPoolBoundProviders")
	}
}
