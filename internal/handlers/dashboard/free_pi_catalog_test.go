package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

// free-pi's catalogue is not an OpenAI-style /models list: it lives at
// GET /client-version as {"min","latest","models":[{"id","name"}]}. Before this
// branch existed, Import answered "Provider free-pi does not support models
// listing" and cached nothing, so a signed-in account advertised zero models.
func TestFetchFreePiModelsReadsClientVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/client-version" {
			t.Errorf("fetched %q, want /client-version", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"min":"0.6.18","latest":"0.6.18","model":"DeepSeek V4 Flash","models":[
			{"id":"deepseek-v4-flash","name":"DeepSeek V4 Flash · 🔥 Free Unlimited: limited time"},
			{"id":"particle","name":"Particle · 🔥 Free Unlimited: limited time"},
			{"id":"deepseek-v4-flash","name":"duplicate id"}
		]}`)
	}))
	t.Cleanup(srv.Close)

	orig := freePiCatalogURL
	freePiCatalogURL = srv.URL + "/client-version"
	t.Cleanup(func() { freePiCatalogURL = orig })

	h := &Handler{}
	res, err := h.fetchFreePiModels("", srv.Client().Timeout)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !res.Supported {
		t.Fatal("import must report Supported, or the dashboard hides the button")
	}
	if len(res.Models) != 2 {
		t.Fatalf("got %d models, want 2 (the duplicate id must be dropped)", len(res.Models))
	}
	if res.Models[0].ID != "deepseek-v4-flash" || res.Models[1].ID != "particle" {
		t.Errorf("ids = %q, want the two distinct catalogue ids", []string{res.Models[0].ID, res.Models[1].ID})
	}
	// The marketing suffix must not become part of the stored identity: an id
	// carrying "· 🔥 Free Unlimited: limited time" would never route.
	for _, m := range res.Models {
		if m.Name != "" {
			t.Errorf("model %q kept the upstream display name %q; only the id is storable", m.ID, m.Name)
		}
	}
}

// A catalogue that answers with an unusable body must still offer the seeded
// list rather than nothing, so Import never leaves a usable provider empty.
func TestFetchFreePiModelsFallsBackToTheSeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	orig := freePiCatalogURL
	freePiCatalogURL = srv.URL + "/client-version"
	t.Cleanup(func() { freePiCatalogURL = orig })

	h := &Handler{}
	res, err := h.fetchFreePiModels("", srv.Client().Timeout)
	if err != nil {
		t.Fatalf("a failing catalogue must not be a hard error: %v", err)
	}
	if !res.Supported || len(res.Models) == 0 {
		t.Fatalf("Supported = %v with %d models; the seed list must be used", res.Supported, len(res.Models))
	}
	if res.Error == "" {
		t.Error("the fallback must say why it fell back")
	}
	// And the person who hit it can still see the button, having been told why.
	if len(res.Models) != len(providers.ProviderModels[providers.FreePiProviderID]) {
		t.Errorf("seed size = %d, want %d", len(res.Models), len(providers.ProviderModels[providers.FreePiProviderID]))
	}
}

// The primary alias must be pinned. free-pi publishes two aliases, so an
// unpinned map makes GetProviderAlias return whichever the iteration reached
// first — and since the imported cache is keyed by one alias while /v1/models
// renders under the other, an imported catalogue could vanish between restarts.
// This is the failure nace already hit.
func TestFreePiPrimaryAliasIsPinned(t *testing.T) {
	if got := providers.GetProviderAlias("free-pi"); got != "fp" {
		t.Errorf("GetProviderAlias(free-pi) = %q, want \"fp\" (it must be stable across restarts)", got)
	}
	// Both published aliases must still resolve to the provider, whichever one
	// the UI or a config file happens to use.
	for _, alias := range []string{"fp", "freepi"} {
		if got := providers.ResolveAlias(alias); got != "free-pi" {
			t.Errorf("ResolveAlias(%q) = %q, want \"free-pi\"", alias, got)
		}
	}
}
