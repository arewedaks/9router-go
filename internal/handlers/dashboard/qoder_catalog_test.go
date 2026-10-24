package dashboard

import "testing"

// TestQoderCatalogImportable pins the Import contract for Qoder: the dispatch
// branch must answer supported rows instead of "does not support models
// listing", and the ids must be the upstream model keys the agent surface
// accepts. The display names Qoder's own /model/list advertises are rejected
// upstream with code 112, so shipping them here made every model look dead.
func TestQoderCatalogImportable(t *testing.T) {
	res, err := (&Handler{}).fetchUpstreamModels("qoder", "", 0)
	if err != nil {
		t.Fatalf("fetchUpstreamModels(qoder): %v", err)
	}
	if !res.Supported {
		t.Fatalf("Supported = false, want true (error %q)", res.Error)
	}
	if res.Provider != "qoder" {
		t.Errorf("Provider = %q, want qoder", res.Provider)
	}

	want := map[string]bool{
		"lite":          false,
		"qmodel_38max":  false,
		"qmodel_latest": false,
		"qmodel":        false,
		"qfmodel":       false,
		"kmodel_latest": false,
		"kmodel":        false,
		"gmodel":        false,
		"gfmodel":       false,
		"dmodel":        false,
		"dfmodel":       false,
		"mmodel":        false,
	}
	for _, m := range res.Models {
		if _, ok := want[m.ID]; ok {
			want[m.ID] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("catalogue is missing %s", id)
		}
	}

	// A display name must never be shipped as a model id: the upstream refuses
	// it, so it would only ever produce a plan error.
	for _, m := range res.Models {
		if m.ID == "qwen3.8-max-preview" || m.ID == "deepseek-v4-pro" {
			t.Errorf("model id %q is a display name, not an upstream key", m.ID)
		}
	}
}
