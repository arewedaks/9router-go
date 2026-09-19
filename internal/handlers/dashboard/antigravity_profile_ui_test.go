package dashboard

import (
	"strings"
	"testing"
)

// The Antigravity client profile is a provider-wide setting, not a per-account
// one. These tests pin that: a single dropdown beside the provider title, and no
// per-account control left behind. Both halves matter — leaving the per-account
// select in place would let the UI write a value the router never reads, so the
// operator would see the old identity while requests used the new one.

func TestAntigravityProfileDropdownRendersInProviderHero(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `antigravityProfileValue()`) {
		t.Error("the profile dropdown no longer reads the shared profile value")
	}
	if !strings.Contains(ui, `setAntigravityProfile(this.value, this)`) {
		t.Error("the profile dropdown no longer wired to the global setter")
	}
	// It must be the provider-wide control, sized for the title row.
	if !strings.Contains(ui, "profile-select profile-select-global") {
		t.Error("the dropdown is missing its global styling class")
	}
	// Sits next to the title, inside the same hero header.
	if !strings.Contains(ui, `<h2 class="section-title" style="margin:0;">${escapeHtml(d.displayName || d.provider)}</h2>`) {
		t.Error("provider title markup changed; the dropdown may no longer sit beside it")
	}
}

func TestAntigravityProfileHasNoPerAccountControl(t *testing.T) {
	ui := readEmbeddedUI(t)

	if strings.Contains(ui, "oauth-profile") {
		t.Error("the OAuth form still asks for a per-account profile")
	}
	if strings.Contains(ui, "setClientProfile(") {
		t.Error("the per-connection profile setter is still present")
	}
	if strings.Contains(ui, "profileSel") {
		t.Error("the per-account profile select is still rendered in the account list")
	}
}

func TestAntigravityProfileSetterCallsGlobalEndpoint(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `/api/dashboard/settings/antigravity-profile`) {
		t.Error("the UI no longer posts to the provider-wide profile endpoint")
	}
	if strings.Contains(ui, `/client-profile`) {
		t.Error("the UI still posts to the removed per-connection endpoint")
	}
}

// The profile must be loaded in openProviderDetail, NOT from inside
// renderProviderDetail. Loading it during render re-entered render (load →
// resolve → re-render), which replaced the <select> hundreds of times a second;
// clicks never landed on a stable element, so the dropdown appeared dead.
func TestAntigravityProfileLoadedOutsideRenderLoop(t *testing.T) {
	ui := readEmbeddedUI(t)

	// renderProviderDetail must not fetch-and-re-render.
	start := strings.Index(ui, "function renderProviderDetail(d) {")
	if start < 0 {
		t.Fatal("renderProviderDetail not found")
	}
	end := strings.Index(ui[start:], "\n  function ")
	if end < 0 {
		end = len(ui) - start
	}
	body := ui[start : start+end]
	if strings.Contains(body, "loadAntigravityProfile") {
		t.Error("renderProviderDetail still loads the profile; this re-renders from render and loops")
	}
	if strings.Contains(body, "renderProviderDetailRefresh()") {
		t.Error("renderProviderDetail still re-renders itself")
	}

	// openProviderDetail is the correct, single load point.
	ostart := strings.Index(ui, "async function openProviderDetail(")
	if ostart < 0 {
		t.Fatal("openProviderDetail not found")
	}
	oend := strings.Index(ui[ostart:], "\n  function ")
	if oend < 0 {
		oend = len(ui) - ostart
	}
	if !strings.Contains(ui[ostart:ostart+oend], "loadAntigravityProfile") {
		t.Error("openProviderDetail no longer loads the profile before rendering")
	}
}
