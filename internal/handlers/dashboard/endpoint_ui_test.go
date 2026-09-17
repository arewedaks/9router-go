package dashboard

import (
	"strings"
	"testing"
)

// TestUIEndpointTabReplacesOverview pins the navigation swap. The Overview tab
// was almost entirely a duplicate of Providers/Combos/Keys plus a single curl
// snippet, so it was replaced by an Endpoint & Key page modelled on the
// upstream /dashboard/endpoint route.
func TestUIEndpointTabReplacesOverview(t *testing.T) {
	ui := readEmbeddedUI(t)

	// The new tab exists, is reachable, and is the default landing view.
	for _, need := range []string{
		`id="nav-endpoint"`,
		`id="tab-endpoint" class="tab-pane"`,
		`navigateTab('endpoint')`,
		`<span class="nav-icon">🔌</span> Endpoint &amp; Key`,
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("missing endpoint tab piece: %q", need)
		}
	}

	// The retired Overview must be gone from the served bytes entirely: a leftover
	// pane or nav button would render a dead tab.
	for _, gone := range []string{
		`id="tab-overview"`,
		`id="nav-overview"`,
		`navigateTab('overview')`,
		// The function fed the Overview counters; it must not be called or
		// defined any more. A comment may still mention the name.
		"loadDashboardStats()",
		"function loadDashboardStats",
		`id="stat-providers"`,
		`id="cat-oauth-total"`,
		`provider-type-chips`,
	} {
		if strings.Contains(ui, gone) {
			t.Errorf("the retired Overview is still referenced by %q", gone)
		}
	}

	// The API Keys tab was folded into Endpoint & Key, matching upstream's
	// single "Endpoint & Key" page.
	for _, gone := range []string{
		`id="tab-keys"`,
		`id="nav-keys"`,
		`navigateTab('keys')`,
	} {
		if strings.Contains(ui, gone) {
			t.Errorf("the standalone API Keys tab is still present: %q", gone)
		}
	}
	if !strings.Contains(ui, `"endpoint", "usage", "providers", "combos", "console", "health", "settings"`) {
		t.Error("HASH_TABS was not updated for the new tab set")
	}
}

// TestUIEndpointTabShowsUrlsAndCurl checks the page actually tells the operator
// what to paste into a client.
func TestUIEndpointTabShowsUrlsAndCurl(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []string{
		`id="endpoint-local"`,
		`id="endpoint-docker"`,
		`id="curl-sample"`,
		"function copyEndpoint(",
		"function copyCurlSample(",
		"function renderEndpointHost()",
		// The host is derived from the address the operator used, so the page is
		// correct over LAN or a tunnel instead of always claiming localhost.
		"const host = location.hostname",
		"/v1/chat/completions",
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("endpoint page is missing %q", need)
		}
	}

	// An exposed host must warn.
	if !strings.Contains(ui, "endpoint-exposed-warn") {
		t.Error("the page no longer warns when the server is reachable from another machine")
	}
	if !strings.Contains(ui, "loadEndpointStatus") || !strings.Contains(ui, `fetch("/health"`) {
		t.Error("the endpoint status pill no longer probes the public /health route")
	}
}

// TestUIEndpointKeyListHasSecretControls checks the key list kept parity with
// the upstream page: a masked secret with reveal/copy, and a Paused marker.
func TestUIEndpointKeyListHasSecretControls(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []string{
		`id="endpoint-keys-body"`,
		`id="endpoint-keys-empty"`,
		"function toggleKeySecret(",
		"function maskKey(",
		"function copyKeySecret(",
		"const revealedKeys = new Set();",
		"allKeysById",
		"key-paused",
		"No API keys yet",
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("endpoint key list is missing %q", need)
		}
	}

	// A secret must be masked until revealed, never printed raw by default.
	if strings.Contains(ui, "${escapeHtml(k.key)}") {
		t.Error("the raw key is interpolated into the list without masking")
	}
	// Secrets must not survive a refresh.
	if strings.Contains(ui, "revealedKeys") && strings.Contains(ui, `localStorage.setItem("revealedKeys"`) {
		t.Error("revealed secrets are persisted across refreshes")
	}
}

// The fallbacks matter more than they look: every unrecognised hash lands on
// the default tab, so if that default drifts back to the retired Overview the
// first paint shows a blank page after any stale bookmark.
func TestUIEndpointIsTheDefaultLandingTab(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []string{
		`if (!raw) return { tab: "endpoint" };`,
		`if (tabId === "endpoint") loadEndpointTab();`,
		`var titles = { endpoint: "Endpoint & Key"`,
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("the endpoint tab is not the default landing view; missing %q", need)
		}
	}
	// parseHash has two fallbacks: an empty hash and an unrecognised one. Both
	// must land on the endpoint tab, and only those two.
	if n := strings.Count(ui, `return { tab: "endpoint" };`); n != 2 {
		t.Errorf("expected both parseHash fallbacks to return endpoint, found %d", n)
	}
	// Both hash tables (the pre-paint inline script and the bundle) must list it,
	// or the first frame and the bundle disagree about what is valid.
	if n := strings.Count(ui, `"endpoint", "usage", "providers", "combos", "console", "health", "settings"`); n != 2 {
		t.Errorf("expected endpoint in both tab tables, found %d", n)
	}
	// The static title is painted before the bundle runs and must match.
	if !strings.Contains(ui, `>Endpoint &amp; Key</div>`) {
		t.Error("the first-paint page title still names the retired tab")
	}
}

// The exposure warning is the only signal that keys can be used from another
// machine. It must depend on the real host, not be permanently hidden.
func TestUIEndpointWarnsOnlyWhenExposed(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `const localHosts = ["localhost", "127.0.0.1", "::1", "0.0.0.0", ""];`) {
		t.Error("the loopback allow-list is missing")
	}
	if !strings.Contains(ui, `if (warn) warn.style.display = localHosts.includes(host) ? "none" : "";`) {
		t.Error("the exposure warning no longer follows the real host")
	}
}
