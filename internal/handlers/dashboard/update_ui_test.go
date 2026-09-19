package dashboard

import (
	"strings"
	"testing"
)

// The update button lives under the logo and must start hidden: showing it when
// there is nothing to install trains the operator to ignore it.
func TestUpdateButtonSitsUnderBrandAndStartsHidden(t *testing.T) {
	ui := readEmbeddedUI(t)

	brandIdx := strings.Index(ui, `class="sidebar-brand"`)
	btnIdx := strings.Index(ui, `id="nav-update"`)
	if brandIdx < 0 || btnIdx < 0 {
		t.Fatal("the sidebar has no update button")
	}
	if btnIdx < brandIdx {
		t.Error("the update button is not below the brand mark")
	}
	// Look only at the button's own tag: later attributes elsewhere in the file
	// must not satisfy this.
	end := strings.Index(ui[btnIdx:], ">")
	if end < 0 {
		t.Fatal("malformed update button tag")
	}
	if !strings.Contains(ui[btnIdx:btnIdx+end], "hidden") {
		t.Error("the update button is visible by default, so it shows even when " +
			"the build is current")
	}
	if !strings.Contains(ui[btnIdx:btnIdx+end], `onclick="promptUpdate()"`) {
		t.Error("the update button is not wired to promptUpdate()")
	}
}

// The dialog must reach all three endpoints and must ask for the password,
// because the server refuses to apply without it.
func TestUpdateDialogIsWiredToEndpoints(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, want := range []string{
		`id="update-modal"`,
		`id="update-pw"`,
		`"/api/dashboard/update/status"`,
		`"/api/dashboard/update/check"`,
		`"/api/dashboard/update/apply"`,
		`"x-9r-password": pw`,
	} {
		if !strings.Contains(ui, want) {
			t.Errorf("update UI is missing %s", want)
		}
	}
}

// The button appears from the background check, so polling must be started once
// the operator is authenticated — not on page load, where the request would be
// rejected and the button would never appear at all.
func TestUpdatePollingStartsAfterAuth(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "startUpdatePolling();") {
		t.Fatal("nothing starts the update polling, so the button never appears")
	}
	if !strings.Contains(ui, "function startUpdatePolling()") {
		t.Fatal("startUpdatePolling is called but not defined")
	}
	if !strings.Contains(ui, "async function checkAuth()") {
		t.Fatal("checkAuth is missing")
	}
	// Every successful auth path must reach it.
	authIdx := strings.Index(ui, "async function checkAuth()")
	tail := ui[authIdx:]
	if !strings.Contains(tail, "startUpdatePolling();") {
		t.Error("checkAuth never starts polling")
	}
}

// A refused (untrusted) release is not an available update, and must be shown as
// refused rather than hidden. Hiding it would display "up to date" after the
// server actually declined an update — a lie the operator cannot debug.
func TestUntrustedReleaseIsSurfacedNotHidden(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "untrustedSource") {
		t.Fatal("the UI never reads untrustedSource, so a refused update looks like 'up to date'")
	}
	idx := strings.Index(ui, "function renderUpdateButton()")
	if idx < 0 {
		t.Fatal("renderUpdateButton is missing")
	}
	// The untrusted branch must come before the hasUpdate check, otherwise a
	// refused release (hasUpdate=false) hits the hidden path first.
	untrusted := strings.Index(ui[idx:], "untrustedSource")
	hasUpdate := strings.Index(ui[idx:], "if (!st.hasUpdate)")
	if untrusted < 0 || hasUpdate < 0 {
		t.Fatal("renderUpdateButton does not branch on both states")
	}
	if untrusted > hasUpdate {
		t.Error("the untrusted check runs after the hasUpdate check, so a refused " +
			"release is hidden and reported as up to date")
	}
}
