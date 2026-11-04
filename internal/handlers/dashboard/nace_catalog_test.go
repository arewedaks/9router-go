package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNaceCatalogImportable(t *testing.T) {
	res, err := (&Handler{}).fetchUpstreamModels("nace", "", 0)
	if err != nil {
		t.Fatalf("fetchUpstreamModels(nace): %v", err)
	}
	if !res.Supported {
		t.Fatalf("Supported = false, want true (error %q)", res.Error)
	}
	if res.Provider != "nace" {
		t.Errorf("Provider = %q, want nace", res.Provider)
	}

	wantModels := map[string]bool{
		"drex-max":         false,
		"drex-max-v1.1":    false,
		"drex-max-finance": false,
		"drex-v2.0":        false,
		"drex-latest":      false,
	}
	for _, m := range res.Models {
		if _, ok := wantModels[m.ID]; ok {
			wantModels[m.ID] = true
		}
	}
	for id, seen := range wantModels {
		if !seen {
			t.Errorf("catalogue is missing model %s", id)
		}
	}
}

func TestNaceProviderLogoServesWebP(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	req := httptest.NewRequest("GET", "/provider-logos/nace.webp", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /provider-logos/nace.webp status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/webp" {
		t.Errorf("Content-Type = %q, want image/webp", ct)
	}
	if w.Body.Len() == 0 {
		t.Fatal("logo body is empty")
	}
}
