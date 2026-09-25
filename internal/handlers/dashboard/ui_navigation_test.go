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
// the operator on the first tab: the selected view has to be recorded in the URL
// and re-applied on load.
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
	tabs := []string{"endpoint", "providers", "combos", "settings"}
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
	for _, tab := range []string{"endpoint", "providers", "combos", "settings"} {
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

// TestUITokenSaverPageHasEveryControl pins the Token Saver page's contract:
// every stage the pipeline runs must have a control, each control must be wired
// to a handler, and the page must be reachable from the sidebar and the hash
// router. A stage without a control is a setting only a database edit can
// change, which is how the Headroom switch went missing before.
func TestUITokenSaverPageHasEveryControl(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, id := range []string{
		"saver-rtk",
		"saver-caveman", "saver-caveman-levels",
		"saver-ponytail", "saver-ponytail-levels",
		"saver-guard",
		"saver-headroom", "saver-headroom-url", "saver-headroom-timeout",
		"headroom-status", "headroom-manage-btn", "headroom-compress-user",
	} {
		if !strings.Contains(ui, `id="`+id+`"`) {
			t.Errorf("token saver control %q is missing", id)
		}
	}

	for _, fn := range []string{
		"function loadTokenSaver()",
		"function renderTokenSaver()",
		"function onTokenSaverToggle(",
		"function onLevelChange(",
		"function onHeadroomToggle(",
		"function refreshHeadroomStatus(",
		"function headroomAction(",
	} {
		if !strings.Contains(ui, fn) {
			t.Errorf("token saver handler %q is missing", fn)
		}
	}

	// The page has to be reachable: a nav button, a pane, and an entry in both
	// the runtime hash list and the pre-paint one (which is a separate copy and
	// would otherwise flash the wrong pane on refresh).
	if !strings.Contains(ui, `id="nav-token-saver"`) {
		t.Error("no sidebar button for the Token Saver page")
	}
	if !strings.Contains(ui, `id="tab-token-saver"`) {
		t.Error("no pane for the Token Saver page")
	}
	if !strings.Contains(ui, `if (tabId === "token-saver") loadTokenSaver();`) {
		t.Error("switchTab does not load the Token Saver page")
	}
	if n := strings.Count(ui, `"token-saver"`); n < 4 {
		t.Errorf(`"token-saver" appears %d times; the nav, both hash lists and the title table must know it`, n)
	}
}

// The level pickers must be built from the vocabulary the server reports, not a
// hardcoded list — that is what kept the old dropdowns offering "light" and
// "compact" for levels the prompt lookup never had.
func TestUILevelPickersComeFromServer(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "s.cavemanLevels") || !strings.Contains(ui, "s.ponytailLevels") {
		t.Error("the level pickers do not read the server's level vocabulary")
	}
	for _, stale := range []string{`value="light"`, `value="medium"`, `value="compact"`} {
		if strings.Contains(ui, stale) {
			t.Errorf("the removed level option %s is still in the markup", stale)
		}
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

	// The key list must still be intact — only the password block was removed.
	// The standalone Keys tab was later folded into Endpoint & Key, so the list
	// now lives there.
	for _, need := range []string{`id="tab-endpoint"`, "endpoint-keys-body", "function loadKeys()"} {
		if !strings.Contains(ui, need) {
			t.Errorf("removing the password form damaged the API key list: %q is gone", need)
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

	// The provider card counter shows the TOTAL connection count as plain text
	// under the provider name. It deliberately does not show an active/total
	// pill: the operator asked "how many accounts does this provider have?", and
	// the active subset is visible per-account once the card is expanded.
	if !strings.Contains(ui, "connection ${accounts.length}") {
		t.Error("provider card must label the total connection count as 'connection N'")
	}
	if strings.Contains(ui, "accounts.filter(a => a.isActive === 1).length") {
		t.Error("provider card still counts a connection as active just because its toggle is on")
	}

	// The account-row and expanded-list statuses must be derived, never read
	// straight from testStatus (a stale "unavailable" is not a broken account;
	// a missing one is not a failure at all). Every comparison now goes through
	// a normalised local, so no raw field comparison may appear at all.
	if n := strings.Count(ui, `p.testStatus === "active"`); n != 0 {
		t.Errorf("expected no direct testStatus comparison (it must go through a derived status), found %d", n)
	}
	// A never-probed connection has no status string to compare, so the helper
	// must branch on its absence rather than falling through to the failure case.
	if !strings.Contains(ui, `if (!status) return !p.hasActiveCooldown;`) {
		t.Error("isEffectivelyActive must treat a missing testStatus as not-yet-probed, not as a failure")
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
// served bytes: a custom endpoint's generated key
// ("openai-compatible-chat-<uuid>") must never be printed verbatim. Upstream
// shows node.name and keeps the id internal. The Overview chip list that first
// motivated this was retired, so the rule is now pinned where the label is
// still rendered: the provider detail hero.
func TestUINeverLabelsChipsWithGeneratedProviderKey(t *testing.T) {
	body := readEmbeddedUI(t)

	if !strings.Contains(body, "function niceProviderLabel(") {
		t.Fatal("niceProviderLabel helper missing from the served UI")
	}
	// The detail hero must resolve the display label through the helper rather
	// than interpolating the raw provider id.
	if !strings.Contains(body, "niceProviderLabel(d.provider)") {
		t.Fatal("the provider display label must be routed through niceProviderLabel")
	}
	// The retired Overview chips must not linger in the served bytes.
	if strings.Contains(body, "chip.innerText = `${provider}: ${count}`;") {
		t.Fatal("the raw provider key is still interpolated into a chip label")
	}
	if strings.Contains(body, "provider-type-chips") {
		t.Fatal("the retired Overview chips container is still in the served UI")
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

// TestUIAddConnectionIsAModal pins the upstream shape: a connection is added
// through a modal opened by a button, not an always-visible inline form. OmniRoute
// collects it in AddApiKeyModal, opened from the connections toolbar and — for a
// compatible endpoint — from the node card's own Add button.
func TestUIAddConnectionIsAModal(t *testing.T) {
	body := readEmbeddedUI(t)

	for _, want := range []string{
		`id="add-conn-modal"`,
		`id="addconn-provider"`,
		`id="acct-secret-label"`,
		`function openAddConnModal(`,
		`openAddConnModal('`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("embedded UI is missing %q", want)
		}
	}
	// The inline form must be gone: a second copy of the fields would leave two
	// elements sharing one id, and getElementById would return the wrong one.
	if strings.Contains(body, `add_link</span>Add Connection`) {
		t.Error("the inline add-connection form should be replaced by the modal")
	}
}

// A compatible endpoint is a node (it stores the base URL) AND a provider that
// needs credentials, so the toolbar must offer "Add API Key".
//
// An earlier version of this test asserted the opposite — that the button must
// be hidden when the provider is a node — and cited upstream as the reason.
// That reading was wrong: upstream renders the button for compatible providers
// and only changes the LABEL
// (providers/[id]/page.js: isCompatible ? "Add API Key" : "Add Connection"),
// passing isCompatible into the same AddApiKeyModal. Hiding it left the
// operator able to set a base URL but never an API key.
func TestUICompatibleNodeOffersAddApiKey(t *testing.T) {
	body := readEmbeddedUI(t)

	// The button must not be suppressed for a node.
	if strings.Contains(body, "&& !d.node;") {
		t.Error("showKeyForm still suppresses the Add button for a node, so a " +
			"compatible provider can never be given an API key")
	}
	if !strings.Contains(body, "const showKeyForm = oauthPanel === '';") {
		t.Error("showKeyForm must depend only on whether an OAuth panel already drives the flow")
	}

	// And it must be labelled for what it does, like upstream.
	if !strings.Contains(body, `const addConnLabel = d.node ? 'Add API Key' : 'Add Connection';`) {
		t.Error("the toolbar button must be labelled 'Add API Key' for a compatible node")
	}
	if !strings.Contains(body, `onclick="openAddConnModal('${d.provider}')"`) {
		t.Error("the toolbar button must still open the connection modal")
	}
}

// TestUIAddConnectionModalFollowsAuthType pins that the credential label is
// derived when the modal opens: a webCookie provider must ask for a Cookie, a
// no-auth provider for an Optional Token. Hardcoding "API Key" would mislabel the
// field for every cookie provider.
func TestUIAddConnectionModalFollowsAuthType(t *testing.T) {
	body := readEmbeddedUI(t)
	if !strings.Contains(body, `const isCookie = authType === "cookie";`) {
		t.Error("the modal must read the provider's auth type")
	}
	if !strings.Contains(body, `const secretLabel = isCookie ? "Cookie" : (authType === "none" ? "Optional Token" : "API Key");`) {
		t.Error("the credential label must switch on auth type")
	}
	if !strings.Contains(body, `document.getElementById("acct-secret-label").textContent = secretLabel;`) {
		t.Error("the derived label must be applied to the modal's field label")
	}
}

// TestUINoAuthCardSaysNoKeyNeeded pins the card counter for a synthesised
// no-auth provider. These cards own no connection row, so the usual
// "active/total" pill would render "0/0" — which reads as a broken provider
// rather than the keyless one it is. The card must say what is true instead.
func TestUINoAuthCardSaysNoKeyNeeded(t *testing.T) {
	body := readEmbeddedUI(t)
	if !strings.Contains(body, "head.noConnection") {
		t.Fatal("the card renderer must branch on noConnection; otherwise a keyless provider shows 'connection 0'")
	}
	// The branch must exist, must live on the card's sub-line, and must not emit
	// a numeric counter.
	noConnIdx := strings.Index(body, "const countLine = head.noConnection")
	if noConnIdx == -1 {
		t.Fatal("expected the card sub-line to be chosen by a noConnection branch")
	}
	if !strings.Contains(body[noConnIdx:noConnIdx+500], "no key needed") {
		t.Error("a no-auth card must label itself 'no key needed'")
	}
	// Scope to the noConnection arm: from the branch to the `: \`` that starts
	// the numeric fallback. Without this the window would spill into the other
	// arm and see its counter.
	arm := body[noConnIdx:]
	if end := strings.Index(arm, ": `"); end != -1 {
		arm = arm[:end]
	}
	if strings.Contains(arm, "accounts.length}") {
		t.Error("the no-auth branch must not fall through to a numeric counter")
	}
}

// TestUIDetailHeroUsesNoConnection pins the same honesty on the provider detail
// page: the hero counter reads "0/0 active" for a keyless provider, which is the
// number a broken provider shows. It must branch on the payload's noConnection.
func TestUIDetailHeroUsesNoConnection(t *testing.T) {
	body := readEmbeddedUI(t)
	idx := strings.Index(body, "${d.noConnection")
	if idx == -1 {
		t.Fatal("the detail hero must branch on d.noConnection; otherwise a keyless provider reads 0/0 active")
	}
	arm := body[idx:]
	if end := strings.Index(arm, ": `"); end != -1 {
		arm = arm[:end]
	}
	if !strings.Contains(arm, "no key needed") {
		t.Error("the detail hero must label a keyless provider 'no key needed'")
	}
	if strings.Contains(arm, "activeCount}/${d.totalCount}") {
		t.Error("the no-auth arm must not fall through to the numeric counter")
	}
}

// The Proxy Routing tab is the only configurable surface a keyless provider has
// (it owns no connection row), so it must be gated on the same noConnection flag
// the hero uses — never shown for providers that have connections.
func TestUIProxyTabGatedOnNoConnection(t *testing.T) {
	body := readEmbeddedUI(t)
	idx := strings.Index(body, "const showProxyTab")
	if idx == -1 {
		t.Fatal("renderProviderDetail must gate the Proxy tab on showProxyTab")
	}
	arm := body[idx:]
	if end := strings.Index(arm, "\n"); end != -1 {
		arm = arm[:end]
	}
	if !strings.Contains(arm, "d.noConnection") {
		t.Errorf("the Proxy tab must be gated on d.noConnection, got: %s", arm)
	}
}

// The Proxy tab must hide itself inside showDetailTab's panel list, or switching
// away from it would leave the panel visible.
func TestUIProxyPanelRegisteredInTabSwitcher(t *testing.T) {
	body := readEmbeddedUI(t)
	// The panel must exist in the markup.
	if !regexp.MustCompile(`id="dt-proxy"`).MatchString(body) {
		t.Fatal("the Proxy panel id dt-proxy must exist")
	}
	// It must be hidden by the switcher. Asserted as "the switcher hides the
	// whole dt- family" rather than "the switcher contains this literal id":
	// the previous form pinned a hand-written array, and that array was the bug
	// — it omitted dt-quota, so the quota panel leaked onto other tabs. Any
	// id-list assertion has the same failure mode, so the property under test is
	// the DOM-derived selector, not membership of a list.
	switcher := functionBody(t, body, "function showDetailTab(")
	if !strings.Contains(switcher, `[id^="dt-"]`) {
		t.Errorf("showDetailTab must hide every dt- panel, got:\n%s", switcher)
	}
}

// Rotation state must round-trip through the server: the card writes the full
// providerStrategies map, and targetProxyPoolIds carries the narrowed set.
func TestUIProxyCardSavesProviderStrategies(t *testing.T) {
	body := readEmbeddedUI(t)
	for _, want := range []string{
		"renderNoAuthProxyCard",
		"saveNoAuthProxyStrategy",
		"targetProxyPoolIds",
		"/api/dashboard/proxy-pools",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("UI must reference %q for proxy routing", want)
		}
	}
}

// A non-none strategy makes the single static pool selection inert, so the
// select must be disabled to avoid implying it still applies.
func TestUIProxyStaticPoolDisabledWhileRotating(t *testing.T) {
	body := readEmbeddedUI(t)
	idx := strings.Index(body, `id="proxy-pool"`)
	if idx == -1 {
		t.Fatal("the static pool select must have id proxy-pool")
	}
	arm := body[idx:]
	if end := strings.Index(arm, ">"); end != -1 {
		arm = arm[:end]
	}
	if !strings.Contains(arm, `strategy === "none"`) {
		t.Errorf("the static pool select must be disabled while a rotation strategy is active, got: %s", arm)
	}
}

// Changing the strategy must reveal the eligible-pool checkboxes, which only
// exist for rotating strategies. Toggling the select alone would leave the
// operator staring at a stale card until a manual reload.
func TestUIProxyStrategyChangeRerenders(t *testing.T) {
	body := readEmbeddedUI(t)
	idx := strings.Index(body, "async function onProxyStrategyChange")
	if idx == -1 {
		t.Fatal("onProxyStrategyChange must exist")
	}
	arm := body[idx:]
	if end := strings.Index(arm, "\n  }"); end != -1 {
		arm = arm[:end]
	}
	if !strings.Contains(arm, "renderProviderDetail(lastProviderDetail)") {
		t.Errorf("changing the strategy must re-render the detail panel, got: %s", arm)
	}
}

// The eligible-pool list is what makes targetProxyPoolIds editable, so it must be
// rendered whenever a rotating strategy is active.
func TestUIProxyCardRendersTargetCheckboxes(t *testing.T) {
	body := readEmbeddedUI(t)
	if !strings.Contains(body, "data-pool-target") {
		t.Fatal("the Proxy card must render per-pool target checkboxes")
	}
	idx := strings.Index(body, `strategy === "none" ? "" :`)
	if idx == -1 {
		t.Error("the target checkbox block must be gated on a rotating strategy")
	}
}

// The "Add OpenAI/Anthropic Compatible" buttons must survive every filter state.
//
// They used to be rendered INSIDE the section header, and the header is skipped
// when the view narrows to one section (showSectionHeaders is false unless the
// category is "all"). Filtering to the custom category therefore removed the
// only way to create a compatible provider — exactly when the operator was
// looking at that category in order to add one.
func TestUICompatButtonsSurviveASingleSectionFilter(t *testing.T) {
	body := readEmbeddedUI(t)

	// The buttons must be built as their own block, outside the header ternary.
	if !strings.Contains(body, `const customActions = key === "custom"`) {
		t.Fatal("the Add Compatible buttons are not a standalone block; nesting them " +
			"in the header makes them disappear whenever the header is hidden")
	}
	// ...and that block must be rendered in the section body, not only in the header.
	sectionStart := strings.Index(body, `<section class="prov-section" data-section=`)
	if sectionStart == -1 {
		t.Fatal("provider section markup not found")
	}
	section := body[sectionStart:]
	if end := strings.Index(section, "</section>"); end != -1 {
		section = section[:end]
	}
	if !strings.Contains(section, "${customActions}") {
		t.Error("the section must render customActions outside the header branch")
	}

	// Both buttons must still be present and wired to their modals.
	for _, want := range []string{
		`openCompatModal('openai')`,
		`openCompatModal('anthropic')`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing Add Compatible button: %s", want)
		}
	}
}

// TestUIAddConnectionModalHasBulkMode verifies the Add Connection modal has
// both Single and Bulk Add modes, with collision-free bulk planning.
func TestUIAddConnectionModalHasBulkMode(t *testing.T) {
	body := readEmbeddedUI(t)

	// Mode switcher buttons
	if !strings.Contains(body, `id="conn-mode-switcher"`) {
		t.Error("missing conn-mode-switcher in Add Connection modal")
	}
	if !strings.Contains(body, `setConnMode('single')`) || !strings.Contains(body, `setConnMode('bulk')`) {
		t.Error("missing setConnMode handlers in Add Connection modal")
	}

	// Bulk textarea and hint
	if !strings.Contains(body, `id="acct-bulk-text"`) {
		t.Error("missing acct-bulk-text textarea in Add Connection modal")
	}
	if !strings.Contains(body, "planBulkAdd(") {
		t.Error("missing planBulkAdd planner function")
	}
	if !strings.Contains(body, "addBulkConnections(") {
		t.Error("missing addBulkConnections function")
	}
}

// TestUIModelImportSelectedOnly ensures that the model import modal tracks
// selection explicitly via selectedIds Set and only imports selected models.
func TestUIModelImportSelectedOnly(t *testing.T) {
	body := readEmbeddedUI(t)

	if !strings.Contains(body, "importState.selectedIds.has(m.id)") {
		t.Error("commitImport must filter exclusively by importState.selectedIds")
	}
	if !strings.Contains(body, "updateImportSelectedUI") {
		t.Error("missing updateImportSelectedUI function")
	}
	if !strings.Contains(body, "onImportCbChange") {
		t.Error("missing onImportCbChange handler")
	}
}

// TestUIModelDisableNoConfirmAndTwoModeAutoDisable ensures that removeModel has no
// blocking confirm() popup and auto-disable supports safe and full modes.
func TestUIModelDisableNoConfirmAndTwoModeAutoDisable(t *testing.T) {
	body := readEmbeddedUI(t)

	// Confirm prompt must be gone from removeModel
	if strings.Contains(body, "Remove model \"${modelId}\"?") {
		t.Error("confirmation prompt should be removed from removeModel")
	}

	// 2-mode auto disable selector
	if !strings.Contains(body, `id="test-auto-disable-mode"`) {
		t.Error("missing test-auto-disable-mode select dropdown")
	}
	if !strings.Contains(body, `value="safe"`) || !strings.Contains(body, `value="full"`) {
		t.Error("missing safe and full options in test-auto-disable-mode")
	}
}
