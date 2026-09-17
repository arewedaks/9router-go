package dashboard

import (
	"strings"
	"testing"
)

// The Usage page is ported from the upstream Next.js dashboard. These tests pin
// the parts of that contract a user can see: the two sub-tabs, the five period
// buttons with their exact labels, the stat-card titles, the table view options
// and the four per-view empty messages. Strings are quoted from the reference
// source, so a typo here is a regression against the design being copied.

func TestUsageTabIsReachableFromTheSidebar(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `id="nav-usage"`) {
		t.Error(`the sidebar has no element with id="nav-usage"`)
	}
	if !strings.Contains(ui, `onclick="navigateTab('usage')"`) {
		t.Error("the sidebar Usage button does not navigate to the usage tab")
	}
	if !strings.Contains(ui, `id="tab-usage"`) {
		t.Error(`there is no pane with id="tab-usage"`)
	}
	if !strings.Contains(ui, `"usage", "providers"`) {
		t.Error("usage is missing from HASH_TABS, so #usage would fall back to Overview")
	}
	if !strings.Contains(ui, `usage: "Usage"`) {
		t.Error("the Usage page title is missing from TAB_TITLES")
	}
	if !strings.Contains(ui, `if (tabId === "usage") loadUsage();`) {
		t.Error("switchTab does not call loadUsage() when the usage tab opens")
	}
}

func TestUsageSubTabsMatchTheReference(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `data-utab="overview"`) || !strings.Contains(ui, `data-utab="details"`) {
		t.Fatal("the Overview / Details sub-tabs are missing")
	}
	// Labels are matched with their markup so a stray rename is caught.
	for _, label := range []string{">Overview<", ">Details<"} {
		if !strings.Contains(ui, label) {
			t.Errorf("the sub-tab label %s is missing", label)
		}
	}
	if !strings.Contains(ui, "function setUsageTab(") {
		t.Error("setUsageTab is not defined")
	}
}

// The period labels have inconsistent casing upstream (Today, 24h, 7D, 30D,
// 60D); copying them exactly is the point.
func TestUsagePeriodLabelsMatchTheReference(t *testing.T) {
	ui := readEmbeddedUI(t)

	periods := []struct {
		value string
		label string
	}{
		{"today", "Today"},
		{"24h", "24h"},
		{"7d", "7D"},
		{"30d", "30D"},
		{"60d", "60D"},
	}
	for _, p := range periods {
		want := `data-period="` + p.value + `" onclick="setUsagePeriod('` + p.value + `')">` + p.label + `<`
		if !strings.Contains(ui, want) {
			t.Errorf("period button for %s is not exactly %q", p.value, want)
		}
	}
}

func TestUsageCardsMatchTheReferenceTitles(t *testing.T) {
	ui := readEmbeddedUI(t)

	cards := []string{
		"Total Requests",
		"Total Input Tokens",
		"Cached Tokens",
		"Output Tokens",
		"Est. Cost",
	}
	for _, title := range cards {
		if !strings.Contains(ui, title) {
			t.Errorf("stat card %q is missing", title)
		}
	}

	if !strings.Contains(ui, "% hit rate") {
		t.Error("the Cached Tokens card lost its hit-rate sub-label")
	}
	if !strings.Contains(ui, "Estimated, not actual billing") {
		t.Error("the Est. Cost card lost its disclaimer sub-label")
	}
}

func TestUsageTableViewsMatchTheReference(t *testing.T) {
	ui := readEmbeddedUI(t)

	options := []struct {
		value string
		label string
	}{
		{"model", "Usage by Model"},
		{"account", "Usage by Account"},
		{"apiKey", "Usage by API Key"},
		{"endpoint", "Usage by Endpoint"},
	}
	for _, o := range options {
		want := `<option value="` + o.value + `">` + o.label + `</option>`
		if !strings.Contains(ui, want) {
			t.Errorf("table view option missing: %q", want)
		}
	}
}

// Each table view has its own empty message; they are not interchangeable.
func TestUsageEmptyMessagesMatchTheReference(t *testing.T) {
	ui := readEmbeddedUI(t)

	messages := []string{
		"No usage recorded yet.",
		"No account-specific usage recorded yet.",
		"No API key usage recorded yet.",
		"No endpoint usage recorded yet.",
		"No requests yet.",
	}
	for _, m := range messages {
		if !strings.Contains(ui, m) {
			t.Errorf("empty message missing: %q", m)
		}
	}
	if !strings.Contains(ui, "Failed to load usage statistics.") {
		t.Error("the stats load-failure message is missing")
	}
	if !strings.Contains(ui, "No request details found") {
		t.Error("the request-details empty message is missing")
	}
	if !strings.Contains(ui, "No data for this period") {
		t.Error("the chart empty message is missing")
	}
}

func TestUsageChartTogglesAndBareArrayConsumption(t *testing.T) {
	ui := readEmbeddedUI(t)

	// The chart toggle uses "Cost" (singular) while the table toggle uses
	// "Costs"; both spellings are deliberate.
	if !strings.Contains(ui, `data-cmode="cost"`) || !strings.Contains(ui, `data-cmode="tokens"`) {
		t.Error("the chart series toggle is missing")
	}
	if !strings.Contains(ui, `data-vmode="costs"`) {
		t.Error("the table view-mode toggle lost its Costs button")
	}
	// The chart endpoint returns a bare array; the UI must iterate it directly.
	if !strings.Contains(ui, `usageState.chart = await chartRes.json();`) {
		t.Error("the chart response is not stored as a bare array")
	}
	if strings.Contains(ui, "chartRes.json()).points") {
		t.Error("the chart response is being read as an object wrapper")
	}
}

// The details table must be paginated through the backend envelope, not sliced
// client-side, and the drawer must be reachable from a row action.
func TestUsageDetailsContract(t *testing.T) {
	ui := readEmbeddedUI(t)

	checks := []struct {
		what string
		need string
	}{
		{"detail fetch", "/api/dashboard/usage/request-details?"},
		{"pagination info", "Showing ${fmtNum(startItem)} to ${fmtNum(endItem)} of"},
		{"rows label", "Rows:"},
		{"detail action", ">Detail<"},
		{"drawer title", ">Request Details<"},
		{"filter provider", `id="usage-filter-provider"`},
		{"filter start", `id="usage-filter-start"`},
		{"filter end", `id="usage-filter-end"`},
		{"search button", "applyUsageFilters()"},
		{"clear button", "clearUsageFilters()"},
	}
	for _, c := range checks {
		if !strings.Contains(ui, c.need) {
			t.Errorf("%s: expected to find %q", c.what, c.need)
		}
	}

	for _, h := range []string{"Timestamp", "Model", "Provider", "Input Tokens", "Output Tokens", "Latency", "Action"} {
		if !strings.Contains(ui, ">"+h+"<") {
			t.Errorf("the details table is missing the %q column", h)
		}
	}
}

// The usage endpoints must be addressed under the session-scoped dashboard
// prefix. Hitting the public /api/usage/* paths would 404 because those are the
// live-stream routes, a different feature entirely.
func TestUsageUIUsesTheDashboardScopedEndpoints(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, path := range []string{
		"/api/dashboard/usage/stats?period=",
		"/api/dashboard/usage/chart?period=",
		"/api/dashboard/usage/filters",
		"/api/dashboard/usage/request-details",
	} {
		if !strings.Contains(ui, path) {
			t.Errorf("the UI never calls %q", path)
		}
	}
}

// The cost split is derived because the rollup stores a single cost per bucket;
// pin that so a later refactor cannot silently drop the derived fields.
func TestUsageDerivesSplitCosts(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "totals.inputCost = totals.promptTokens + totals.completionTokens > 0") {
		t.Error("the summary row no longer derives an input/output cost split")
	}
	if !strings.Contains(ui, "totals.outputCost = totals.cost - totals.inputCost") {
		t.Error("the summary row no longer derives output cost")
	}
}

// The Provider Topology panel renders the "web" of provider spokes around the
// router. It is hand-rolled SVG — the design forbids a graph library — so these
// tests pin the pieces that make that true, plus the string the empty state
// shows.
func TestUsageTopologyPanelExists(t *testing.T) {
	ui := readEmbeddedUI(t)

	checks := []struct {
		what string
		need string
	}{
		{"panel mount point", `id="usage-topology"`},
		{"panel label", ">Provider Topology<"},
		{"empty state", "No providers connected"},
		{"renderer", "function renderUsageTopology()"},
		{"layout maths", "TOPO_NODE_W = 180"},
		{"ellipse placement", "const angle = -Math.PI / 2 + (2 * Math.PI * i) / count;"},
		{"ellipse aspect ratio", "const ry = Math.max(TOPO_BASE_RY, rx * 0.55);"},
		{"minimum radius grows with node count", "const minRx = ((TOPO_NODE_W + 24) * count) / (2 * Math.PI);"},
		{"pan installer", "function installTopoPan("},
		{"zoom", "function topoZoom("},
		{"reset", "function topoReset("},
		{"legend", "topo-legend"},
		{"zoom controls", "topo-zoom-btns"},
	}
	for _, c := range checks {
		if !strings.Contains(ui, c.need) {
			t.Errorf("%s: expected to find %q", c.what, c.need)
		}
	}

	// No charting/graph library may be pulled in: the bundle has no build step
	// and must stay dependency-free.
	for _, forbidden := range []string{"@xyflow/react", "react-flow", "recharts", "d3.select"} {
		if strings.Contains(ui, forbidden) {
			t.Errorf("the topology must not depend on %q", forbidden)
		}
	}
}

// The topology draws one spoke per provider, animated when the provider is
// active. The three edge classes are the contract the CSS hangs off.
func TestUsageTopologyEdgeStates(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, cls := range []string{"topo-edge topo-active", "topo-edge topo-last", `class="${cls}" d="${d}"`} {
		if !strings.Contains(ui, cls) {
			t.Errorf("edge markup missing: %q", cls)
		}
	}
	if !strings.Contains(ui, "<animateMotion dur=") {
		t.Error("active spokes no longer carry travelling particles")
	}
	if !strings.Contains(ui, "for (let k = 0; k < 6; k++)") {
		t.Error("the travelling-particle count changed")
	}
	if !strings.Contains(ui, `"#22d3ee"`) {
		t.Error("the active spoke colour changed")
	}
}

// Generated compatible-node ids are a long UUID and must never surface in the
// graph, exactly as they never surface in the provider grid. Labels prefer the
// operator-facing names.
func TestUsageTopologyNeverShowsGeneratedNodeIDs(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "function providerLabel(id)") {
		t.Fatal("providerLabel is missing")
	}
	if !strings.Contains(ui, `return isGeneratedNodeId(id) ? "Custom endpoint" : id;`) {
		t.Error("providerLabel no longer falls back to a friendly name for generated ids")
	}
	if !strings.Contains(ui, "p.nodeName || p.displayName || p.registryName || p.accountLabel || p.name") {
		t.Error("providerLabel no longer prefers the operator-facing names")
	}
	// The icon resolver must be the shared one, so the \"no logo for a generated
	// id\" rule has a single implementation.
	if !strings.Contains(ui, "const resolved = providerLogoId(id);") {
		t.Error("the topology does not reuse providerLogoId for brand assets")
	}
}

func TestUsageTopologyModes(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `data-tmode="all"`) || !strings.Contains(ui, `data-tmode="used"`) {
		t.Error("the All / Used mode toggle is missing")
	}
	// \"all\" is the default, matching upstream, where every connected LLM
	// provider is drawn even before it serves a request.
	if !strings.Contains(ui, `topologyMode: "all",`) {
		t.Error("the topology no longer defaults to showing every provider")
	}
	// Keyless providers carry no connection but are still real LLM routes, so
	// the only connection filtered out is one the operator switched off.
	if !strings.Contains(ui, "if (!p.noConnection && p.isActive === false) return;") {
		t.Error("the graph no longer admits keyless providers, or no longer skips inactive connections")
	}
}
