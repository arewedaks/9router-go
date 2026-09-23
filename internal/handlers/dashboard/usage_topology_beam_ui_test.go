package dashboard

import (
	"strings"
	"testing"
)

// Active spokes on the Provider Topology render as a Kame-style electric beam
// instead of a plain dashed line, matching VansRouter's TopologyEdge: three
// stacked strokes over a shared turbulence filter, plus orbs and sparks. These
// tests pin the pieces the effect is built from, since each one is easy to drop
// in a refactor while the graph still "looks fine" — just without the animation.

func TestTopologyActiveSpokeDrawsThreeStrokeBeam(t *testing.T) {
	ui := readEmbeddedUI(t)

	// The three layers, from widest to hottest.
	for _, cls := range []string{"topo-halo", "topo-plasma", "topo-core"} {
		if !strings.Contains(ui, `class="`+cls+`"`) {
			t.Errorf("active beam is missing its %s stroke", cls)
		}
	}
	// The reference's geometry: 10px cyan halo, 5px green plasma, 2.2px white core.
	if !strings.Contains(ui, `stroke-width="10"`) {
		t.Error("halo is no longer the widest stroke")
	}
	if !strings.Contains(ui, `stroke-width="5"`) {
		t.Error("plasma stroke is missing")
	}
	if !strings.Contains(ui, `stroke-width="2.2"`) {
		t.Error("hot white core is missing")
	}
	if !strings.Contains(ui, `stroke="#4ade80"`) {
		t.Error("plasma lost its green")
	}
	if !strings.Contains(ui, `stroke="#f8fafc"`) {
		t.Error("core lost its hot white")
	}
}

// The electric ripple comes from an animated feTurbulence feeding an
// feDisplacementMap. Without the <animate> the beam is a static blur.
func TestTopologyBeamUsesAnimatedTurbulenceFilter(t *testing.T) {
	ui := readEmbeddedUI(t)

	mustContain := []string{
		`<feTurbulence`,
		`type="fractalNoise"`,
		`<feDisplacementMap`,
		`attributeName="baseFrequency"`,
		`values="0.8;1.4;0.8"`,
		`in="SourceGraphic"`,
		`xChannelSelector="R"`,
	}
	for _, item := range mustContain {
		if !strings.Contains(ui, item) {
			t.Errorf("turbulence filter is missing %q", item)
		}
	}
	// The filter must live in <defs>, not inside the edge loop: one filter per
	// edge would multiply the number of animated filter regions. Other defs
	// (the hub gradient) may follow the filters, so match the opening only.
	if !strings.Contains(ui, "<defs>${beamFilters}") {
		t.Error("beam filters are not hoisted into <defs>")
	}
	if !strings.Contains(ui, "const beamFilters = providers.map") {
		t.Error("beam filters are no longer built once per provider")
	}
}

// Sparks blink along the wire, which is what makes it read as current rather
// than as a second set of steady orbs.
func TestTopologyActiveSpokeHasBlinkingSparks(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "topo-spark") {
		t.Fatal("active beam has no sparks")
	}
	if !strings.Contains(ui, `attributeName="opacity"`) {
		t.Error("sparks do not blink")
	}
	if !strings.Contains(ui, `for (let k = 0; k < 5; k++)`) {
		t.Error("the spark count changed")
	}
}

// The idle/last-used spokes must stay plain lines: hoisting the beam into every
// edge would make an idle graph as loud as a busy one.
func TestTopologyIdleSpokesStayPlain(t *testing.T) {
	ui := readEmbeddedUI(t)

	// The active beam is built inside an if/else, and the else branch still emits
	// the single plain path the idle and last-used spokes use.
	if !strings.Contains(ui, "} else {\n        edges += `<path class=\"${cls}\"") {
		t.Error("the beam is not confined to the active branch")
	}
	if !strings.Contains(ui, "edges += `<path class=\"${cls}\" d=\"${d}\" stroke=\"${stroke}\" stroke-width=\"${width}\"/>") {
		t.Error("idle spokes no longer render as a plain edge")
	}
}

// The CSS animations the beam depends on must all be defined.
func TestTopologyBeamAnimationsAreDefined(t *testing.T) {
	ui := readEmbeddedUI(t)

	mustContain := []string{
		"@keyframes topo-flicker",
		"@keyframes topo-dash",
		"@keyframes topo-router-label-flicker",
		".topo-halo { animation:",
		".topo-plasma {",
		".topo-core {",
		".topo-router-label.is-powering {",
	}
	for _, item := range mustContain {
		if !strings.Contains(ui, item) {
			t.Errorf("beam animation %q is undefined", item)
		}
	}
	// The halo flicker is the reference's stepped opacity curve.
	if !strings.Contains(ui, "steps(2) infinite") {
		t.Error("halo flicker is no longer stepped")
	}
}

// The beam only renders for providers the UI considers active. It used to read
// activeRequests from /api/dashboard/usage/stats, which is built from recorded
// history and therefore always reports an empty list — so every spoke stayed
// idle and the animation never appeared no matter how much traffic ran. The
// live tracker at /api/usage/stats is the endpoint that actually knows what is
// in flight.
func TestTopologyReadsLiveTrackerNotHistoricalStats(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `api("/api/usage/stats")`) {
		t.Fatal("the topology never reads the live in-flight tracker")
	}
	if !strings.Contains(ui, "usageState.liveTracker") {
		t.Error("the live tracker response is not stored")
	}
	if !strings.Contains(ui, "usageState.liveTracker = await res.json()") {
		t.Error("refreshUsageLive does not store the tracker payload")
	}
	// The historical field is only a fallback, never the primary source.
	if !strings.Contains(ui, "(tracker && tracker.activeRequests) || (usageState.stats.activeRequests)") {
		t.Error("the active set no longer prefers the live tracker")
	}
}

// A request can finish well inside the aggregate refresh period, so the tracker
// needs its own faster poll, and the speaker must linger briefly after the
// request ends or the beam would flash for less than a frame.
func TestTopologyPollsTrackerFasterThanAggregates(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "USAGE_LIVE_MS") {
		t.Fatal("the tracker has no dedicated poll interval")
	}
	if !strings.Contains(ui, "usageLiveTimer = setInterval(() => { refreshUsageLive(); }") {
		t.Error("the tracker interval is not started with usage refresh")
	}
	if !strings.Contains(ui, "function refreshUsageLive()") {
		t.Error("refreshUsageLive is missing")
	}
	// Both timers must be cleared, or leaving the tab leaks a poller.
	if !strings.Contains(ui, "clearInterval(usageLiveTimer)") {
		t.Error("stopUsageRefresh does not clear the tracker interval")
	}
	// The linger window keeps a short request visible.
	if !strings.Contains(ui, "recentlyActive") || !strings.Contains(ui, "LINGER_MS") {
		t.Error("spokes do not linger after their request finishes")
	}
}

// An idle graph must still look alive: the last-used spoke keeps a slow dash
// animation so the panel does not read as frozen when nothing is in flight.
func TestTopologyLastUsedSpokeAnimatesWhenIdle(t *testing.T) {
	ui := readEmbeddedUI(t)

	start := strings.Index(ui, ".topo-edge.topo-last {")
	if start < 0 {
		t.Fatal("the last-used spoke style is missing")
	}
	end := strings.Index(ui[start:], "}")
	if end < 0 {
		t.Fatal("unterminated .topo-edge.topo-last block")
	}
	block := ui[start : start+end]
	if !strings.Contains(block, "animation: topo-dash") {
		t.Errorf("the idle spoke carries no animation: %q", block)
	}
}
