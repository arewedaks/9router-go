package providers

import (
	"slices"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

// TestProviderHasFreeModels_ResolvesAliases guards that a caller may pass
// either the canonical id or a short alias; the badge must not depend on which.
func TestProviderHasFreeModels_ResolvesAliases(t *testing.T) {
	cases := map[string]bool{
		"antigravity": true,
		"ag":          true,
		"gemini":      true,
		"glm":         true,
		// OpenCode is a keyless free tier (auth "none", default key "public"),
		// so both its canonical id and its "oc" alias must report free.
		"opencode": true,
		"oc":       true,
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

// TestOpenCodeFreeTier_OnlyFreeIdsBadge pins the OpenCode ("oc") free tier:
// the provider must be known to grant free access, its "-free" models must
// badge, and its paid catalogue must not.
//
// The "-free" suffix is deliberately NOT a payload signal (only OpenRouter's
// ":free" is), so a passing badge here can only come from the shipped
// catalogue — which is exactly the wiring that regressed for this provider.
func TestOpenCodeFreeTier_OnlyFreeIdsBadge(t *testing.T) {
	// Only ids confirmed reachable by an actual chat call. deepseek-v4-flash-free
	// and jev-1.13-free are listed by /zen/v1/models but answer 400 "Model is
	// unavailable" and 500 respectively, so advertising them would make the
	// router's own catalogue the source of a guaranteed failure.
	freeIDs := []string{
		"big-pickle",
		"mimo-v2.5-free",
		"mimo-v2.6-flash-free",
		"ling-3.0-flash-fin-free",
		"nemotron-3-ultra-free",
		"nemotron-3.5-lightning-free",
		"space-bunny-free",
		"muse-spark-1.3-contributor-free",
		"muse-spark-1.2-contributor-free",
	}

	// A model the chat endpoint rejects must not be advertised as free, even
	// though the listing endpoint returns it.
	for _, dead := range []string{"deepseek-v4-flash-free", "jev-1.13-free"} {
		if IsFreeModel("opencode", FreeModelCandidate{ID: dead}) {
			t.Errorf("IsFreeModel(opencode, %q) = true, but the chat endpoint rejects that id", dead)
		}
	}
	for _, id := range freeIDs {
		// Canonical id and alias must agree.
		for _, provider := range []string{"opencode", "oc"} {
			if !IsFreeModel(provider, FreeModelCandidate{ID: id}) {
				t.Errorf("IsFreeModel(%q, %q) = false, want true (catalogued keyless free)", provider, id)
			}
			if !IsModelFreeBadge(provider, FreeModelCandidate{ID: id, DisplayName: id}) {
				t.Errorf("IsModelFreeBadge(%q, %q) = false, want true", provider, id)
			}
		}
	}

	// A paid OpenCode model must not be badged, even though the provider has a
	// documented free tier. These ids are live on the same endpoint.
	for _, id := range []string{"claude-opus-5", "gpt-5.4", "kimi-k3", "minimax-m3"} {
		if IsFreeModel("opencode", FreeModelCandidate{ID: id}) {
			t.Errorf("IsFreeModel(opencode, %q) = true, want false (paid model)", id)
		}
		if IsModelFreeBadge("opencode", FreeModelCandidate{ID: id, DisplayName: id}) {
			t.Errorf("IsModelFreeBadge(opencode, %q) = true, want false (paid model)", id)
		}
	}

	// A "-free" id that is NOT catalogued stays unpaid until the catalogue
	// learns about it: the suffix alone is not a signal. This documents the
	// deliberate tightening and guards against someone "helpfully" teaching
	// the payload path about "-free".
	if IsFreeModel("opencode", FreeModelCandidate{ID: "some-future-model-free"}) {
		t.Error("uncatalogued \"-free\" id was reported free; the dash-suffix is not a payload signal")
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

// TestNoAuthProvidersRequireLLM pins the scope of the always-visible no-auth
// list. "No auth" alone is too broad: the registry also has keyless local
// media/utility servers (text-to-speech, search) that cannot serve a chat
// request, so surfacing them as provider cards would be noise. Upstream splits
// them into separate catalog files (noauth.ts vs audio.ts/search.ts) and this
// list must match that boundary via ServiceKinds.
func TestNoAuthProvidersRequireLLM(t *testing.T) {
	got := map[string]bool{}
	for _, m := range NoAuthProviders() {
		got[m.ID] = true
		if m.AuthType != "none" {
			t.Errorf("%s is listed as no-auth but has authType %q", m.ID, m.AuthType)
		}
		if !slices.Contains(m.ServiceKinds, "llm") {
			t.Errorf("%s is listed as no-auth but declares no llm service kind (%v)", m.ID, m.ServiceKinds)
		}
	}

	if !got["opencode"] {
		t.Error("opencode must be listed: keyless and serves chat")
	}

	// Keyless, but not chat: these must stay off the list.
	for _, id := range []string{"coqui", "edge-tts", "google-tts", "local-device", "searxng", "tortoise"} {
		if got[id] {
			t.Errorf("%s must not be listed: it is keyless but cannot serve chat", id)
		}
	}

	// A credentialed provider must never be listed, however it is categorised.
	for _, m := range NoAuthProviders() {
		if meta, ok := providerRegistry[m.ID]; ok && meta.AuthType != "none" {
			t.Errorf("%s requires auth yet was listed", m.ID)
		}
	}
}
