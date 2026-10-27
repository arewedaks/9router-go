package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The CLI's JSON envelope is not contractual, so the walk must survive the
// shapes it plausibly ships: families with nested models, a flat list, and
// extra scalar fields. A family object that only has a "name" must NOT become
// a model id.
func TestParseDevinModels(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "families with nested models",
			raw: `{"families":[{"name":"Claude","models":[
				{"id":"claude-opus-4.7-high","display_name":"Opus 4.7 High"},
				{"id":"claude-sonnet-4.6"}]},
				{"name":"GPT","models":[{"id":"gpt-5.5-high","name":"GPT 5.5 High"}]}]}`,
			want: []string{"claude-opus-4.7-high", "claude-sonnet-4.6", "gpt-5.5-high"},
		},
		{
			name: "flat list with slug",
			raw:  `[{"slug":"swe-1.6","label":"SWE 1.6"},{"slug":"swe-1.6-fast"}]`,
			want: []string{"swe-1.6", "swe-1.6-fast"},
		},
		{
			name: "duplicates collapse, junk ignored",
			raw:  `{"models":[{"id":"a"},{"id":"a"}],"count":2,"ok":true}`,
			want: []string{"a"},
		},
		{name: "not json", raw: `Not logged in. Run 'devin auth login'.`, want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDevinModels([]byte(tc.raw))
			if len(got) != len(tc.want) {
				t.Fatalf("got %d models %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i, id := range tc.want {
				if got[i].ID != id {
					t.Errorf("model %d = %q, want %q", i, got[i].ID, id)
				}
				if got[i].Kind != "llm" || got[i].Name == "" {
					t.Errorf("model %d missing kind/name: %+v", i, got[i])
				}
			}
		})
	}
}

// The live branch must actually exec the CLI and prefer its answer over the
// registry fallback.
func TestFetchDevinModelsUsesCLI(t *testing.T) {
	script := filepath.Join(t.TempDir(), "devin")
	body := "#!/bin/sh\nprintf '%s' '{\"families\":[{\"name\":\"Claude\",\"models\":[{\"id\":\"claude-opus-4.7-high\"}]}]}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVIN_BIN", script)

	h := &Handler{}
	res, err := h.fetchDevinModels("devin-cli", 10*time.Second)
	if err != nil {
		t.Fatalf("fetchDevinModels: %v", err)
	}
	if !res.Supported {
		t.Error("Supported = false, want true")
	}
	if len(res.Models) != 1 || res.Models[0].ID != "claude-opus-4.7-high" {
		t.Fatalf("models = %+v, want the CLI's single model", res.Models)
	}
	if res.Warning == "" || !strings.Contains(res.Warning, "Live catalogue") {
		t.Errorf("warning = %q, want the live-catalogue note", res.Warning)
	}
}

// Missing binary must degrade to the registry catalogue, not error out.
func TestFetchDevinModelsFallsBackWhenCLIMissing(t *testing.T) {
	t.Setenv("DEVIN_BIN", filepath.Join(t.TempDir(), "does-not-exist"))
	h := &Handler{}
	res, err := h.fetchDevinModels("dv", 5*time.Second)
	if err != nil {
		t.Fatalf("fetchDevinModels: %v", err)
	}
	if !res.Supported || len(res.Models) == 0 {
		t.Fatalf("fallback returned supported=%v models=%d", res.Supported, len(res.Models))
	}
	if res.Provider != "devin-cli" {
		t.Errorf("provider = %q, want canonical devin-cli", res.Provider)
	}
	if !strings.Contains(res.Warning, "registry catalogue") {
		t.Errorf("warning = %q, want the fallback note", res.Warning)
	}
}

// Import must complete even with no binary and no login: the registry
// catalogue backs it, and Supported stays true so the UI does not error out.
func TestDevinFallbackCatalogue(t *testing.T) {
	models := devinStaticModels()
	if len(models) == 0 {
		t.Fatal("registry catalogue for devin-cli is empty")
	}
	found := false
	for _, m := range models {
		if m.ID == "swe-1.6" {
			found = true
		}
		if m.Kind != "llm" {
			t.Errorf("%s kind = %q, want llm", m.ID, m.Kind)
		}
	}
	if !found {
		t.Error("registry catalogue is missing swe-1.6")
	}
}
