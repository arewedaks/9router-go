package providers

import "testing"

func boolPtr(b bool) *bool { return &b }

// TestProviderHasFreeModels_ResolvesAliases guards that a caller may pass
// either the canonical id or a short alias; the badge must not depend on which.
func TestProviderHasFreeModels_ResolvesAliases(t *testing.T) {
	cases := map[string]bool{
		"antigravity": true,
		"ag":          true,
		"gemini":      true,
		"glm":         true,
		// CodeBuddy has no documented free tier (absent from OmniRoute's
		// catalog), so it must NOT report free.
		"codebuddy-cn":   false,
		"codebuddy-intl": false,
		"cbai":           false,
		"":               false,
	}
	for provider, want := range cases {
		if got := ProviderHasFreeModels(provider); got != want {
			t.Errorf("ProviderHasFreeModels(%q) = %v, want %v", provider, got, want)
		}
	}
}

// TestIsFreeModel_CatalogEntry covers the "listed in the shipped catalog" arm.
func TestIsFreeModel_CatalogEntry(t *testing.T) {
	if !IsFreeModel("antigravity", FreeModelCandidate{ID: "claude-sonnet-4-6"}) {
		t.Error("antigravity/claude-sonnet-4-6 is catalogued free but was not reported")
	}
	if !IsFreeModel("ag", FreeModelCandidate{ID: "gemini-pro-agent"}) {
		t.Error("alias lookup failed for a catalogued model")
	}
	// A model the catalog does not list, on a provider that has a free tier but
	// no payload signal, must not be free.
	if IsFreeModel("antigravity", FreeModelCandidate{ID: "some-paid-model"}) {
		t.Error("uncatalogued model without a payload signal was reported free")
	}
	// A provider with no free tier is never free, even with a payload signal.
	if IsFreeModel("codebuddy-intl", FreeModelCandidate{ID: "glm-5.2", IsFree: boolPtr(true)}) {
		t.Error("provider without a free tier honoured a payload signal")
	}
}

// TestIsFreeModel_PayloadSignals covers the three signals allowed only on a
// provider that has a documented free tier.
func TestIsFreeModel_PayloadSignals(t *testing.T) {
	if !IsFreeModel("openrouter", FreeModelCandidate{ID: "x/y:free"}) {
		t.Error("`:free` suffix was not honoured on a free-tier provider")
	}
	if !IsFreeModel("gemini", FreeModelCandidate{ID: "whatever", IsFree: boolPtr(true)}) {
		t.Error("isFree:true was not honoured on a free-tier provider")
	}
	if !IsFreeModel("gemini", FreeModelCandidate{ID: "whatever", PromptPrice: "0", CompletionPrice: "0.0"}) {
		t.Error("zero prices were not honoured on a free-tier provider")
	}
	// Either price non-zero is paid.
	if IsFreeModel("gemini", FreeModelCandidate{ID: "whatever", PromptPrice: "0", CompletionPrice: "1.5"}) {
		t.Error("a non-zero completion price was ignored")
	}
}

// TestFreeModelZeroPrice_RejectsNonNumeric guards the tiny parser against
// treating garbage as zero.
func TestFreeModelZeroPrice_RejectsNonNumeric(t *testing.T) {
	for _, in := range []string{"0", "0.0", "0.000000", "00"} {
		if !zeroPrice(in) {
			t.Errorf("zeroPrice(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"", "abc", "0.1", "1", "-0", "0x0"} {
		if zeroPrice(in) {
			t.Errorf("zeroPrice(%q) = true, want false", in)
		}
	}
}

// TestIsModelFreeBadge_DefaultRule mirrors upstream's non-strict badge rule.
func TestIsModelFreeBadge_DefaultRule(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    FreeModelCandidate
		want     bool
	}{
		{"catalogued", "antigravity", FreeModelCandidate{ID: "claude-sonnet-4-6"}, true},
		{"display name says free", "codebuddy-intl", FreeModelCandidate{ID: "x", DisplayName: "Foo (Free)"}, true},
		{"display name says gratis", "codebuddy-intl", FreeModelCandidate{ID: "x", DisplayName: "Grátis"}, true},
		{"free word inside another word does not count", "codebuddy-intl", FreeModelCandidate{ID: "x", DisplayName: "Freeform"}, false},
		{"truthy free field", "codebuddy-intl", FreeModelCandidate{ID: "x", Free: true}, true},
		{"string false is not a signal", "codebuddy-intl", FreeModelCandidate{ID: "x", Free: "false"}, false},
		{"paid catalogued provider", "codebuddy-intl", FreeModelCandidate{ID: "glm-5.2"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsModelFreeBadge(tc.provider, tc.model); got != tc.want {
				t.Errorf("IsModelFreeBadge(%q, %+v) = %v, want %v", tc.provider, tc.model, got, tc.want)
			}
		})
	}
}

// TestFreeBudget_DiscontinuedDoesNotGrantAccess guards that a retired regime is
// filtered out when the lookup tables are built.
func TestFreeBudget_DiscontinuedDoesNotGrantAccess(t *testing.T) {
	if freeBudgetGrantsAccess(regimeDiscontinued) {
		t.Fatal("discontinued regime must not grant free access")
	}
	// The catalog literal must contain no discontinued entries we would wrongly
	// surface; this asserts the source list stays clean.
	for _, entry := range freeModelCatalog {
		if entry.FreeType == regimeDiscontinued {
			t.Errorf("catalog entry %q/%q is discontinued; it must not be shipped as free", entry.Provider, entry.ModelID)
		}
	}
}
