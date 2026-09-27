package dashboard

import (
	"strings"
	"testing"
)

// TestMimoFreeCatalogImportable pins the Import contract for the keyless MiMo
// Code Free provider: the dispatch branch must return supported rows, not the
// "does not support models listing" dead end, and the catalogue must only
// carry platform ids (no vendor prefix — the free-ai chat endpoint takes the
// bare id).
func TestMimoFreeCatalogImportable(t *testing.T) {
	res, err := (&Handler{}).fetchUpstreamModels("mimo-free", "", 0)
	if err != nil {
		t.Fatalf("fetchUpstreamModels(mimo-free): %v", err)
	}
	if !res.Supported {
		t.Fatalf("Supported = false, want true (error %q)", res.Error)
	}
	if len(res.Models) == 0 {
		t.Fatal("catalogue is empty")
	}
	for _, m := range res.Models {
		if strings.Contains(m.ID, "/") || m.ID == "" {
			t.Errorf("model id %q must be a bare platform id (no vendor prefix)", m.ID)
		}
	}
	found := false
	for _, m := range res.Models {
		if m.ID == "mimo-v2.5-pro" {
			found = true
		}
	}
	if !found {
		t.Error("flagship mimo-v2.5-pro missing from the catalogue")
	}
}
