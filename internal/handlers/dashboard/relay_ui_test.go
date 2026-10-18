package dashboard

import (
	"strings"
	"testing"
)

// The relay deploy endpoints existed with no UI in front of them, so the only
// way to create a relay pool was to curl the API and then hand-add a pool row.
// This pins the three things that make the UI real: the menu that opens the
// modal, one entry per platform, and each entry pointing at the endpoint that
// platform's handler actually serves.
func TestRelayDeployUIWiredForEveryPlatform(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `id="relay-menu"`) {
		t.Fatal("the Deploy Relay menu is missing, so relay deploy is API-only again")
	}
	if !strings.Contains(ui, `id="relay-modal"`) {
		t.Fatal("the relay deploy modal is missing")
	}

	// Endpoint paths must match the dashboard mounts registered in router.go.
	// These sit behind the session cookie, so the UI can deploy without
	// holding an API key; the same handlers are also mounted at the root under
	// API-key auth for programmatic use.
	for _, want := range []string{
		`"/api/dashboard/proxy-pools/cloudflare-deploy"`,
		`"/api/dashboard/proxy-pools/vercel-deploy"`,
		`"/api/dashboard/proxy-pools/deno-deploy"`,
	} {
		if !strings.Contains(ui, want) {
			t.Errorf("relay platform endpoint %s is not referenced by the UI", want)
		}
	}

	// The form fields are driven by a per-platform table; a platform that loses
	// its credential field would silently deploy with an empty token.
	for _, want := range []string{"accountId", "apiToken", "vercelToken", "denoToken", "orgDomain"} {
		if !strings.Contains(ui, want) {
			t.Errorf("relay form is missing the %q field", want)
		}
	}
}

// The Deploy Relay dropdown is built on native <details>, so the browser owns
// open/close toggle on the <summary> element. Dismissing on clicks outside is
// handled in the capture phase (pointerdown) so that inner widgets calling
// stopPropagation cannot leave the menu hanging open.
func TestRelayDropdownUsesNativeDetails(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, want := range []struct{ name, needle string }{
		{"native details element", `<details class="relay-menu-wrap" id="relay-menu">`},
		{"summary trigger button", `<summary class="btn">Deploy Relay`},
		{"outside click dismisses via pointerdown", `document.addEventListener("pointerdown"`},
		{"tab-away dismisses via focusout", `document.addEventListener("focusout"`},
	} {
		if !strings.Contains(ui, want.needle) {
			t.Errorf("relay dropdown missing native structure: %s", want.name)
		}
	}
}
