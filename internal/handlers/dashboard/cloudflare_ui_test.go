package dashboard

import (
	"strings"
	"testing"
)

// The Cloudflare card must exist in the Settings tab with both switches, and
// must bind them to the settings API rather than to a read-only display.
func TestCloudflareSettingsCardIsWired(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, want := range []string{
		`id="set-trust-proxy"`,
		`id="set-cookie-secure"`,
		// The onclick attribute, not merely the definition: a function that
		// exists but is not bound to a button is dead code.
		`onclick="applyCloudflarePreset()"`,
		`onclick="showCloudflaredConfig()"`,
		"async function applyCloudflarePreset()",
		"async function showCloudflaredConfig()",
		"trustProxy:",
		"authCookieSecure:",
	} {
		if !strings.Contains(ui, want) {
			t.Errorf("Cloudflare settings UI is missing %s", want)
		}
	}

	if !strings.Contains(ui, "Cloudflare / Reverse Proxy") {
		t.Error("the settings tab has no Cloudflare section")
	}

	// The card must not claim the bind address can be changed from the browser:
	// the listening socket is fixed at process start.
	if strings.Contains(ui, `"listenHost":`) && strings.Contains(ui, "onclick=\"setListenHost") {
		t.Error("the UI offers a runtime control for the bind address, which is impossible")
	}
	// And the operator must be told a restart is needed when it is exposed.
	if !strings.Contains(ui, "needs a restart") && !strings.Contains(ui, "Restart with") {
		t.Error("the UI never tells the operator the bind address needs a restart")
	}
}
