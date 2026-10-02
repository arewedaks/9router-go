package dashboard

import (
	"os"
	"strings"
	"testing"
)

// TestProviderTabLoadsStrategies pins the fix for a provider's proxy binding
// appearing to reset to "direct" after a refresh. The binding was always
// persisted; the provider tab simply never fetched settings.providerStrategies,
// so renderNoAuthProxyCard read an empty map and selected "None (direct)".
func TestProviderTabLoadsStrategies(t *testing.T) {
	raw, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatalf("read ui/index.html: %v", err)
	}
	html := string(raw)

	// The loader must exist and assign the map the renderer reads.
	if !strings.Contains(html, "async function loadProviderStrategies()") {
		t.Fatal("loadProviderStrategies() is missing; the provider tab has no way to load saved bindings")
	}
	idx := strings.Index(html, "async function loadProviderStrategies()")
	body := html[idx:]
	if end := strings.Index(body, "\n  }"); end != -1 {
		body = body[:end]
	}
	if !strings.Contains(body, "providerStrategies = s.providerStrategies || {}") {
		t.Error("loadProviderStrategies() does not assign providerStrategies")
	}

	// It must run on the providers tab, not only on the settings tab.
	if !strings.Contains(html, `if (tabId === "providers") { loadProviders(); loadProviderStrategies(); }`) {
		t.Error("providers tab does not call loadProviderStrategies(); the binding will render as direct on a fresh load")
	}

	// And before the no-auth detail render, so the first paint is already correct.
	detail := strings.Index(html, "if (d.noConnection) {")
	if detail == -1 {
		t.Fatal("no-auth detail branch not found")
	}
	branch := html[detail:]
	if end := strings.Index(branch, "\n      }"); end != -1 {
		branch = branch[:end]
	}
	if !strings.Contains(branch, "await loadProviderStrategies();") {
		t.Error("provider detail renders before loading strategies; first paint shows the wrong pool")
	}
}
