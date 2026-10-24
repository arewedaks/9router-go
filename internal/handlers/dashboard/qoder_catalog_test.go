package dashboard

import "testing"

// TestQoderCatalogImportable pins the Import contract for Qoder: the dispatch
// branch must answer supported rows instead of "does not support models
// listing", and the ids must be the ones the upstream registry serves (bare
// model names, not the internal tier codes in the /v1/models registry).
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
		"qwen3.8-max-preview": false,
		"qwen3.7-max":         false,
		"qwen3.7-plus":        false,
		"kimi-k3":             false,
		"kimi-k2.7-code":      false,
		"glm-5.2":             false,
		"deepseek-v4-pro":     false,
		"deepseek-v4-flash":   false,
		"minimax-m3":          false,
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
}
