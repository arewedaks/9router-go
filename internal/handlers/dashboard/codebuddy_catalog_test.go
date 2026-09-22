package dashboard

import (
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

// TestCodebuddyProviderDetection pins which ids route to the CodeBuddy
// catalogue. A miss here means Import falls through to the generic registry
// lookup and fails with "does not support models listing" — the exact bug this
// change fixes.
func TestCodebuddyProviderDetection(t *testing.T) {
	yes := []string{"codebuddy-cn", "codebuddy-intl", "cbcn", "cbai", "CodeBuddy-CN", " CODEBUDDY-INTL "}
	for _, id := range yes {
		if !isCodebuddyProvider(id) {
			t.Errorf("isCodebuddyProvider(%q) = false, want true", id)
		}
	}
	no := []string{"", "antigravity", "agy", "openai", "cline", "codebuddy"}
	for _, id := range no {
		if isCodebuddyProvider(id) {
			t.Errorf("isCodebuddyProvider(%q) = true, want false", id)
		}
	}
}

// TestFetchCodebuddyModels_ReturnsStaticCatalogue is the core regression: the
// fetch must SUCCEED with a non-empty list and never report an error, because
// there is no /models endpoint to fail against.
func TestFetchCodebuddyModels_ReturnsStaticCatalogue(t *testing.T) {
	h := &Handler{}
	for _, alias := range []string{"codebuddy-cn", "cbcn", "codebuddy-intl", "cbai"} {
		res, err := h.fetchCodebuddyModels(alias)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", alias, err)
		}
		if res == nil {
			t.Fatalf("%s: nil result", alias)
		}
		if !res.Supported {
			t.Errorf("%s: Supported = false, want true (so Import is enabled)", alias)
		}
		if len(res.Models) == 0 {
			t.Fatalf("%s: empty catalogue", alias)
		}
		if res.Error != "" {
			t.Errorf("%s: Error should be empty, got %q", alias, res.Error)
		}
		if res.Warning == "" {
			t.Errorf("%s: Warning should explain the static catalogue", alias)
		}
		// Aliases must resolve to the canonical id so cache keys are stable.
		if res.Provider != providers.ResolveAlias(alias) {
			t.Errorf("%s: Provider = %q, want %q", alias, res.Provider, providers.ResolveAlias(alias))
		}
	}
}

// TestCodebuddyCatalogue_ExcludesRetiredAndNonChat guards the two filter rules:
// an id upstream now rejects with "service info not found" must not be offered,
// and non-chat surfaces must be dropped.
func TestCodebuddyCatalogue_ExcludesRetiredAndNonChat(t *testing.T) {
	models := codebuddyStaticModels()
	seen := map[string]bool{}
	for _, m := range models {
		seen[m.ID] = true
	}
	if seen["deepseek-v4-flash"] {
		t.Error("deepseek-v4-flash is retired upstream (400 service info not found) but was offered")
	}
	for _, bad := range []string{"", "some-image-model", "glm-tts", "video-gen", "bge-embedding", "rerank-v1"} {
		if isDiscoverableCodebuddyModel(bad) {
			t.Errorf("isDiscoverableCodebuddyModel(%q) = true, want false", bad)
		}
	}
	// At least one real model from each surviving family must be present, or the
	// catalogue silently regressed to the blocklist behaviour.
	//
	// The expected ids are the ones a live account actually served. minimax-m2.7,
	// deepseek-v4-pro and hy3-preview were listed here before they were probed:
	// all three now answer 11102 ("model is not available"), so they are retired
	// rather than expected. minimax-m3 and deepseek-v4.1-flash are their
	// available counterparts.
	for _, want := range []string{"glm-5.1", "minimax-m3", "deepseek-v4.1-flash"} {
		if !seen[want] {
			t.Errorf("expected %q in the CodeBuddy catalogue", want)
		}
	}
	// Ids that only the intl registry (VansRouter codebuddy-intl.js) lists must
	// also be present, otherwise the union catalogue dropped them.
	for _, want := range []string{"glm-5.2", "glm-5v-turbo", "minimax-m3", "kimi-k2.7", "kimi-k2.6"} {
		if !seen[want] {
			t.Errorf("expected intl model %q in the CodeBuddy catalogue", want)
		}
	}
}

// TestCodebuddyModelsCarryKind ensures imported entries declare a kind, since
// the UI renders it and a blank kind reads as unknown.
func TestCodebuddyModelsCarryKind(t *testing.T) {
	for _, m := range codebuddyStaticModels() {
		if m.Kind == "" {
			t.Errorf("model %q has empty kind", m.ID)
		}
	}
}

// TestFetchUpstreamModels_RoutesCodebuddyToCatalogue proves the wiring in
// fetchUpstreamModels: a codebuddy provider must NOT reach the generic branch
// that answers "does not support models listing".
func TestFetchUpstreamModels_RoutesCodebuddyToCatalogue(t *testing.T) {
	h := &Handler{}
	res, err := h.fetchUpstreamModels("cbcn", `{"accessToken":"x"}`, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || !res.Supported || len(res.Models) == 0 {
		t.Fatalf("codebuddy did not route to the static catalogue: %+v", res)
	}
	if res.Error != "" {
		t.Fatalf("codebuddy import reported an error: %q", res.Error)
	}
}
