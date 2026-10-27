package providers

import "testing"

// The free lane serves a smaller window than the paid id of the same name, and
// the name-pattern table matches on the name alone — so it advertised 1M for a
// model that accepts 330k. A client trusting that number compacted too late and
// the request died upstream after being sent.
func TestOpencodeFreeContextCap(t *testing.T) {
	clearCatalog()

	cw, _ := GetModelTokenLimitsFor("opencode", "deepseek-v4.1-flash-free")
	if cw != 330000 {
		t.Errorf("free deepseek window = %d, want 330000", cw)
	}
	cw, _ = GetModelTokenLimitsFor("opencode", "glm-5.3-flash-free")
	if cw != 200000 {
		t.Errorf("free glm window = %d, want 200000", cw)
	}

	// The paid id keeps the family window: the override is per provider+model,
	// not a blanket cap on the name.
	cw, _ = GetModelTokenLimitsFor("opencode-go", "deepseek-v4.1-flash")
	if cw != 1000000 {
		t.Errorf("paid deepseek window = %d, want 1000000", cw)
	}

	// A bare model id still resolves through the override.
	cw, _ = GetModelTokenLimitsFor("", "oc/deepseek-v4.1-flash-free")
	if cw != 330000 {
		t.Errorf("bare id window = %d, want 330000", cw)
	}

	// The dashboard reads the same numbers, not a separate table.
	cw, _ = GetDisplayTokenLimits("opencode", "deepseek-v4.1-flash-free")
	if cw != 330000 {
		t.Errorf("display window = %d, want 330000", cw)
	}
}
