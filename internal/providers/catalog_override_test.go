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

// The Alysis gateway refuses the chat lane at ~77.5k of its own prompt_tokens
// while /v1/models published the family's 131072. A harness that trusted that
// number packed a conversation the gateway answered 400 context_length_exceeded
// to, after the request was already sent.
func TestAlysisChatLaneContextCap(t *testing.T) {
	clearCatalog()

	cw, mo := GetModelTokenLimitsFor("alysis", "deepseek-flash")
	if cw != 76000 {
		t.Errorf("alysis deepseek-flash window = %d, want 76000", cw)
	}
	if mo != 393216 {
		t.Errorf("alysis deepseek-flash max output = %d, want 393216 (gateway ceiling)", mo)
	}
	cw, _ = GetModelTokenLimitsFor("alys", "deepseek-flash")
	if cw != 76000 {
		t.Errorf("alias window = %d, want 76000", cw)
	}

	// The /responses lane is genuinely larger: the same account served Luna at
	// 297189 input tokens, so 272000 stays and the override must not leak.
	cw, _ = GetModelTokenLimitsFor("alysis", "gpt-6-luna")
	if cw != 272000 {
		t.Errorf("luna window = %d, want 272000 (its /responses lane accepts more)", cw)
	}

	// Other providers serving the same model name keep their own window.
	cw, _ = GetModelTokenLimitsFor("river", "deepseek-ai/DeepSeek-V4.1-Flash")
	if cw != 1000000 {
		t.Errorf("river window = %d, want 1000000", cw)
	}
}
