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
