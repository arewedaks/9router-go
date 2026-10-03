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

	// And it must run for EVERY provider detail, not only no-auth ones: the
	// Round Robin toggle lives on connected providers and reads the same map,
	// so gating the load on noConnection left it rendering as off after a
	// refresh. Both call sites are asserted, and neither may sit inside the
	// no-auth branch.
	if !strings.Contains(html, "await loadProviderStrategies();\n      // The Proxy tab also needs the pool list") {
		t.Error("provider detail does not load strategies unconditionally; the Round Robin toggle will render as off")
	}
	if strings.Contains(html, "if (d.noConnection) {\n          await loadProxyPools(false);\n          await loadProviderStrategies();") {
		t.Error("strategies are still loaded only for no-auth providers; connected providers lose the toggle state")
	}

	// The Round Robin control and the proxy pool picker must both read the map
	// the loader fills, otherwise loading it has no effect on either.
	for _, fn := range []string{"accountRoundRobinControls", "renderNoAuthProxyCard"} {
		idx := strings.Index(html, "function "+fn+"(d)")
		if idx == -1 {
			t.Fatalf("%s() not found", fn)
		}
		body := html[idx:]
		if end := strings.Index(body, "\n  }"); end != -1 {
			body = body[:end]
		}
		if !strings.Contains(body, "providerStrategies[d.provider]") {
			t.Errorf("%s() does not read providerStrategies[d.provider]", fn)
		}
	}
}
