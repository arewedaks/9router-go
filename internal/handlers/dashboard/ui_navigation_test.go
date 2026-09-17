package dashboard

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// readEmbeddedUI returns the SPA source the server actually serves. Navigation
// behaviour lives in this file, so asserting against the served bytes checks
// what the browser receives rather than what happens to be on disk.
func readEmbeddedUI(t *testing.T) string {
	t.Helper()
	_, _, r := setupTestDashboard(t)
	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("ServeUI status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if body == "" {
		t.Fatal("ServeUI returned an empty body")
	}
	return body
}

// TestUIViewIsRememberedOnRefresh guards the fix for a refresh always dropping
// the operator on Overview: the selected view has to be recorded in the URL and
// re-applied on load.
func TestUIViewIsRememberedOnRefresh(t *testing.T) {
	ui := readEmbeddedUI(t)

	mustContain := []struct {
		what string
		need string
	}{
		{"hash parser", "function parseHash()"},
		{"hash writer", "function writeHash("},
		{"restore-on-load", "function restoreFromHash()"},
		{"back/forward support", `window.addEventListener("hashchange"`},
		// All three sign-in paths must restore the remembered view, not blindly
		// load the default stats page.
		{"restore after sign-in", "restoreFromHash();"},
		// Provider detail pages carry their sub-tab in the URL too.
		{"provider detail in hash", `writeHash("#provider/"`},
	}
	for _, c := range mustContain {
		if !strings.Contains(ui, c.need) {
			t.Errorf("missing %s: expected %q in the served UI", c.what, c.need)
		}
	}

	// A refresh previously always ended on the stats page; the boot path must
	// now route through the restore helper instead.
	if got := strings.Count(ui, "restoreFromHash();"); got < 3 {
		t.Errorf("only %d restoreFromHash() call sites; the three sign-in paths "+
			"(no-login, session cookie, API key) each need one", got)
	}
}

// TestUIHasNoLegacyEventGlobal locks in the fix for the nav highlight bug.
// switchTab used to read the global `event`, which only exists for real click
// events; calling it from code left every nav button unlit.
func TestUIHasNoLegacyEventGlobal(t *testing.T) {
	ui := readEmbeddedUI(t)
	if strings.Contains(ui, "event && event.target") {
		t.Error("switchTab still depends on the global `event`; " +
			"programmatic calls will leave the nav highlight unset")
	}
	// Highlighting must now be driven by a stable element id.
	if !strings.Contains(ui, `document.getElementById("nav-" + tabId)`) {
		t.Error("switchTab does not resolve the nav button by id")
	}
}

// TestUINoFlickerOnRefresh guards the fix for the visible flash of Overview on
// every refresh. Two things have to hold: no pane may be hard-coded active in
// the markup (otherwise the browser paints it before any script runs), and a
// synchronous bootstrap must pre-select the URL's pane before the first paint.
// The bootstrap cannot live in the main bundle, which only runs after the auth
// round-trip — that gap is exactly when the flicker was visible.
func TestUINoFlickerOnRefresh(t *testing.T) {
	ui := readEmbeddedUI(t)

	if strings.Contains(ui, `class="tab-pane active"`) {
		t.Error("a pane is hard-coded active in the markup; the browser will paint " +
			"it before the bootstrap runs and then snap to the real view")
	}

	boot := "Apply the view named in the URL"
	i := strings.Index(ui, boot)
	if i < 0 {
		t.Fatal("synchronous hash bootstrap is missing")
	}
	// It must run after the panes exist (they are parsed in order) and before
	// the main bundle, so the first paint is already correct.
	mainScript := strings.LastIndex(ui, "<script>")
	panesEnd := strings.Index(ui, "</main>")
	if panesEnd < 0 || panesEnd > i {
		t.Error("bootstrap runs before the panes are parsed; it cannot select them")
	}
	if i > mainScript {
		t.Error("bootstrap is placed after the main bundle, so the wrong pane " +
			"still gets a chance to paint first")
	}

	// It must not fetch anything: its only job is choosing a pane before paint.
	block := ui[i:mainScript]
	for _, forbidden := range []string{"fetch(", "await ", "loadDashboardStats", "loadProviders"} {
		if strings.Contains(block, forbidden) {
			t.Errorf("bootstrap block should not contain %q; it must stay synchronous", forbidden)
		}
	}
}

// TestUINavButtonsRecordHash makes sure every top-level tab is wired to the
// hash-recording entry point. A button left on the raw switchTab would look
// like it works until the operator refreshes.
func TestUINavButtonsRecordHash(t *testing.T) {
	ui := readEmbeddedUI(t)
	tabs := []string{"overview", "providers", "combos", "keys", "health", "settings"}
	for _, tab := range tabs {
		if !strings.Contains(ui, `navigateTab('`+tab+`')`) {
			t.Errorf("nav button for %q is not wired to navigateTab", tab)
		}
		if strings.Contains(ui, `onclick="switchTab('`+tab+`')"`) {
			t.Errorf("nav button for %q still calls switchTab directly", tab)
		}
		if !strings.Contains(ui, `id="nav-`+tab+`"`) {
			t.Errorf("nav button for %q has no id for highlight lookup", tab)
		}
	}
}

// TestUINavigationLivesInSidebar checks the navigation moved from the top bar
// into a left rail and that every pane still has a matching button. A pane
// without a button would be reachable only by typing a hash.
func TestUINavigationLivesInSidebar(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `class="sidebar"`) {
		t.Error("sidebar container is missing")
	}
	// The old horizontal nav container must be gone, otherwise the buttons
	// would render in the header again.
	if strings.Contains(ui, `class="nav-tabs"`) {
		t.Error("the header nav-tabs container is still present; navigation should be in the sidebar")
	}

	// Every pane needs a nav button, and vice versa.
	for _, tab := range []string{"overview", "providers", "combos", "keys", "health", "settings"} {
		if !strings.Contains(ui, `id="tab-`+tab+`" class="tab-pane"`) {
			t.Errorf("pane for %q is missing", tab)
		}
		if !strings.Contains(ui, `id="nav-`+tab+`"`) {
			t.Errorf("sidebar button for %q is missing", tab)
		}
	}

	// The retired tab must not linger anywhere.
	if strings.Contains(ui, `"savers"`) || strings.Contains(ui, `id="tab-savers"`) {
		t.Error(`the old "savers" tab is still referenced; it was folded into Settings`)
	}

	// Mobile drawer wiring.
	for _, need := range []string{`id="sidebar-scrim"`, `id="sidebar-toggle"`, "function toggleSidebar("} {
		if !strings.Contains(ui, need) {
			t.Errorf("missing mobile drawer piece: %q", need)
		}
	}
}

// TestUISettingsPageHasPasswordControls verifies the Settings tab exposes both
// credential actions against the endpoints the backend actually serves, and
// that the reset button warns before it can lock the operator out.
func TestUISettingsPageHasPasswordControls(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []string{
		"function changePassword()",
		"function resetPasswordConfirm()",
		"/api/dashboard/auth/change-password",
		"/api/dashboard/auth/reset-password",
		`id="set-cur-pw"`, `id="set-new-pw"`, `id="set-confirm-pw"`,
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("Settings page is missing %q", need)
		}
	}

	// Resetting restores the default password, so it must be confirmed first.
	reset := ui[strings.Index(ui, "async function resetPasswordConfirm()"):]
	reset = reset[:strings.Index(reset, "\n  }")]
	if !strings.Contains(reset, "confirm(") {
		t.Error("resetPasswordConfirm does not ask for confirmation before wiping the password")
	}
	// And the danger has to be visible on the page, not only in the dialog.
	if !strings.Contains(ui, "settings-warn") {
		t.Error("the reset card has no visible warning")
	}
}

// TestUISettingsKeepsTokenOptimization makes sure folding Token Savers into
// Settings did not drop any of the four toggles or their level selectors.
func TestUISettingsKeepsTokenOptimization(t *testing.T) {
	ui := readEmbeddedUI(t)
	for _, id := range []string{"saver-rtk", "saver-caveman", "saver-caveman-level", "saver-ponytail", "saver-ponytail-level", "saver-guard"} {
		if !strings.Contains(ui, `id="`+id+`"`) {
			t.Errorf("token saver control %q was lost in the move", id)
		}
	}
	if !strings.Contains(ui, "function loadSettings()") {
		t.Error("loadSettings() is missing; the settings pane would render empty")
	}
}

// TestUIHasNoDuplicateIDs catches a class of bug that is invisible until it
// bites: two elements sharing an id make getElementById return whichever comes
// first in the document, so a form silently reads or clears the wrong field.
// The Settings password form originally collided with the API Keys tab's own
// "Dashboard Security" form, which duplicated this feature.
func TestUIHasNoDuplicateIDs(t *testing.T) {
	ui := readEmbeddedUI(t)

	re := regexp.MustCompile(`\sid="([^"]+)"`)
	seen := map[string]int{}
	for _, m := range re.FindAllStringSubmatch(ui, -1) {
		seen[m[1]]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("id %q appears %d times; getElementById would resolve to the wrong element", id, n)
		}
	}
}

// TestUIPasswordFormIsOnlyInSettings pins the fix for the duplicated feature:
// the API Keys tab used to carry its own "Dashboard Security" password form,
// byte-for-byte the same job as Settings → Security. Two forms writing one
// credential meant an operator could change the password in one place and be
// confused when the other still showed the old fields.
func TestUIPasswordFormIsOnlyInSettings(t *testing.T) {
	ui := readEmbeddedUI(t)

	if strings.Contains(ui, "Dashboard Security") {
		t.Error("the duplicated Dashboard Security section is still present")
	}
	if strings.Contains(ui, "changeDashboardPassword") {
		t.Error("the API Keys tab's password handler is still defined; it is dead code now")
	}
	// The first-run hint used to live in that removed block, but the login modal
	// still needs to know whether a password exists at all.
	if !strings.Contains(ui, "authHasPassword") {
		t.Error("authHasPassword tracking was lost while removing the duplicate form")
	}

	// Exactly one password form, and it must be inside the Settings pane.
	settingsStart := strings.Index(ui, `id="tab-settings"`)
	if settingsStart < 0 {
		t.Fatal("settings pane is missing")
	}
	securityIdx := strings.Index(ui, `id="set-cur-pw"`)
	if securityIdx < settingsStart {
		t.Error("the password form is not inside the Settings pane")
	}
	if n := strings.Count(ui, `id="set-new-pw"`); n != 1 {
		t.Errorf("expected exactly one new-password field, found %d", n)
	}

	// The API Keys tab must still be intact — only the password block was removed.
	for _, need := range []string{`id="tab-keys"`, "keys-table-body", "function loadKeys()"} {
		if !strings.Contains(ui, need) {
			t.Errorf("removing the password form damaged the API Keys tab: %q is gone", need)
		}
	}
}

// TestUIProviderCountsUseEffectiveStatus pins the fix for the "no accounts are
// connecting" report. The provider cards counted a connection as active when
// its toggle was on (`isActive === 1`), so 33 openai-compatible keys that all
// failed upstream with "credit insufficient balance" rendered as "33/33
// active". The UI must count health, not the switch position.
func TestUIProviderCountsUseEffectiveStatus(t *testing.T) {
	ui := readEmbeddedUI(t)

	// The shared helpers that interpret the connection status must exist.
	for _, need := range []string{
		"function effectiveStatusOf(",
		"function isEffectivelyActive(",
		"function acctDotClass(",
		"function statusPillFor(",
		"function connStatusClass(",
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("status helper missing: %q", need)
		}
	}

	// The provider card counter must go through the effective-status helper.
	if !strings.Contains(ui, "accounts.filter(isEffectivelyActive).length") {
		t.Error("provider card active count does not use isEffectivelyActive()")
	}
	if strings.Contains(ui, "accounts.filter(a => a.isActive === 1).length") {
		t.Error("provider card still counts a connection as active just because its toggle is on")
	}

	// The account-row and expanded-list statuses must be derived, never read
	// straight from testStatus (a stale "unavailable" is not a broken account).
	// The only sanctioned direct comparison is the legacy-payload fallback
	// inside isEffectivelyActive itself.
	if n := strings.Count(ui, `p.testStatus === "active"`); n != 1 {
		t.Errorf("expected exactly one direct testStatus comparison (the fallback in isEffectivelyActive), found %d", n)
	}

	// The dot must distinguish a failing-but-enabled account from a healthy one.
	if !strings.Contains(ui, "return isEffectivelyActive(p) ? \"on\" : \"warn\";") {
		t.Error("account dot no longer colours an enabled-but-failing account distinctly")
	}

	// A failing status must render as the red pill, not the neutral idle one.
	if !strings.Contains(ui, "return `<span class=\"acct-pill exp\">${escapeHtml(status)}</span>`;") {
		t.Error("a failing effective status is not shown with the error pill")
	}
}

// TestUIHasNoHardcodedActiveFallbackForStatus guards against regressing to the
// old "isActive means connected" reading in the connection list markup.
func TestUIHasNoHardcodedActiveFallbackForStatus(t *testing.T) {
	ui := readEmbeddedUI(t)

	// The provider-detail connection list used to read:
	//   const dot = p.isActive !== 1 ? "off" : (p.expired ? "warn" : "on");
	// which lit a green dot for a credit-exhausted, enabled account.
	if strings.Contains(ui, `const dot = p.isActive !== 1 ? "off" : (p.expired ? "warn" : "on")`) {
		t.Error("the connection list still colours dots from isActive alone")
	}
	// And it printed the raw testStatus with no class, hiding failures.
	if strings.Contains(ui, `title="status: ${escapeHtml(p.testStatus || 'idle')}"`) {
		t.Error("the connection status chip still shows the raw testStatus without a severity class")
	}
}

// TestUINeverLabelsChipsWithGeneratedProviderKey pins the label rule on the
// served bytes: the Overview chips must go through niceProviderLabel so a
// custom endpoint's generated key ("openai-compatible-chat-<uuid>") can never
// be printed verbatim. Upstream shows node.name and keeps the id internal.
func TestUINeverLabelsChipsWithGeneratedProviderKey(t *testing.T) {
	body := readEmbeddedUI(t)

	if !strings.Contains(body, "function niceProviderLabel(") {
		t.Fatal("niceProviderLabel helper missing from the served UI")
	}
	if !strings.Contains(body, "chip.innerText = `${niceProviderLabel(provider)}: ${count}`;") {
		t.Fatal("the chip label must be routed through niceProviderLabel")
	}
	// The old raw interpolation must be gone.
	if strings.Contains(body, "chip.innerText = `${provider}: ${count}`;") {
		t.Fatal("the raw provider key is still interpolated into the chip label")
	}
}

// TestUIModelPrefixUsesNodePrefixNotGeneratedKey pins the model-list label on a
// compatible endpoint. The router addresses a model as "<prefix>/<modelId>"
// ("bai/claude-fable-5"), but the tab used to fall back to the raw provider key
// when no alias was configured, so it rendered
// "openai-compatible-chat-8e96ef73-.../claude-fable-5". currentModelAlias must
// prefer d.prefix.
func TestUIModelPrefixUsesNodePrefixNotGeneratedKey(t *testing.T) {
	body := readEmbeddedUI(t)

	// The assignment has to consult prefix before falling back to provider.
	// Skip the `let currentModelAlias = null;` declaration and target the
	// assignment inside renderProviderDetail.
	marker := "currentModelAlias = (d.prefix"
	alt := "currentModelAlias = (d.aliases"
	idx := strings.Index(body, marker)
	if idx < 0 {
		idx = strings.Index(body, alt)
	}
	if idx < 0 {
		t.Fatal("currentModelAlias assignment not found in served UI")
	}
	end := strings.Index(body[idx:], ";")
	if end < 0 {
		t.Fatal("currentModelAlias assignment is not terminated")
	}
	stmt := body[idx : idx+end]

	if !strings.Contains(stmt, "d.prefix") {
		t.Fatalf("currentModelAlias does not prefer d.prefix: %q", stmt)
	}
	// d.provider must be the last resort, not the primary source.
	prefixAt := strings.Index(stmt, "d.prefix")
	providerAt := strings.Index(stmt, "d.provider")
	if providerAt >= 0 && providerAt < prefixAt {
		t.Fatalf("currentModelAlias consults d.provider before d.prefix: %q", stmt)
	}
	// No bare `: d.provider` shortcut that would skip the prefix check.
	if strings.Contains(stmt, "? d.aliases[0] : d.provider") {
		t.Fatalf("currentModelAlias still short-circuits to d.provider: %q", stmt)
	}
}

// The Add Compatible Provider modal is a single form serving three modes, the
// way OmniRoute's AddCompatibleProviderModal does. These tests assert against the
// bytes the server actually serves, so a broken embed fails them too.
func TestUIHasCompatibleProviderModal(t *testing.T) {
	body := readEmbeddedUI(t)

	for _, want := range []string{
		`id="compatible-modal"`,
		`id="compat-name"`,
		`id="compat-prefix"`,
		`id="compat-baseurl"`,
		`id="compat-apitype"`,
		`id="compat-modelspath"`,
		`id="compat-checkkey"`,
		`id="compat-advanced"`,
		`openCompatModal`,
		`submitCompatibleNode`,
		`validateCompatNode`,
		`toggleCompatAdvanced`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("served UI is missing the compatible-provider modal piece %s", want)
		}
	}

	// The three modes and their distinguishing defaults.
	for _, want := range []string{
		`"/api/dashboard/provider-nodes/validate"`,
		`"/api/dashboard/provider-nodes"`,
		`CC_DEFAULT_CHAT_PATH`,
		`"/v1/messages?beta=true"`,
		`"anthropic-compatible"`,
		`"openai-compatible"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("served UI does not reference %s", want)
		}
	}
}

// TestUICompatibleModalPrefersPrefixAsRoutingKey pins the one piece of the create
// payload the operator is most likely to get wrong: models are addressed by
// `<prefix>/<model>`, so the prefix must be sent as `prefix`, exactly.
func TestUICompatibleModalSendsPrefixField(t *testing.T) {
	body := readEmbeddedUI(t)

	idx := strings.Index(body, "function compatPayload()")
	if idx < 0 {
		t.Fatal("compatPayload not found in served UI")
	}
	end := strings.Index(body[idx:], "\n  }")
	if end < 0 {
		t.Fatal("compatPayload body not terminated")
	}
	fn := body[idx : idx+end]

	if !strings.Contains(fn, `prefix:`) {
		t.Fatalf("compatPayload does not send a prefix: %q", fn)
	}
	// Guard against reintroducing the raw provider key as a label anywhere in
	// the modal's payload builder.
	if strings.Contains(fn, "provider:") {
		t.Fatalf("compatPayload must not send a raw provider key: %q", fn)
	}
}

// TestUICompatibleEndpointsSectionComesFirst pins that the Compatible Endpoints
// section renders above every other section, so a just-added gateway is the
// first thing the operator sees.
func TestUICompatibleEndpointsSectionComesFirst(t *testing.T) {
	body := readEmbeddedUI(t)

	idx := strings.Index(body, "const CATEGORY_ORDER")
	if idx < 0 {
		t.Fatal("CATEGORY_ORDER not found in served UI")
	}
	end := strings.Index(body[idx:], "]")
	if end < 0 {
		t.Fatal("CATEGORY_ORDER is not terminated")
	}
	decl := body[idx : idx+end]

	// "custom" must be the first key in the literal.
	open := strings.Index(decl, "[")
	if open < 0 {
		t.Fatalf("CATEGORY_ORDER has no literal list: %q", decl)
	}
	list := decl[open+1:]
	first := strings.TrimSpace(strings.SplitN(list, ",", 2)[0])
	if first != `"custom"` {
		t.Fatalf("CATEGORY_ORDER does not start with custom: %q", decl)
	}
	if !strings.Contains(list, `"custom"`) {
		t.Fatalf("CATEGORY_ORDER dropped custom entirely: %q", decl)
	}
}

// TestUIHasCompatibleNodeCard pins the compatible-node card on the provider
// detail page. Upstream renders the endpoint (base URL + protocol + path) with
// its own Edit/Delete actions; without it an operator can never see — or fix — the
// URL a node points at after creating it.
func TestUIHasCompatibleNodeCard(t *testing.T) {
	body := readEmbeddedUI(t)

	for _, want := range []string{
		`function renderCompatibleNodeCard(d)`,
		`renderCompatibleNodeCard(d)`,
		`n.baseUrl`,
		`n.apiPath`,
		`n.apiLabel`,
		`n.compatMode`,
		`function openNodeModal(`,
		`function submitNodeEdit(`,
		`function deleteNode(`,
		`id="node-modal"`,
		`id="node-name"`,
		`id="node-prefix"`,
		`id="node-baseurl"`,
		`id="node-apitype-group"`,
		`id="node-modelspath-group"`,
		`id="node-error"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("embedded UI is missing %q", want)
		}
	}

	// The card must be gated on the node payload; a built-in provider has none and
	// must render exactly as before.
	if !strings.Contains(body, `const n = d.node;`) || !strings.Contains(body, `if (!n) return '';`) {
		t.Error("compatible-node card must return empty when there is no node")
	}
}

// TestUICompatibleNodeCardNeverShowsNodeID pins the display rule: the generated
// node id is an internal routing key, so the card may only use it in the fetch
// URL (encoded), never as rendered text.
func TestUICompatibleNodeCardNeverShowsNodeID(t *testing.T) {
	body := readEmbeddedUI(t)

	// The card prints the operator's own name and the endpoint, never the id.
	// The id does appear inside onclick attributes (escaped) so the buttons can
	// address the right node — what must never happen is rendering it as text.
	if strings.Contains(body, `>${escapeHtml(n.id)}<`) {
		t.Error("card must not render the node id as text")
	}
	if !strings.Contains(body, `provider-nodes/${encodeURIComponent(`) {
		t.Error("node id should only appear encoded inside request URLs")
	}
}

// TestUICompatibleNodeCardShowsWarnOnlyForCC pins that the Claude Code warning is
// conditional: an ordinary OpenAI/Anthropic node must not carry it, or the banner
// becomes noise the operator learns to ignore.
func TestUICompatibleNodeCardShowsWarnOnlyForCC(t *testing.T) {
	body := readEmbeddedUI(t)
	if !strings.Contains(body, `const isCC = n.compatMode === 'cc';`) {
		t.Error("card must detect the cc compat mode")
	}
	if !strings.Contains(body, `const warn = isCC`) {
		t.Error("the warning banner must be conditional on isCC")
	}
}

// TestUIDeleteNodeWarnsAboutConnections pins the destructive-action honesty:
// deleting a node removes its accounts too, so the confirm text must say so when
// connections exist.
func TestUIDeleteNodeWarnsAboutConnections(t *testing.T) {
	body := readEmbeddedUI(t)
	if !strings.Contains(body, `This also deletes its ${count} connection(s)`) {
		t.Error("delete confirm must mention the connections it will remove")
	}
	if !strings.Contains(body, `?cascade=1`) {
		t.Error("delete must pass cascade=1 once the operator has confirmed")
	}
}
