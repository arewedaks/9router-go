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
		`<span class="nav-icon">api</span> Endpoint &amp; Key`,
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
	if !strings.Contains(ui, `"endpoint", "usage", "providers", "combos", "console", "token-saver", "settings"`) {
		t.Error("HASH_TABS was not updated for the new tab set")
	}
	// The Health & Setup tab was removed, so neither nav entry may survive.
	for _, gone := range []string{`id="nav-health"`, `id="tab-health"`, `navigateTab('health')`} {
		if strings.Contains(ui, gone) {
			t.Errorf("the retired Health & Setup tab is still present: %q", gone)
		}
	}
}

// TestUIEndpointTabShowsUrlsAndCurl checks the page tells the operator which
// base URL to call, for both a local and a containerised client.
//
// The curl/sample block was removed from this tab, so nothing here asserts on a
// copy-paste snippet any more.
func TestUIEndpointTabShowsUrlsAndCurl(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []string{
		`id="endpoint-local"`,
		`id="endpoint-docker"`,
		"function copyEndpoint(",
		// The signature takes the settings payload so the API-key switches can be
		// repainted from the same response the rest of the endpoint block uses.
		"function renderEndpointHost(settings)",
		// The host is derived from the address the operator used, so the page is
		// correct over LAN or a tunnel instead of always claiming localhost.
		"const host = location.hostname",
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
	if n := strings.Count(ui, `"endpoint", "usage", "providers", "combos", "console", "token-saver", "settings"`); n != 2 {
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

// The endpoint page must not print a port the client cannot dial.
//
// The listen port was hard-coded (20129) in the markup and used as the fallback
// when the browser reports no port. Behind a reverse proxy the browser reports
// none — https://example.com — so the page advertised
// "https://example.com:20129/v1": a URL that is wrong, and reachable only if the
// internal port happens to be exposed, which the Cloudflare hardening exists to
// prevent. The live install served on :20127 while the page said :20129.
func TestUIEndpointTabHasNoHardcodedPort(t *testing.T) {
	ui := readEmbeddedUI(t)

	if strings.Contains(ui, "20129") {
		t.Errorf("a hard-coded listen port is still present; the endpoint page would " +
			"advertise a port that is not the one clients should use")
	}
}

// The removed sample block must not leave orphans behind: a dangling element id
// or a handler that references it would be dead code masquerading as a feature.
func TestUIEndpointSampleIsFullyRemoved(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, gone := range []string{
		`id="curl-sample"`,
		"function copyCurlSample(",
		"function refreshEndpointSamples(",
		"svcEndpointBase",
	} {
		if strings.Contains(ui, gone) {
			t.Errorf("the removed endpoint-sample block still leaves behind: %q", gone)
		}
	}
}

// A reverse-proxy origin has no port in location.port, so the derived URL must
// omit it rather than borrow the listen port.
func TestUIEndpointOmitsPortForDefaultScheme(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []string{
		"function renderEndpointHost",
		"isDefaultPort",
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("renderEndpointHost no longer handles the proxied case: %s missing", need)
		}
	}
}

// The provider page must expose the account round-robin toggle, the control
// VansRouter shows next to its Connections heading. Without it an operator can
// only set the strategy by hand, and the stored fallbackStrategy is invisible.
func TestUIProviderPageHasAccountRoundRobinToggle(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []string{
		"function accountRoundRobinControls",
		"function setAccountRoundRobin",
		"function setAccountStickyLimit",
		"function saveAccountStrategy",
		"fallbackStrategy: \"round-robin\"",
		"stickyRoundRobinLimit",
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("account round-robin control missing: %s", need)
		}
	}
}

// The toggle must be rendered inside the Connections tab, not only defined.
func TestUIAccountRoundRobinIsRenderedInConnections(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "${accountRoundRobinControls(d)}") {
		t.Error("accountRoundRobinControls is defined but never rendered in the Connections tab")
	}
}

// The model row must reflow to two lines on a phone.
//
// Five controls sat inline with the model name, which on a narrow screen left
// the name clipped to a few characters — and the name is the thing the operator
// copies. The row is now two groups, so the controls wrap to their own line
// while staying on one line on a wide screen.
func TestModelRowWrapsOnNarrowScreens(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []string{
		`class="mrow-main"`,
		`class="mrow-actions"`,
		`.model-row .mrow-main`,
		`.model-row .mrow-actions`,
		`@media (max-width: 640px)`,
		`.model-row .mrow-main { flex: 1 1 100%;`,
		`.model-row .mrow-actions { flex: 1 1 100%;`,
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("model row does not reflow for mobile: %s missing", need)
		}
	}
}

// The model id must not be truncated on a narrow screen; wrapping is the point
// of the change.
func TestModelNameWrapsInsteadOfTruncatingOnMobile(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `.model-row .mid { white-space: normal; overflow-wrap: anywhere;`) {
		t.Error("the model id is still truncated on mobile instead of wrapping")
	}
}
