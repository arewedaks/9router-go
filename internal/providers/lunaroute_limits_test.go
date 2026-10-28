package providers

import "testing"

// LunaRoute publishes deepseek-4.1-flash without the "v" that the family row
// matched on, so the name pattern fell through to the generic "deepseek" entry
// and served 131072 tokens to clients of a 1M-token model. Both numbers below
// were read off the gateway's own 400 bodies (2026-10-28): max_model_len=1048576
// for max_tokens above 1048576, and an 870468-token prompt accepted.
func TestLunarouteDeepseekContext(t *testing.T) {
	clearCatalog()

	for _, model := range []string{"deepseek-4.1-flash", "deepseek-4.1-flash-background"} {
		cw, mo := GetModelTokenLimitsFor("lr", model)
		if cw != 1048576 || mo != 393216 {
			t.Errorf("lr/%s = (%d, %d), want (1048576, 393216)", model, cw, mo)
		}
		// The dashboard must show the measured window, not a blank or a guess.
		if dcw, _ := GetDisplayTokenLimits("lr", model); dcw != 1048576 {
			t.Errorf("display lr/%s = %d, want 1048576", model, dcw)
		}
	}

	// Any other provider serving the same id gets the family window instead of
	// the generic deepseek 131072.
	if cw, _ := GetModelTokenLimitsFor("reseller-x", "deepseek-4.1-flash"); cw != 1000000 {
		t.Errorf("reseller deepseek-4.1-flash window = %d, want 1000000", cw)
	}
	// The "v" spelling keeps its own family row.
	if cw, _ := GetModelTokenLimitsFor("opencode-go", "deepseek-v4.1-flash"); cw != 1000000 {
		t.Errorf("opencode deepseek-v4.1-flash window = %d, want 1000000", cw)
	}
}

// muse-spark-open: upstream answers "supports at most 524288 completion tokens"
// and "input is longer than the model's context length (524288 tokens)".
func TestLunarouteMuseSparkLimits(t *testing.T) {
	clearCatalog()

	for _, model := range []string{"muse-spark-open", "muse-spark-open-background"} {
		cw, mo := GetModelTokenLimitsFor("lr", model)
		if cw != 524288 || mo != 524288 {
			t.Errorf("lr/%s = (%d, %d), want (524288, 524288)", model, cw, mo)
		}
	}
}

// /v1/models calls the resolver with the node's opaque generated id and the
// prefixed model id — the shape the live gateway actually serves. The override
// keys are written with the short prefix, so this is the case that used to fall
// through to the family guess and advertise 1000000/128000 for a 1M/393K model.
func TestLunarouteOverrideMatchesOpaqueNodeID(t *testing.T) {
	clearCatalog()
	const node = "openai-compatible-chat-d76ef300-b269-4a63-973f-e54dd52d985f"
	cases := map[string][2]int{
		"lr/deepseek-4.1-flash":            {1048576, 393216},
		"lr/deepseek-4.1-flash-background": {1048576, 393216},
		"lr/muse-spark-open":               {524288, 524288},
		"lr/muse-spark-open-background":    {524288, 524288},
	}
	for model, want := range cases {
		cw, mo := GetModelTokenLimitsFor(node, model)
		if cw != want[0] || mo != want[1] {
			t.Errorf("%s via node id = (%d, %d), want (%d, %d)", model, cw, mo, want[0], want[1])
		}
		if !HasCatalogLimitOverride(node, model) {
			t.Errorf("override not detected for %s via node id", model)
		}
	}
	// A provider that is not pinned keeps its catalogue/family number: the
	// prefix fallback must not hand LunaRoute's measured window to a reseller.
	if cw, _ := GetModelTokenLimitsFor("reseller-x", "deepseek-4.1-flash"); cw != 1000000 {
		t.Errorf("reseller window = %d, want 1000000", cw)
	}
}
