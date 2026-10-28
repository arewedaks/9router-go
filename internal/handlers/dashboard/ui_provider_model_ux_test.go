package dashboard

import (
	"strings"
	"testing"
)

// TestUIProviderAndModelUX verifies that Provider & Model management exposes
// real-time search, category chips, status filters, and copy helpers.
func TestUIProviderAndModelUX(t *testing.T) {
	ui := readEmbeddedUI(t)

	// Model filter toolbar & state in Available Models sub-tab
	modelControls := []string{
		`id="model-search-box"`,
		`id="model-search-clear-btn"`,
		`id="model-visible-count"`,
		`id="models-list-container"`,
		`id="models-none-matched"`,
		`class="model-filter-bar"`,
		`class="mfilter-chip`,
		`function applyModelFilters()`,
		`function handleModelSearch(`,
		`function clearModelSearch()`,
		`function setModelFilterCategory(`,
		`function copyFilteredModelIDs(`,
	}
	for _, need := range modelControls {
		if !strings.Contains(ui, need) {
			t.Errorf("missing expected model management control/handler: %s", need)
		}
	}

	// Status badge & token pill visual classes
	visuals := []string{
		`model-status-badge`,
		`token-pill`,
		`prov-status-indicator`,
	}
	for _, need := range visuals {
		if !strings.Contains(ui, need) {
			t.Errorf("missing expected visual styling token: %s", need)
		}
	}

	// Providers overview quick status filter bar
	statusFilters := []string{
		`id="prov-status-filters"`,
		`id="f-status-all"`,
		`id="f-status-connected"`,
		`id="f-status-active"`,
		`id="f-status-unconfigured"`,
		`function setProviderStatusFilter(`,
	}
	for _, need := range statusFilters {
		if !strings.Contains(ui, need) {
			t.Errorf("missing expected provider status filter: %s", need)
		}
	}

	// Quota tab must only be shown for OAuth & CLI session providers
	quotaChecks := []string{
		`const showQuotaTab = d.category === "oauth" || d.authType === "oauth";`,
		`${quotaTabBtn}`,
	}
	for _, need := range quotaChecks {
		if !strings.Contains(ui, need) {
			t.Errorf("missing expected quota tab gating: %s", need)
		}
	}

	// Account rotation strategy card in Connections tab
	stratChecks := []string{
		`class="acct-strat-card"`,
		`class="acct-strat-title"`,
		`class="acct-strat-select"`,
		`acct-count-chip`,
	}
	for _, need := range stratChecks {
		if !strings.Contains(ui, need) {
			t.Errorf("missing expected account strategy styling: %s", need)
		}
	}

	// Unified connections header card
	headerChecks := []string{
		`class="conn-header-card"`,
		`class="conn-header-divider"`,
		`class="conn-select-bar"`,
	}
	for _, need := range headerChecks {
		if !strings.Contains(ui, need) {
			t.Errorf("missing expected unified connections header card element: %s", need)
		}
	}

	// Mobile simplification (icon-only text wrappers)
	mobileChecks := []string{
		`btn-text`,
		`dtab-text`,
		`.conn-test-bar .btn .btn-text`,
	}
	for _, need := range mobileChecks {
		if !strings.Contains(ui, need) {
			t.Errorf("missing expected mobile icon-only element: %s", need)
		}
	}

	// Touch and hold / press tooltip popup
	holdChecks := []string{
		`app-tooltip-popup`,
		`initHoldTooltips`,
		`showTooltip(`,
	}
	for _, need := range holdChecks {
		if !strings.Contains(ui, need) {
			t.Errorf("missing expected hold tooltip feature: %s", need)
		}
	}

	// Special compatible hero cards in Add Provider modal
	pickerHeroChecks := []string{
		`picker-compat-box`,
		`compat-openai`,
		`compat-anthropic`,
		`picker-filter-chips`,
		`pfilter-chip`,
		`setPickerCategoryFilter(`,
	}
	for _, need := range pickerHeroChecks {
		if !strings.Contains(ui, need) {
			t.Errorf("missing expected special picker component: %s", need)
		}
	}
}
