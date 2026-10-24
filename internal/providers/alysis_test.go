package providers

import "testing"

// Alysis Code (github.com/AlysisAi/AlysisCode) is an OpenAI-compatible gateway
// whose CLI device login hands out a static `slk_…` key. It must be reachable
// through the alias, the registry, the model catalogue and the executor map, or
// the dashboard offers a provider whose requests have nowhere to go.
func TestAlysisProviderRegistered(t *testing.T) {
	if got := ResolveAlias("alys"); got != "alysis" {
		t.Errorf("ResolveAlias(\"alys\") = %q, want \"alysis\"", got)
	}

	meta, ok := GetProviderMeta("alysis")
	if !ok {
		t.Fatal("Alysis is missing from the provider registry")
	}
	if meta.AuthType != "oauth" || meta.Alias != "alys" {
		t.Errorf("authType/alias = %q/%q, want oauth/alys", meta.AuthType, meta.Alias)
	}
	// The CLI signs in with a device flow, so the dashboard must offer it in the
	// OAuth section — that section is the only one with a sign-in button.
	if meta.Category != CategoryOAuth {
		t.Errorf("category = %q, want %q", meta.Category, CategoryOAuth)
	}

	cfg, ok := KnownProviders["alysis"]
	if !ok {
		t.Fatal("Alysis is missing from KnownProviders")
	}
	if cfg.AuthScheme != "bearer" || cfg.AuthHeader != "Authorization" {
		t.Errorf("auth = %q/%q, want bearer/Authorization", cfg.AuthScheme, cfg.AuthHeader)
	}
	// The gateway answers the OpenAI /chat/completions shape; a URL without the
	// path would land on the Supabase function root and 404.
	if want := "/llm/v1/chat/completions"; len(cfg.BaseURL) < len(want) ||
		cfg.BaseURL[len(cfg.BaseURL)-len(want):] != want {
		t.Errorf("BaseURL = %q, want it to end with %q", cfg.BaseURL, want)
	}

	if models := GetProviderModels("alysis"); len(models) != 4 {
		t.Errorf("model catalogue has %d ids (%v), want 4 (the live gateway set)", len(models), models)
	}

	// GetCatalogByCategory is what the dashboard's /providers/catalog serialises,
	// so an entry present in providerRegistry but absent here is invisible.
	found := false
	for _, m := range GetCatalogByCategory()[CategoryOAuth] {
		if m.ID == "alysis" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Alysis is not in the OAuth category the dashboard catalog returns")
	}
}
