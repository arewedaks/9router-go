package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseTwinmindCatalog(t *testing.T) {
	raw := []byte(`{
	  "default_model": {"name":"auto","display_name":"Basic","minimum_tier":"free"},
	  "providers": [
	    {"id":"google","display_name":"Google","models":[
	      {"name":"gemini-3.7-flash","display_name":"Gemini 3.7 Flash","minimum_tier":"pro"},
	      {"name":"gemini-3.8-flash-thinking","display_name":"Gemini 3.8 Flash Thinking","minimum_tier":"max"}]},
	    {"id":"openai","display_name":"OpenAI","models":[
	      {"name":"gpt-6-luna","display_name":"GPT-6 Luna","minimum_tier":"pro"}]}
	  ]}`)
	models := parseTwinmindCatalog(raw)
	if len(models) != 4 {
		t.Fatalf("parsed %d models, want 4 (auto + 3 vendor models)", len(models))
	}
	byID := make(map[string]UpstreamModel, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	if m, ok := byID["auto"]; !ok || m.Name != "Basic" {
		t.Errorf("auto entry = %+v", m)
	}
	if m, ok := byID["google/gemini-3.7-flash"]; !ok || !strings.Contains(m.Name, "pro") {
		t.Errorf("google/gemini-3.7-flash = %+v, want the tier in the display name", m)
	}
	if _, ok := byID["gemini-3.7-flash"]; ok {
		t.Error("bare model id leaked in; ids must be vendor-prefixed so same-named models stay distinct")
	}
}

func TestFetchTwinmindModelsLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer id-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"default_model":{"name":"auto","display_name":"Basic"},
		  "providers":[{"id":"google","models":[{"name":"gemini-3.7-flash","display_name":"Gemini 3.7 Flash","minimum_tier":"pro"}]}]}`))
	}))
	defer srv.Close()

	old := twinmindModelsURLForTest
	twinmindModelsURLForTest = srv.URL
	defer func() { twinmindModelsURLForTest = old }()

	h := &Handler{}
	res, err := h.fetchTwinmindModels("twinmind", `{"accessToken":"id-token"}`, 5*time.Second)
	if err != nil {
		t.Fatalf("fetchTwinmindModels: %v", err)
	}
	if !res.Supported || res.Warning != "" {
		t.Errorf("Supported=%v Warning=%q, want a clean live fetch", res.Supported, res.Warning)
	}
	if len(res.Models) != 2 || res.Models[1].ID != "google/gemini-3.7-flash" {
		t.Errorf("models = %+v", res.Models)
	}
}

func TestFetchTwinmindModelsFallsBackWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	old := twinmindModelsURLForTest
	twinmindModelsURLForTest = srv.URL
	defer func() { twinmindModelsURLForTest = old }()

	h := &Handler{}
	res, err := h.fetchTwinmindModels("twinmind", `{"accessToken":"id-token"}`, 5*time.Second)
	if err != nil {
		t.Fatalf("fetchTwinmindModels: %v", err)
	}
	if !res.Supported || res.Warning == "" {
		t.Errorf("Supported=%v Warning=%q, want the fallback catalogue with a warning", res.Supported, res.Warning)
	}
	if len(res.Models) == 0 {
		t.Error("fallback returned no models")
	}
}

func TestFetchTwinmindModelsWithoutCredential(t *testing.T) {
	h := &Handler{}
	res, err := h.fetchTwinmindModels("twinmind", `{}`, time.Second)
	if err != nil {
		t.Fatalf("fetchTwinmindModels: %v", err)
	}
	if !res.Supported || res.Warning == "" {
		t.Errorf("Supported=%v Warning=%q, want the no-credential fallback", res.Supported, res.Warning)
	}
}
