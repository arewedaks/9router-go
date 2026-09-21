package dashboard

import (
	"strings"
	"testing"
)

// The rail opens with the macOS window ornament, matching the VansRouter
// sidebar. It sits above the brand lockup.
func TestSidebarHasMacOrnamentAboveBrand(t *testing.T) {
	ui := readEmbeddedUI(t)

	lights := strings.Index(ui, `class="traffic-lights"`)
	if lights < 0 {
		t.Fatal("no traffic-lights ornament in the UI")
	}
	brand := strings.Index(ui, `class="sidebar-brand"`)
	if brand < 0 {
		t.Fatal("no sidebar brand block")
	}
	if lights > brand {
		t.Error("the ornament must render above the brand lockup, not below it")
	}

	// All three dots, with the real system colours.
	block := ui[lights:brand]
	for _, dot := range []string{"tl-close", "tl-min", "tl-zoom"} {
		if !strings.Contains(block, dot) {
			t.Errorf("the ornament is missing the %q dot", dot)
		}
	}
	// The colours are declared in the stylesheet, not on the elements.
	for _, hex := range []string{"#ff5f56", "#ffbd2e", "#27c93f"} {
		if !strings.Contains(ui, hex) {
			t.Errorf("the ornament is missing the system colour %s", hex)
		}
	}

	// Purely decorative: it must not become a keyboard or screen-reader target.
	if !strings.Contains(block, `aria-hidden="true"`) {
		t.Error("the ornament is exposed to assistive tech; it is decoration")
	}
}

// The sidebar brand is a lockup: the mark, then "9Router", then the running
// version on the line below. The version belongs under the name rather than in
// a separate badge, so it reads as part of the identity.
func TestSidebarBrandIsMarkAndWordmark(t *testing.T) {
	ui := readEmbeddedUI(t)

	start := strings.Index(ui, `class="sidebar-brand"`)
	if start < 0 {
		t.Fatal("no sidebar brand block")
	}
	block := ui[start:]
	if end := strings.Index(block, `class="update-pill"`); end > 0 {
		block = block[:end]
	}

	for _, want := range []string{"brand-mark", "brand-name", "brand-version"} {
		if !strings.Contains(block, want) {
			t.Errorf("the sidebar brand is missing %q", want)
		}
	}
	if !strings.Contains(block, "9Router") {
		t.Error("the wordmark text is gone from the brand lockup")
	}
	// The GO marker trails the wordmark on the same line.
	if !strings.Contains(block, `class="brand-go"`) {
		t.Error("the GO marker is missing from the brand lockup")
	}
	name := strings.Index(block, "brand-name")
	goMark := strings.Index(block, `class="brand-go"`)
	if name < 0 || goMark < 0 || goMark < name {
		t.Error("the GO marker must sit inside the wordmark, after the name")
	}

	// The version sits in the text column, not as a badge, so it is styled with
	// the muted version rule rather than the Go badge colours.
	at := strings.Index(block, `id="brand-version"`)
	lineStart := strings.LastIndex(block[:at], "<")
	if lineStart >= 0 {
		tag := block[lineStart:at]
		if strings.Contains(tag, "badge") {
			t.Errorf("the version is a badge again instead of a line under the "+
				"wordmark: %q", tag)
		}
		if !strings.Contains(tag, "brand-version") {
			t.Errorf("the version line lost its styling hook: %q", tag)
		}
	}

	// Order matters: mark, name (with GO), then version.
	mark := strings.Index(block, "brand-mark")
	ver := strings.Index(block, `id="brand-version"`)
	if !(mark < name && name < ver) {
		t.Errorf("brand lockup is out of order: mark=%d name=%d version=%d", mark, name, ver)
	}
}

// The version is rendered from the update check and shown as a plain "v" prefix,
// because the wordmark above it already says what the product is.
func TestBrandVersionRendersBareVPrefix(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `el.textContent = "v" + v`) {
		t.Error(`renderVersion does not write a bare "v" prefix into the version ` +
			`line, so it would double up with the wordmark`)
	}
	if !strings.Contains(ui, "document.title = ") {
		t.Error("the UI never sets document.title, so the tab title shows no version")
	}
}

// The endpoint page must expose a real API-key switch wired to the shared
// settings key, not the static "Always on" text it used to show. VansRouter
// writes the same key, so this is what makes the setting portable between the
// two dashboards on one database.
func TestEndpointSecurityHasRealAPIKeyToggle(t *testing.T) {
	ui := readEmbeddedUI(t)

	toggle := strings.Index(ui, `id="require-apikey-toggle"`)
	if toggle < 0 {
		t.Fatal("the endpoint page has no API-key toggle")
	}
	if !strings.Contains(ui, `onchange="saveRequireApiKey(this.checked)"`) {
		t.Error("the toggle is not wired to its save handler")
	}
	if !strings.Contains(ui, "function saveRequireApiKey(") {
		t.Error("saveRequireApiKey is missing, so the toggle cannot persist")
	}
	// The field name has to match VansRouter's for a shared row to work.
	if !strings.Contains(ui, "requireApiKey: required") {
		t.Error(`the save payload does not use VansRouter's "requireApiKey" key`)
	}
	if !strings.Contains(ui, `id="require-apikey-warn"`) {
		t.Error("no warning element for the open-proxy state")
	}

	// The old claim must be gone: it said no switch exists, which is now false.
	if strings.Contains(ui, "does not expose a switch to turn it off") {
		t.Error("the stale \"no switch\" copy is still present")
	}
}

// Turning the key off must not be the same as publishing the proxy. The second
// switch, matching VansRouter's allowRemoteNoApiKey, is what widens access past
// loopback, and it must write that exact key so the two dashboards agree.
func TestRemoteAccessIsASeparateSecondSwitch(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `id="allow-remote-toggle"`) {
		t.Fatal("the endpoint page has no remote-access switch")
	}
	if !strings.Contains(ui, `onchange="saveAllowRemoteNoApiKey(this.checked)"`) {
		t.Error("the remote switch is not wired to its save handler")
	}
	if !strings.Contains(ui, "function saveAllowRemoteNoApiKey(") {
		t.Error("saveAllowRemoteNoApiKey is missing, so the switch cannot persist")
	}
	if !strings.Contains(ui, "allowRemoteNoApiKey: allow") {
		t.Error(`the payload does not use VansRouter's "allowRemoteNoApiKey" key`)
	}
	// The switch is inert unless the main requirement is off, so it starts hidden
	// and the render function is what reveals it.
	if !strings.Contains(ui, `id="allow-remote-row" style="display:none;"`) {
		t.Error("the remote row must start hidden until requireApiKey is off")
	}
	if !strings.Contains(ui, `id="allow-remote-warn"`) {
		t.Error("no warning element for the fully-open state")
	}
}
