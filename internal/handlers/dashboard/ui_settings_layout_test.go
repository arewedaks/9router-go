package dashboard

import (
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

	// Exactly one card between the Backup heading and the end of the pane.
	start := strings.Index(pane, "💾 Backup & Restore")
	if start < 0 {
		t.Fatal("the Backup & Restore group is missing")
	}
	tail := pane[start:]
	// Stop at the pane's end so a later tab's cards are not counted.
	if end := strings.Index(tail, "<!-- TOKEN SAVER TAB -->"); end > 0 {
		tail = tail[:end]
	}
	if n := strings.Count(tail, `class="card"`); n != 1 {
		t.Errorf("Backup & Restore renders %d cards, want 1: download and restore "+
			"are the same topic and belong in one card", n)
	}
	for _, need := range []string{"Download Backup", "Restore From Backup"} {
		if !strings.Contains(tail, need) {
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
