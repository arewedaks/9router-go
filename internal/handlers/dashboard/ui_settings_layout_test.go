package dashboard

import (
	"regexp"
	"strings"
	"testing"
)

// The Settings tab used to spend one full card per switch, so a group of two
// related toggles read as three unrelated settings and the page ran to nine
// cards. Toggles that answer the same question now share a card as rows.
//
// This pins the shape loosely -- the row wrapper and the shared card -- so a
// later edit cannot quietly reintroduce the one-card-per-toggle sprawl without
// a reviewer noticing. It deliberately does not count cards: the count is a
// layout detail, the grouping is the decision.
func TestRelatedProxyTogglesShareOneCard(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `class="proxy-row"`) {
		t.Error("the proxy toggles are not laid out as rows, so each one is " +
			"back to needing a card of its own")
	}
	// Both switches must sit inside a row wrapper rather than directly in a card.
	for _, id := range []string{"set-trust-proxy", "set-cookie-secure"} {
		marker := `id="` + id + `"`
		idx := strings.Index(ui, marker)
		if idx < 0 {
			t.Fatalf("%s is missing from the dashboard", id)
		}
		// Look back a short way for the row wrapper that should enclose it.
		start := idx - 400
		if start < 0 {
			start = 0
		}
		if !strings.Contains(ui[start:idx], "proxy-row-head") {
			t.Errorf("%s is not inside a proxy row header, so the two proxy "+
				"switches are no longer grouped together", id)
		}
	}
}

// Ids are the contract between the markup and every handler that reads them.
// Restructuring the cards must not touch them, or the page renders and silently
// stops saving.
func TestSettingsIDsSurviveTheLayoutChange(t *testing.T) {
	ui := readEmbeddedUI(t)

	ids := []string{
		"set-trust-proxy", "set-cookie-secure",
		"set-listen-host", "set-listen-port",
		"set-bind-warn", "set-trust-proxy-warn", "cf-msg",
		"set-cur-pw", "set-new-pw", "set-confirm-pw",
		"pw-change-btn", "pw-reset-btn", "pw-change-msg", "pw-reset-msg",
	}
	for _, id := range ids {
		if n := strings.Count(ui, `id="`+id+`"`); n != 1 {
			t.Errorf("id %q appears %d times, want exactly 1: the layout change "+
				"must not drop or duplicate an element the handlers look up", id, n)
		}
	}
}

// TestSettingsUseTwoColumnsOnDesktop: a single column of full-width cards
// stretched each row of controls across the whole window. The grid is what
// keeps the page readable on a desktop, so losing it is a regression even
// though nothing breaks functionally.
func TestSettingsUseTwoColumnsOnDesktop(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "grid-template-columns: repeat(2, minmax(0, 1fr))") {
		t.Error("the settings grid no longer asks for two columns")
	}
	// Narrow windows have to fall back to one column or the cards crush.
	if !strings.Contains(ui, "@media (max-width: 980px) { .settings-grid") {
		t.Error("the two-column grid has no narrow-window fallback")
	}
	// Every group shares one grid, and each heading spans both columns. A grid
	// per group looked right in isolation but started the columns over at every
	// heading, so the cards never lined up across the page.
	idx := strings.Index(ui, `id="tab-settings"`)
	end := strings.Index(ui[idx:], "<!-- TOKEN SAVER TAB -->")
	if end < 0 {
		t.Fatal("could not locate the end of the settings pane")
	}
	pane := ui[idx : idx+end]

	for _, group := range []string{"🔐 Security", "🌐 Cloudflare / Reverse Proxy", "💾 Backup & Restore"} {
		if !strings.Contains(pane, group) {
			t.Fatalf("group %q is missing from the settings pane", group)
		}
	}
	if n := strings.Count(pane, `class="settings-grid"`); n != 1 {
		t.Errorf("the pane opens %d grids, want 1: one grid keeps the rows aligned "+
			"across groups", n)
	}
	if !strings.Contains(ui, "grid-column: 1 / -1;") {
		t.Error("the group headings no longer span both columns, so a heading "+
			"would render beside the first card instead of above the group")
	}
}

// TestBackupAndRestoreShareOneCard: the two directions were a card each, which
// gave a rarely used pair as much room as the settings an operator actually
// changes. They are one card with a row per direction.
func TestBackupAndRestoreShareOneCard(t *testing.T) {
	ui := readEmbeddedUI(t)

	idx := strings.Index(ui, `id="tab-settings"`)
	pane := ui[idx:]

	// Both directions live in one card. The card is the element that opens
	// before the label, not everything after it, so count the rows inside the
	// card that carries the label.
	label := strings.Index(pane, "💾 Backup & Restore")
	if label < 0 {
		t.Fatal("the Backup & Restore group is missing")
	}
	cardOpen := strings.LastIndex(pane[:label], `<div class="card">`)
	if cardOpen < 0 {
		t.Fatal("the Backup & Restore label is not inside a card")
	}
	// Walk forward to the matching close by tracking div depth.
	depth := 0
	cardEnd := -1
	for _, m := range regexp.MustCompile(`<div\b|</div>`).FindAllStringIndex(pane[cardOpen:], -1) {
		if strings.HasPrefix(pane[cardOpen+m[0]:cardOpen+m[1]], "</div>") {
			depth--
			if depth == 0 {
				cardEnd = cardOpen + m[1]
				break
			}
		} else {
			depth++
		}
	}
	if cardEnd < 0 {
		t.Fatal("could not find the end of the Backup & Restore card")
	}
	card := pane[cardOpen:cardEnd]

	if n := strings.Count(card, `class="card-row"`); n != 2 {
		t.Errorf("the Backup & Restore card has %d rows, want 2 (download and "+
			"restore are the same topic and share one card)", n)
	}
	for _, need := range []string{"Download Backup", "Restore From Backup"} {
		if !strings.Contains(card, need) {
			t.Errorf("the merged card is missing the %q row", need)
		}
	}
}

// TestPasswordPromptsLiveInModals: the password inputs were inline, so the
// Settings page opened as a wall of fields for actions the operator rarely
// takes. They belong in a modal, opened only when the action is chosen.
func TestPasswordPromptsLiveInModals(t *testing.T) {
	ui := readEmbeddedUI(t)

	idx := strings.Index(ui, `id="tab-settings"`)
	if idx < 0 {
		t.Fatal("the settings pane is missing")
	}
	// The pane ends at </main>; a modal defined outside it is what we want.
	mainEnd := strings.Index(ui[idx:], "</main>")
	if mainEnd < 0 {
		t.Fatal("could not locate the end of the settings pane")
	}
	pane := ui[idx : idx+mainEnd]

	for _, field := range []string{`id="set-cur-pw"`, `id="backup-pw"`, `id="restore-pw"`} {
		if strings.Contains(pane, field) {
			t.Errorf("%s is still inline in the Settings pane; password prompts "+
				"belong in a modal so the page stays simple", field)
		}
		if !strings.Contains(ui, field) {
			t.Errorf("%s was dropped entirely instead of moved into a modal", field)
		}
	}

	// Each prompt is opened by a control the Settings page actually renders.
	for _, pair := range []struct{ opener, modal string }{
		{`onclick="openPwModal()"`, `id="pw-modal"`},
		{`onclick="openBackupModal()"`, `id="backup-modal"`},
		{`onclick="openRestoreModal()"`, `id="restore-modal"`},
	} {
		if !strings.Contains(pane, pair.opener) {
			t.Errorf("the Settings page has no control opening %s", pair.modal)
		}
		if !strings.Contains(ui, pair.modal) {
			t.Errorf("%s is missing from the dashboard", pair.modal)
		}
	}
}

// TestSecurityAndBackupShareOneRow: Security is one short card under a
// full-width heading, which left the whole right half of the row empty while
// Backup started a fresh row below. The two cards sit side by side instead, so
// each group's label lives inside its own card.
func TestSecurityAndBackupShareOneRow(t *testing.T) {
	ui := readEmbeddedUI(t)

	idx := strings.Index(ui, `id="tab-settings"`)
	end := strings.Index(ui[idx:], "<!-- TOKEN SAVER TAB -->")
	if end < 0 {
		t.Fatal("could not locate the end of the settings pane")
	}
	pane := ui[idx : idx+end]

	// A spanning heading between the two would force Backup onto its own row, so
	// neither label may be a standalone heading.
	for _, label := range []string{"🔐 Security", "💾 Backup & Restore"} {
		i := strings.Index(pane, label)
		if i < 0 {
			t.Fatalf("label %q is missing", label)
		}
		if strings.Contains(pane[:i], "settings-group-title") &&
			strings.LastIndex(pane[:i], "settings-group-title") > strings.LastIndex(pane[:i], "<div") {
			t.Errorf("%q is still a spanning group heading, so its card cannot "+
				"share a row with the card beside it", label)
		}
		if !strings.Contains(pane[i:i+200], "card-row-title") {
			t.Errorf("%q is not rendered as a card title", label)
		}
	}
	// Only Cloudflare keeps a spanning heading, so exactly one remains.
	if n := strings.Count(pane, "settings-group-title"); n != 1 {
		t.Errorf("the pane has %d spanning headings, want 1 (Cloudflare); the "+
			"others moved into their cards to sit side by side", n)
	}
}

// TestSettingsButtonsShareOneSize: the row controls each sized to their own
// label, so a card showed buttons of 66px, 78px and 90px with the icon pair a
// pixel taller than the rest. One width and one height makes the controls line
// up as a column, which is what "rapi" means here.
func TestSettingsButtonsShareOneSize(t *testing.T) {
	ui := readEmbeddedUI(t)

	// Icon buttons were taller because the glyph added line height; a min-height
	// on the shared class is what levels them.
	if !strings.Contains(ui, "min-height: 36px") {
		t.Error("buttons have no min-height, so an icon button renders taller " +
			"than a text-only one")
	}
	if !strings.Contains(ui, ".card-row > .btn { min-width: 104px; flex: 0 0 auto; }") {
		t.Error("row controls have no shared width, so each button sizes to its " +
			"own label and the card edge looks ragged")
	}
	// A two-action row splits evenly, so neither button reads as primary merely
	// because its label is longer.
	if !strings.Contains(ui, ".btn-pair { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr))") {
		t.Error("the Cloudflare action pair is not split evenly")
	}
	// The pair markup has to use that class, or the rule does nothing.
	idx := strings.Index(ui, "applyCloudflarePreset()")
	if idx < 0 {
		t.Fatal("the Cloudflare preset button is missing")
	}
	if !strings.Contains(ui[idx-200:idx], `class="btn-pair"`) {
		t.Error("the Cloudflare buttons are not wrapped in .btn-pair")
	}
	if strings.Contains(ui, `style="display:flex; gap:8px; margin-top:14px; flex-wrap:wrap;"`) {
		t.Error("the Cloudflare pair still uses the ad-hoc flex wrapper the " +
			"rule was meant to replace")
	}
}

// TestSettingsCardsEndOnOneLine: the message slots kept an 18px min-height plus
// a 10px margin even while empty, so a card with one ended 28px of blank space
// below its buttons and the two cards in a row stopped matching. Empty slots
// must collapse, and a stretched card must push its action block to the bottom
// edge so both cards in a row finish on the same line.
func TestSettingsCardsEndOnOneLine(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, ".settings-msg:empty { min-height: 0; margin-bottom: 0; }") {
		t.Error("an empty message slot still reserves space, so cards with one " +
			"end with a hole and row bottoms stop lining up")
	}
	if !strings.Contains(ui, "align-items: stretch;") {
		t.Error("the settings grid does not stretch cards, so the shorter card " +
			"in a row stops early and the bottom edge looks ragged")
	}
	if !strings.Contains(ui, ".card > .btn-pair:last-child { margin-top: auto; }") {
		t.Error("the action pair is not pushed to the card bottom, so two cards " +
			"of different content height do not finish their actions on one line")
	}
	// The action pair has to be the last element of its card, or the auto margin
	// lands on the wrong block and the buttons hang mid-card.
	for _, card := range []string{"applyCloudflarePreset()", "showCloudflaredConfig()"} {
		i := strings.Index(ui, card)
		if i < 0 {
			t.Fatalf("%s is missing", card)
		}
		if !strings.Contains(ui[i:i+300], `</div>`+"\n    </div>") &&
			!strings.Contains(ui[i:i+400], `.settings-msg`) {
			t.Errorf("the buttons for %s are not at the end of their card", card)
		}
	}
}

// TestSettingsPaneClosesBeforeTheNextPane: the settings pane once swallowed the
// three panes after it -- its closing </div> sat at the end of <main> instead of
// before tab-proxies. Total div counts still balanced, so a count check passed
// while navigateTab('console') hid the whole chain: turning settings inactive
// set display:none on the pane that *contained* the console. Each pane must be a
// sibling, so the depth has to return to zero before the next pane opens.
func TestSettingsPaneClosesBeforeTheNextPane(t *testing.T) {
	ui := readEmbeddedUI(t)

	panes := regexp.MustCompile(`<div id="tab-([a-z-]+)" class="tab-pane">`).FindAllStringSubmatch(ui, -1)
	if len(panes) < 2 {
		t.Fatal("no tab panes found")
	}
	for i := 0; i < len(panes)-1; i++ {
		a, b := panes[i][1], panes[i+1][1]
		start := strings.Index(ui, `<div id="tab-`+a) + len(`<div id="tab-`+a)
		end := strings.Index(ui, `<div id="tab-`+b)
		if end < 0 {
			t.Fatalf("pane %q not found", b)
		}
		depth := 1
		for _, m := range regexp.MustCompile(`<div\b|</div>`).FindAllString(ui[start:end], -1) {
			if strings.HasPrefix(m, "</div>") {
				depth--
			} else {
				depth++
			}
		}
		if depth != 0 {
			t.Errorf("tab-%s is still open (depth %d) when tab-%s opens: %s swallows %s, "+
				"so hiding %s also hides %s", a, depth, b, a, b, a, b)
		}
	}
}

// TestNoNativeBrowserDialogs: window.confirm/alert/prompt render browser chrome,
// are suppressed when a page is not focused, and cannot be styled. The dashboard
// replaces them with the in-app dialog (appConfirm/appAlert/appPrompt) and
// non-blocking toasts, so a remaining native call is a regression.
func TestNoNativeBrowserDialogs(t *testing.T) {
	ui := readEmbeddedUI(t)

	native := regexp.MustCompile(`(?m)^(\s*)(?:window\.)?(?:alert|confirm|prompt)\s*\(`)
	for _, m := range native.FindAllStringSubmatchIndex(ui, -1) {
		line := ui[m[0]:m[1]]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue // prose about the dialogs, not a call
		}
		// The in-app wrappers legitimately contain those words in their names.
		if strings.Contains(line, "appConfirm") || strings.Contains(line, "appAlert") || strings.Contains(line, "appPrompt") {
			continue
		}
		t.Errorf("native browser dialog at: %s -- use appConfirm/appAlert/appPrompt or toast", trimmed)
	}

	// The helpers the replacement relies on must exist.
	for _, need := range []string{"function appConfirm(", "function appAlert(", "function appPrompt(", "function toast("} {
		if !strings.Contains(ui, need) {
			t.Errorf("missing helper %s", need)
		}
	}
}
