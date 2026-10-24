package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

// The dashboard catalog endpoint is what actually renders the provider card.
// Asserting providers.GetCatalogByCategory() alone would miss a route that
// filters the catalog on the way out.
func TestCatalogEndpointExposesAlysis(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	req := httptest.NewRequest("GET", "/api/dashboard/providers/catalog", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}

	var catalog map[string][]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}

	for _, m := range catalog[string(providers.CategoryAPIKey)] {
		if m["id"] == "alysis" {
			if m["name"] != "Alysis Code" {
				t.Errorf("name = %v, want Alysis Code", m["name"])
			}
			if m["authType"] != "apikey" {
				t.Errorf("authType = %v, want apikey", m["authType"])
			}
			return
		}
	}
	t.Fatalf("Alysis Code is absent from the dashboard catalog: %v", catalog[string(providers.CategoryAPIKey)])
}
