package providers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// modelsDevFixture is trimmed from the real https://models.dev/api.json. Keep
// the field names exactly as upstream ships them: the previous parser read a
// "modality" map that models.dev never had, and a hand-built fixture using the
// same wrong name kept that bug green for every release.
const modelsDevFixture = `{
 "anthropic": {"models": {
  "claude-sonnet-4-6": {"id":"claude-sonnet-4-6","attachment":true,"reasoning":true,"tool_call":true,
   "modalities":{"input":["text","image","pdf"],"output":["text"]},"limit":{"context":1000000,"output":64000}}}},
 "nvidia": {"models": {
  "meta/llama-3.2-11b-vision-instruct": {"reasoning":false,"tool_call":false,
   "modalities":{"input":["text","image"],"output":["text"]},"limit":{"context":128000,"output":4096}},
  "nvidia/nemotron-3-nano-omni": {"reasoning":true,"tool_call":true,
   "modalities":{"input":["text","image","audio","video"],"output":["text"]}}}},
 "openrouter": {"models": {
  "openrouter/auto": {"reasoning":true,"tool_call":true,
   "modalities":{"input":["text","image","pdf","audio","video"],"output":["text","image"]}},
  "anthropic/claude-sonnet-4-6": {"reasoning":true,"tool_call":true,
   "modalities":{"input":["text","image","pdf"],"output":["text"]}},
  "google/gemini-9-flash-image": {"reasoning":false,"tool_call":false,
   "modalities":{"input":["text","image"],"output":["text","image"]}}}},
 "vercel": {"models": {
  "anthropic/claude-sonnet-4-6": {"reasoning":true,"tool_call":true,
   "modalities":{"input":["text","image"],"output":["text"]}}}},
 "typo-reseller": {"models": {
  "gpt-oss-20b": {"reasoning":true,"tool_call":true,
   "modalities":{"input":["text","image"],"output":["text"]}}}},
 "groq": {"models": {
  "openai/gpt-oss-20b": {"reasoning":true,"tool_call":true,
   "modalities":{"input":["text"],"output":["text"]}}}},
 "deepinfra": {"models": {
  "openai/gpt-oss-20b": {"reasoning":true,"tool_call":true,
   "modalities":{"input":["text"],"output":["text"]}}}}
}`

// syncFromFixture runs the real SyncModelCatalog against a local server
// standing in for models.dev, so the production decoder is what gets tested.
func syncFromFixture(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelsDevFixture))
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: rewriteTransport{target}}

	path := filepath.Join(t.TempDir(), "model-catalog.json")
	if err := SyncModelCatalog(t.Context(), client, path); err != nil {
		t.Fatalf("SyncModelCatalog: %v", err)
	}
	t.Cleanup(ClearCatalogForTest)
	return path
}

type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = rt.target.Scheme, rt.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func TestSyncModelCatalogReadsRealModelsDevFields(t *testing.T) {
	syncFromFixture(t)

	m := GetCatalogModalities("nvidia", "meta/llama-3.2-11b-vision-instruct")
	if m == nil || !m.Vision {
		t.Fatalf("nvidia llama vision: got %+v, want Vision from modalities.input", m)
	}
	omni := GetCatalogModalities("nvidia", "nvidia/nemotron-3-nano-omni")
	if omni == nil || !omni.Vision || !omni.AudioInput || !omni.VideoInput || !omni.Tools || !omni.Reasoning {
		t.Fatalf("nemotron omni: got %+v, want vision+audio+video+tools+reasoning", omni)
	}
	img := GetCatalogModalities("openrouter", "google/gemini-9-flash-image")
	if img == nil || !img.ImageOutput {
		t.Fatalf("image model: got %+v, want ImageOutput from modalities.output", img)
	}
}

func TestCatalogModalitiesPreferProviderListing(t *testing.T) {
	syncFromFixture(t)

	// vercel's listing omits pdf; the provider's own row must win over the
	// cross-provider majority (anthropic + openrouter say pdf).
	if m := GetCatalogModalities("vercel", "claude-sonnet-4-6"); m == nil || m.PDF {
		t.Fatalf("vercel claude: got %+v, want its own listing (no pdf)", m)
	}
	// An unlisted provider falls back to the majority: 2 of 3 list pdf.
	if m := GetCatalogModalities("antigravity", "claude-sonnet-4-6"); m == nil || !m.PDF {
		t.Fatalf("antigravity claude: got %+v, want majority pdf", m)
	}
	// ProviderAliases maps 9router ids onto models.dev ids.
	if m := GetCatalogModalities("claude", "claude-sonnet-4-6"); m == nil || !m.PDF {
		t.Fatalf("claude alias: got %+v, want anthropic's listing", m)
	}
}

func TestCatalogModalitiesMajorityIgnoresOneReseller(t *testing.T) {
	syncFromFixture(t)

	// One of three providers claims vision for a text-only model.
	if m := GetCatalogModalities("antigravity", "gpt-oss-20b"); m == nil || m.Vision {
		t.Fatalf("gpt-oss-20b: got %+v, want no vision (1 of 3 is a minority)", m)
	}
}

func TestCatalogModalitiesSkipsGenericNamesAcrossProviders(t *testing.T) {
	syncFromFixture(t)

	// "auto" on openrouter is a router with every modality; another
	// provider's "auto" is unrelated and must not inherit it.
	if m := GetCatalogModalities("twinmind", "auto"); m != nil {
		t.Fatalf("twinmind auto: got %+v, want nil (generic name)", m)
	}
	if m := GetCatalogModalities("openrouter", "openrouter/auto"); m == nil || !m.ImageOutput {
		t.Fatalf("openrouter auto: got %+v, want its own listing", m)
	}
}

func TestCapabilitiesOverlayOnlyTurnsOn(t *testing.T) {
	syncFromFixture(t)

	// models.dev says tool_call:false for this llama; the guess (Tools on)
	// must survive, while Vision is switched on from the catalogue.
	caps := GetCapabilitiesForModel("nvidia", "meta/llama-3.2-11b-vision-instruct")
	if !caps.Vision {
		t.Errorf("Vision: got false, want true from catalogue")
	}
	if !caps.Tools {
		t.Errorf("Tools: got false, want the pattern guess kept (overlay never turns off)")
	}
	if caps := GetCapabilitiesForModel("antigravity", "claude-sonnet-4-6"); !caps.PDF {
		t.Errorf("antigravity claude PDF: got false, want true from catalogue majority")
	}
}

func TestCatalogFileRoundTrip(t *testing.T) {
	path := syncFromFixture(t)
	ClearCatalogForTest()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("catalog not written: %v", err)
	}
	if err := LoadCatalogFromFile(path); err != nil {
		t.Fatalf("LoadCatalogFromFile: %v", err)
	}
	InvalidateCapabilitiesCache()
	if m := GetCatalogModalities("nvidia", "meta/llama-3.2-11b-vision-instruct"); m == nil || !m.Vision {
		t.Fatalf("after reload: got %+v, want per-provider modalities persisted", m)
	}
}
