package chat

import (
	"testing"

	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// A combo publishes the largest window across its seats: a request needs one
// seat, not all of them. Asking by name alone returned the 128000 name-pattern
// default, which understated a combo of 1M-window models by 8x.
func TestComboTokenLimitsUsesSeats(t *testing.T) {
	providers.SeedCatalogForTest(map[string]map[string]providers.SyncedModelLimits{
		"opencode": {
			"nemotron-3-ultra-free": {ContextWindow: 1000000, MaxOutput: 128000},
		},
		"nvidia": {
			"nemotron-3-ultra-550b-a55b": {ContextWindow: 1000000, MaxOutput: 65536},
		},
		"antigravity": {
			"gemini-3.6-flash-high": {ContextWindow: 1048576, MaxOutput: 65536},
		},
	})
	defer providers.ClearCatalogForTest()

	h := &ChatHandler{}

	// Seats carry UI aliases ("oc/", "ag/"), not catalogue provider ids.
	c := &models.Combo{
		Name:   "nemotron-model",
		Models: `["oc/nemotron-3-ultra-free","nvidia/nvidia/nemotron-3-ultra-550b-a55b"]`,
	}
	ctx, maxOut := h.comboTokenLimits(c)
	if ctx != 1000000 {
		t.Errorf("context window = %d, want 1000000 (largest seat)", ctx)
	}
	if maxOut != 128000 {
		t.Errorf("max output = %d, want 128000 (largest seat)", maxOut)
	}

	// A single-seat combo inherits that seat's figures exactly.
	single := &models.Combo{Name: "gemini-flash", Models: `["ag/gemini-3.6-flash-high"]`}
	if ctx, maxOut := h.comboTokenLimits(single); ctx != 1048576 || maxOut != 65536 {
		t.Errorf("single seat = (%d, %d), want (1048576, 65536)", ctx, maxOut)
	}
}

// The old behaviour must survive where it is still the only answer: a combo
// whose seats are all unknown still gets the name-based guess rather than zero.
func TestComboTokenLimitsFallsBackWhenNoSeatResolves(t *testing.T) {
	providers.ClearCatalogForTest()
	h := &ChatHandler{}

	c := &models.Combo{Name: "mystery-combo", Models: `["unknown/vendor-model"]`}
	ctx, maxOut := h.comboTokenLimits(c)
	if ctx != 128000 || maxOut != 4096 {
		t.Errorf("fallback = (%d, %d), want (128000, 4096)", ctx, maxOut)
	}
}

// Unparseable seats must not panic or zero the result.
func TestComboTokenLimitsHandlesBadSeats(t *testing.T) {
	providers.ClearCatalogForTest()
	h := &ChatHandler{}

	for _, seatJSON := range []string{`not json`, `[]`, `["no-slash"]`, `["/"]`, `[""]`} {
		c := &models.Combo{Name: "broken-combo", Models: seatJSON}
		if ctx, _ := h.comboTokenLimits(c); ctx <= 0 {
			t.Errorf("seats %q produced ctx=%d, want a positive fallback", seatJSON, ctx)
		}
	}
}

// A seat may be a bare model id with no provider prefix. Skipping it dropped
// gemini-flash to the name-pattern default even though every seat was a 1M
// model, so bare seats must resolve across providers.
func TestComboTokenLimitsMenanganiKursiTanpaProvider(t *testing.T) {
	providers.SeedCatalogForTest(map[string]map[string]providers.SyncedModelLimits{
		"google": {
			"gemini-3.8-flash": {ContextWindow: 1048576, MaxOutput: 65536},
		},
		"openrouter": {
			"gemini-3.7-flash": {ContextWindow: 1048576, MaxOutput: 65536},
		},
	})
	defer providers.ClearCatalogForTest()

	h := &ChatHandler{}
	c := &models.Combo{Name: "gemini-flash", Models: `["gemini-3.8-flash","gemini-3.7-flash"]`}
	if ctx, maxOut := h.comboTokenLimits(c); ctx != 1048576 || maxOut != 65536 {
		t.Errorf("bare seats = (%d, %d), want (1048576, 65536)", ctx, maxOut)
	}
}

// A seat id may embed a route before the model
// ("z-ai/glm-5.2-free/nvidia/nemotron-3-..."). Cutting at the first slash and
// letting the prefix provider's own model answer attributed glm's window to a
// nemotron seat, pinning the combo at 131072 instead of 1000000.
func TestComboTokenLimitsTidakSalahAtribusiKursiBersubRute(t *testing.T) {
	providers.SeedCatalogForTest(map[string]map[string]providers.SyncedModelLimits{
		"zai": {
			"glm-5.2-free": {ContextWindow: 131072, MaxOutput: 8192},
		},
		"nvidia": {
			"nemotron-3-nano-omni-30b-a3b-reasoning": {ContextWindow: 1000000, MaxOutput: 65536},
		},
	})
	defer providers.ClearCatalogForTest()

	h := &ChatHandler{}
	c := &models.Combo{
		Name:   "nemotron-model",
		Models: `["z-ai/glm-5.2-free/nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free"]`,
	}
	ctx, maxOut := h.comboTokenLimits(c)
	if ctx == 131072 || maxOut == 8192 {
		t.Errorf("seat = (%d, %d): borrowed the route provider's limits", ctx, maxOut)
	}
	if ctx != 1000000 || maxOut != 65536 {
		t.Errorf("seat = (%d, %d), want (1000000, 65536)", ctx, maxOut)
	}
}

// Upstream ships a handful of catalogue rows whose max output equals the whole
// context window (cortecs/digitalocean/wandb/nebius deepseek-v4.1-flash).
// Taking the largest such row as the model's capability produced a combo with
// maxOutput > contextWindow, which is impossible and breaks client budgeting.
func TestComboTokenLimitsMembuangBarisKatalogMustahil(t *testing.T) {
	providers.SeedCatalogForTest(map[string]map[string]providers.SyncedModelLimits{
		"cortecs": {
			"deepseek-v4.1-flash": {ContextWindow: 1048576, MaxOutput: 1048576},
		},
		"opencode": {
			"deepseek-v4.1-flash": {ContextWindow: 1000000, MaxOutput: 384000},
		},
	})
	defer providers.ClearCatalogForTest()

	h := &ChatHandler{}
	c := &models.Combo{Name: "deepseek-model", Models: `["deepseek-v4.1-flash"]`}
	ctx, maxOut := h.comboTokenLimits(c)
	if ctx != 1000000 || maxOut != 384000 {
		t.Errorf("combo = (%d, %d), want (1000000, 384000) from the sane row", ctx, maxOut)
	}
	if maxOut >= ctx {
		t.Errorf("max output %d >= context window %d", maxOut, ctx)
	}
}

// Seats are fallbacks, not a pipeline, so a small helper seat must not lower
// what the combo advertises. The real nemotron-model combo mixes 1M seats with
// a 4B "nemotron-mini-4b-instruct" seat (128000/8192); taking the minimum
// published 128K for a combo that can serve 1M.
func TestComboTokenLimitsMengambilKursiTerbesar(t *testing.T) {
	providers.SeedCatalogForTest(map[string]map[string]providers.SyncedModelLimits{
		"opencode": {
			"nemotron-3-ultra-free": {ContextWindow: 1000000, MaxOutput: 128000},
		},
		"nvidia": {
			"nemotron-mini-4b-instruct": {ContextWindow: 128000, MaxOutput: 8192},
		},
	})
	defer providers.ClearCatalogForTest()

	h := &ChatHandler{}
	c := &models.Combo{
		Name:   "nemotron-model",
		Models: `["oc/nemotron-3-ultra-free","nvidia/nemotron-mini-4b-instruct"]`,
	}
	ctx, maxOut := h.comboTokenLimits(c)
	if ctx != 1000000 {
		t.Errorf("context window = %d, want 1000000 (the seat the combo can serve)", ctx)
	}
	if maxOut != 128000 {
		t.Errorf("max output = %d, want 128000", maxOut)
	}
}
