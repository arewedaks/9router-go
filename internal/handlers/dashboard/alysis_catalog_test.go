package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"9router/proxy/internal/models"
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

	for _, m := range catalog[string(providers.CategoryOAuth)] {
		if m["id"] == "alysis" {
			if m["name"] != "Alysis Code" {
				t.Errorf("name = %v, want Alysis Code", m["name"])
			}
			if m["authType"] != "oauth" {
				t.Errorf("authType = %v, want oauth", m["authType"])
			}
			return
		}
	}
	t.Fatalf("Alysis Code is absent from the dashboard catalog: %v", catalog[string(providers.CategoryOAuth)])
}

// Import is how an Alysis connection becomes usable: buildModelsList has no
// registry fallback, so a provider whose models were never fetched contributes
// nothing to /v1/models. This drives the real import route against a stub
// gateway and asserts the ids land in the cache that /v1/models reads.
func TestImportModelsAlysisCachesGatewayCatalogue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer slk_test_key" {
			t.Errorf("upstream Authorization = %q, want the stored slk_ key as a bearer", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"deepseek-flash"},
			{"id":"glm-5.3-flash"},
			{"id":"gpt-6-luna"},
			{"id":"claude-haiku-5-5"}
		]}`))
	}))
	defer srv.Close()

	old, hadOld := knownModelFetchers["alysis"]
	knownModelFetchers["alysis"] = modelFetcher{url: srv.URL, method: http.MethodGet, bearer: true}
	defer func() {
		if hadOld {
			knownModelFetchers["alysis"] = old
		} else {
			delete(knownModelFetchers, "alysis")
		}
	}()

	_, repo, r := setupTestDashboard(t)
	if err := repo.UpsertProviderConnection(&models.ProviderConnection{
		ID:       "alysis-1",
		Provider: "alysis",
		AuthType: "oauth",
		IsActive: 1,
		Data:     `{"accessToken":"slk_test_key"}`,
	}); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/dashboard/providers/alysis/models?save=1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var res ModelFetchResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if !res.Supported {
		t.Fatalf("Alysis import must report supported=true, got error %q", res.Error)
	}
	if len(res.Models) != 4 {
		t.Errorf("fetched %d models, want 4 (%v)", len(res.Models), res.Models)
	}

	key := repo.ResolveModelCacheKey("alysis", providers.AliasesFor("alysis"))
	cached, err := repo.ListCachedModels(key)
	if err != nil {
		t.Fatalf("list cached models: %v", err)
	}
	have := map[string]bool{}
	for _, m := range cached {
		have[m.ModelID] = true
	}
	for _, id := range []string{"deepseek-flash", "glm-5.3-flash", "gpt-6-luna", "claude-haiku-5-5"} {
		if !have[id] {
			t.Errorf("model %q is missing from the cache key %q that /v1/models reads", id, key)
		}
	}
}

// TestAlysisUsesOAuthSignInPanel pins the dashboard half of the OAuth switch:
// renderProviderDetail only hides the paste-a-key form when a panel is listed
// for the provider, so an Alysis entry missing from OAUTH_PANELS silently
// re-offers "Add connection" with an API-key field the flow never produces.
func TestAlysisUsesOAuthSignInPanel(t *testing.T) {
	raw, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatalf("read ui/index.html: %v", err)
	}
	html := string(raw)

	idx := strings.Index(html, "const OAUTH_PANELS = [")
	if idx < 0 {
		t.Fatal("OAUTH_PANELS is missing; the provider tab no longer has an OAuth panel table")
	}
	table := html[idx:]
	if end := strings.Index(table, "];"); end > 0 {
		table = table[:end]
	}

	entry := -1
	for _, line := range strings.Split(table, "\n") {
		if strings.Contains(line, `"alysis"`) {
			entry = 1
			if !strings.Contains(line, "genericOAuthPanelHtml") {
				t.Errorf("Alysis is listed without the generic device-flow panel: %s", line)
			}
		}
	}
	if entry < 0 {
		t.Fatal("Alysis is not in OAUTH_PANELS, so the provider tab still shows the API-key form")
	}
	if !strings.Contains(html, "function genericOAuthPanelHtml(") {
		t.Fatal("genericOAuthPanelHtml() is missing")
	}
}
