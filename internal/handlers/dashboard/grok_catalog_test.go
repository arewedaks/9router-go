package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The live catalogue carries one model with selectable reasoning-effort
// levels; each level must import as its own id so an operator can pin an
// effort per route (grok-4.7-xhigh, grok-4.7-high, ...).
func TestParseGrokCatalogExpandsEfforts(t *testing.T) {
	cat := grokCatalog{}
	cat.Data = []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Context int    `json:"context_window"`
		Efforts []struct {
			ID      string `json:"id"`
			Default bool   `json:"default"`
		} `json:"reasoning_efforts"`
	}{
		{
			ID:   "grok-4.7",
			Name: "Grok 4.7",
			Efforts: []struct {
				ID      string `json:"id"`
				Default bool   `json:"default"`
			}{{ID: "xhigh"}, {ID: "high", Default: true}, {ID: "medium"}, {ID: "low"}},
		},
	}
	models := parseGrokCatalog(cat)
	got := make([]string, 0, len(models))
	for _, m := range models {
		got = append(got, m.ID)
	}
	want := []string{"grok-4.7", "grok-4.7-xhigh", "grok-4.7-high", "grok-4.7-medium", "grok-4.7-low"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("parseGrokCatalog ids = %v, want %v", got, want)
	}
}

// The fetcher must call the CLI proxy with the stored access token and the
// identity headers the executor sends; upstream rejects the rest.
func TestFetchGrokCLIModelsLiveFetch(t *testing.T) {
	var gotAuth, gotClient string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotClient = r.Header.Get("x-grok-client-identifier")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"grok-4.7","name":"Grok 4.7","context_window":500000,"reasoning_efforts":[{"id":"high","default":true},{"id":"low"}]}]}`))
	}))
	defer srv.Close()
	old := grokModelsURLForTest
	grokModelsURLForTest = srv.URL
	t.Cleanup(func() { grokModelsURLForTest = old })

	h := &Handler{}
	res, err := h.fetchGrokCLIModels("grok-cli", `{"accessToken":"tok-123"}`, 2*time.Second)
	if err != nil {
		t.Fatalf("fetchGrokCLIModels: %v", err)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want the stored access token", gotAuth)
	}
	if gotClient != "grok-shell" {
		t.Errorf("x-grok-client-identifier = %q, want grok-shell", gotClient)
	}
	if !res.Supported || len(res.Models) != 3 {
		t.Errorf("Supported=%v models=%d, want supported with 3 rows (base + 2 efforts)", res.Supported, len(res.Models))
	}
}

// A broken catalogue falls back to the static registry list instead of
// answering "does not support models listing".
func TestFetchGrokCLIModelsFallsBackWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	old := grokModelsURLForTest
	grokModelsURLForTest = srv.URL
	t.Cleanup(func() { grokModelsURLForTest = old })

	h := &Handler{}
	res, err := h.fetchGrokCLIModels("grok-cli", `{"accessToken":"tok-123"}`, 2*time.Second)
	if err != nil {
		t.Fatalf("fetchGrokCLIModels: %v", err)
	}
	if !res.Supported {
		t.Error("fallback must still report Supported=true")
	}
	if len(res.Models) == 0 {
		t.Error("fallback returned zero models")
	}
	if !strings.Contains(res.Warning, "HTTP 502") {
		t.Errorf("warning does not mention the upstream status: %q", res.Warning)
	}
}
