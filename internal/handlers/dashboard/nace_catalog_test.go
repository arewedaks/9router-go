package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// The live listing is {"models":[{"name":…,"alias_for":…}]}, not OpenAI's
// {"data":[{"id":…}]}. Parsing the wrong shape returned no models and silently
// fell back to the static catalogue, so a live import looked like it worked
// while carrying hand-written ids. Pin the real shape against a fake upstream.
func TestNaceCatalogParsesLiveListing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"Invalid API key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"name":"drex-latest","description":"Alias","release_date":"2026-10-06","alias_for":"drex-v2.0"},
			{"name":"drex-v2.0","description":"Drex 2.0","release_date":"2026-10-06","alias_for":null},
			{"name":"drex-max","description":"Multimodal","release_date":"2026-10-13","alias_for":null}
		]}`))
	}))
	defer srv.Close()

	// The fetcher reads the connection's baseUrl from the data blob, so point it
	// at the fake upstream and give it a key.
	data := `{"apiKey":"nace_sk_test","baseUrl":"` + srv.URL + `"}`
	res, err := (&Handler{}).fetchNaceModels("nace", data, 0)
	if err != nil {
		t.Fatalf("fetchNaceModels: %v", err)
	}
	if res.Warning != "" {
		t.Errorf("fell back to the static catalogue despite a valid listing: %q", res.Warning)
	}
	got := map[string]string{}
	for _, m := range res.Models {
		got[m.ID] = m.Name
	}
	if len(got) != 3 {
		t.Fatalf("parsed %d models, want 3: %v", len(got), got)
	}
	for _, id := range []string{"drex-latest", "drex-v2.0", "drex-max"} {
		if _, ok := got[id]; !ok {
			t.Errorf("listing model %s missing", id)
		}
	}
	// An alias entry must show the version that answers it.
	if n := got["drex-latest"]; !strings.Contains(n, "drex-v2.0") {
		t.Errorf("alias display name %q does not name the version that answers it", n)
	}
}

// A listing without a key must not be treated as an empty catalogue: the
// upstream answers 401 and the fetcher falls back, which is the correct path.
func TestNaceCatalogFallsBackWithoutKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	res, err := (&Handler{}).fetchNaceModels("nace", `{"baseUrl":"`+srv.URL+`"}`, 0)
	if err != nil {
		t.Fatalf("fetchNaceModels: %v", err)
	}
	if len(res.Models) == 0 {
		t.Fatal("fallback catalogue is empty")
	}
	if res.Warning == "" {
		t.Error("expected a fallback warning when the live listing is unusable")
	}
}
