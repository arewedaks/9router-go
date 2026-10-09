package providers

import "testing"

// Kartu katalog tiruan berbentuk persis seperti hasil SyncModelCatalog, supaya
// resolusi bisa diuji tanpa mengunduh models.dev.
func seedCatalog() {
	catalogMu.Lock()
	globalCatalog = &SyncedCatalog{
		SyncedAt: "2025-01-01T00:00:00Z",
		Models:   map[string]SyncedModelModalities{},
		Providers: map[string]map[string]SyncedModelLimits{
			"openai": {
				"gpt-4.1-mini": {ContextWindow: 1000000, MaxOutput: 32768},
			},
			"alibaba": {
				"qwen3-max": {ContextWindow: 256000, MaxOutput: 65536},
			},
			"anthropic": {
				"claude-sonnet-4-5": {ContextWindow: 200000, MaxOutput: 64000},
			},
			"nvidia": {
				"riva-translate-4b-instruct-v2": {ContextWindow: 4096, MaxOutput: 1024},
			},
		},
	}
	catalogMu.Unlock()
	InvalidateCapabilitiesCache()
}

func clearCatalog() {
	catalogMu.Lock()
	globalCatalog = nil
	catalogMu.Unlock()
	InvalidateCapabilitiesCache()
}

func TestCatalogLimitsMengalahkanTebakan(t *testing.T) {
	seedCatalog()
	defer clearCatalog()

	cases := []struct {
		provider string
		model    string
		wantCtx  int
		wantMax  int
	}{
		// Sebelumnya 128000 (default tebakan), padahal asli 1M.
		{"openai", "gpt-4.1-mini", 1000000, 32768},
		// Sebelumnya 131072, asli 256K.
		{"alibaba", "qwen3-max", 256000, 65536},
		// Sebelumnya 128000, asli ~4K. Ini yg paling berbahaya.
		{"nvidia", "riva-translate-4b-instruct-v2", 4096, 1024},
		// Model dgn prefix provider pada namanya tetap harus ketemu.
		{"openai", "openai/gpt-4.1-mini", 1000000, 32768},
	}

	for _, c := range cases {
		ctx, maxOut := GetModelTokenLimitsFor(c.provider, c.model)
		if ctx != c.wantCtx || maxOut != c.wantMax {
			t.Errorf("GetModelTokenLimitsFor(%q, %q) = (%d, %d), mau (%d, %d)",
				c.provider, c.model, ctx, maxOut, c.wantCtx, c.wantMax)
		}
	}
}

func TestAliasProvider(t *testing.T) {
	seedCatalog()
	defer clearCatalog()

	// "glm" adalah alias 9router untuk "zai" di models.dev.
	catalogMu.Lock()
	globalCatalog.Providers["zai"] = map[string]SyncedModelLimits{
		"glm-5": {ContextWindow: 200000, MaxOutput: 128000},
	}
	catalogMu.Unlock()
	InvalidateCapabilitiesCache()

	ctx, maxOut := GetModelTokenLimitsFor("glm", "glm-5")
	if ctx != 200000 || maxOut != 128000 {
		t.Errorf("alias glm->zai tidak bekerja: dapat (%d, %d)", ctx, maxOut)
	}
}

func TestFallbackKeTebakanSaatKatalogKosong(t *testing.T) {
	clearCatalog()

	// Tanpa katalog, harus jatuh ke pola nama — bukan angka nol.
	ctx, maxOut := GetModelTokenLimitsFor("unknown", "claude-sonnet-4-5")
	if ctx != 200000 || maxOut != 8192 {
		t.Errorf("fallback pola gagal: dapat (%d, %d), mau (200000, 8192)", ctx, maxOut)
	}
}

func TestModelTidakDikenalTetapDapatDefault(t *testing.T) {
	clearCatalog()

	// Nama yg tidak cocok pola apa pun tetap dapat default, bukan nol:
	// klien memecah percakapan berdasarkan angka ini, dan nol lebih buruk
	// daripada tebakan yg mungkin salah.
	ctx, maxOut := GetModelTokenLimitsFor("", "misteri-tanpa-pola")
	if ctx != 128000 || maxOut != 4096 {
		t.Errorf("default berubah: dapat (%d, %d), mau (128000, 4096)", ctx, maxOut)
	}
}

func TestKatalogTidakBocorAntarProvider(t *testing.T) {
	seedCatalog()
	defer clearCatalog()

	// Model yg hanya terdaftar di bawah openai tidak boleh mewarisi angka itu
	// saat diminta dari provider lain.
	ctx, _ := GetModelTokenLimitsFor("someothervendor", "gpt-4.1-mini")
	if ctx == 1000000 {
		t.Error("batas katalog bocor antar provider")
	}
}

// GetDisplayTokenLimits backs the dashboard's model list. It must label a
// model whose family is known even when models.dev has no entry (antigravity
// is absent from the 225-provider catalog, so its Gemini and Claude models
// would otherwise read blank despite well-known windows), and must stay silent
// for a name no family matches instead of printing the 128K routing default.
func TestDisplayTokenLimits(t *testing.T) {
	clearCatalog()
	defer clearCatalog()

	// A known family, absent from the catalog: fallback value is genuine.
	if cw, _ := GetDisplayTokenLimits("antigravity", "gemini-3-flash"); cw != 1048576 {
		t.Errorf("known family must be labelled, got %d", cw)
	}
	if cw, _ := GetDisplayTokenLimits("antigravity", "claude-opus-4-6-thinking"); cw != 200000 {
		t.Errorf("known family must be labelled, got %d", cw)
	}

	// No family matches: report nothing rather than inventing 128K.
	for _, m := range []string{"totally-made-up-model-xyz", "custom-endpoint-v2"} {
		if cw, mo := GetDisplayTokenLimits("antigravity", m); cw != 0 || mo != 0 {
			t.Errorf("%s: unknown model must be unlabelled, got ctx=%d out=%d", m, cw, mo)
		}
	}
}

// The routing fallback keeps its default: a rough cap beats none when choosing
// a connection, which is why GetDisplayTokenLimits cannot simply be
// GetModelTokenLimitsFor.
func TestRoutingLimitsKeepDefault(t *testing.T) {
	clearCatalog()
	defer clearCatalog()
	if cw, mo := GetModelTokenLimitsFor("antigravity", "totally-made-up-model-xyz"); cw != 128000 || mo != 4096 {
		t.Errorf("routing fallback must keep its default, got ctx=%d out=%d", cw, mo)
	}
}
