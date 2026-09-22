package dashboard

import (
	"testing"
)

// The Kiro control-plane payload uses camelCase ids and pairs a default model
// with the list; both must land in the catalogue, de-duplicated.
func TestNormalizeKiroModels(t *testing.T) {
	raw := []byte(`{
		"defaultModel": {"modelId":"auto","modelName":"Auto","rateMultiplier":1.0},
		"models": [
			{"modelId":"auto","modelName":"Auto"},
			{"modelId":"claude-sonnet-4.5","modelName":"Claude Sonnet 4.5"},
			{"modelId":"glm-5","modelName":"GLM 5"}
		]
	}`)
	got := normalizeKiroModels(raw)

	if len(got) != 3 {
		t.Fatalf("got %d models, want 3 (default + 2 unique, auto de-duplicated)", len(got))
	}
	if got[0].ID != "auto" {
		t.Errorf("first model = %q, want the default first", got[0].ID)
	}
	byID := make(map[string]UpstreamModel, len(got))
	for _, m := range got {
		byID[m.ID] = m
	}
	if m, ok := byID["claude-sonnet-4.5"]; !ok || m.Name != "Claude Sonnet 4.5" {
		t.Errorf("claude-sonnet-4.5 missing or mislabelled: %+v", m)
	}
	// Every entry is an LLM; the control plane reports no other kind.
	for _, m := range got {
		if m.Kind != "llm" {
			t.Errorf("%s kind = %q, want llm", m.ID, m.Kind)
		}
	}
}

// A response that does not parse must yield nothing rather than an empty
// success, so the caller falls back to the static catalogue.
func TestNormalizeKiroModels_Malformed(t *testing.T) {
	for _, raw := range []string{`{`, `null`, `[]`, `{"models":"nope"}`} {
		if got := normalizeKiroModels([]byte(raw)); len(got) != 0 {
			t.Errorf("normalizeKiroModels(%s) = %d models, want 0", raw, len(got))
		}
	}
}

// An entry without a modelId is not addressable, so it must be dropped instead
// of producing a row the router cannot call.
func TestNormalizeKiroModels_SkipsEmptyID(t *testing.T) {
	raw := []byte(`{"models":[{"modelId":"","modelName":"Nameless"},{"modelId":"glm-5","modelName":"GLM 5"}]}`)
	got := normalizeKiroModels(raw)
	if len(got) != 1 || got[0].ID != "glm-5" {
		t.Fatalf("got %+v, want only glm-5", got)
	}
}

// A missing display name falls back to a humanised id rather than an empty row.
func TestNormalizeKiroModels_HumanizesMissingName(t *testing.T) {
	raw := []byte(`{"models":[{"modelId":"qwen3-coder-next"}]}`)
	got := normalizeKiroModels(raw)
	if len(got) != 1 {
		t.Fatalf("got %d models, want 1", len(got))
	}
	if got[0].Name == "" {
		t.Error("name was left empty; expected a humanised id")
	}
}

// The static fallback must produce a non-empty catalogue, otherwise Import
// reports nothing usable when the control plane is unreachable.
func TestKiroStaticModels_NonEmpty(t *testing.T) {
	got := kiroStaticModels("kiro")
	if len(got) == 0 {
		t.Fatal("static fallback is empty")
	}
	for _, m := range got {
		if m.ID == "" || m.Name == "" {
			t.Errorf("incomplete entry: %+v", m)
		}
	}
}

// isKiroProvider must accept the alias so a request addressed either way is
// routed to the Kiro fetcher rather than the generic registry lookup.
func TestIsKiroProvider(t *testing.T) {
	for _, id := range []string{"kiro", "kr", "KIRO", " kr "} {
		if !isKiroProvider(id) {
			t.Errorf("isKiroProvider(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"kilocode", "kimi", ""} {
		if isKiroProvider(id) {
			t.Errorf("isKiroProvider(%q) = true, want false", id)
		}
	}
}

// CodeBuddy CN must produce the same catalogue as intl: VansRouter lists an
// identical model set for both registry entries, and they share one backend
// lineage, so a divergence here would be a bug rather than a feature.
func TestCodebuddyCatalog_CNMatchesIntl(t *testing.T) {
	if !isCodebuddyProvider("codebuddy-cn") {
		t.Fatal("codebuddy-cn is not routed to the CodeBuddy catalogue")
	}
	cn := codebuddyStaticModels()
	intl := codebuddyStaticModels()
	if len(cn) == 0 {
		t.Fatal("catalogue is empty")
	}
	if len(cn) != len(intl) {
		t.Fatalf("cn has %d models, intl %d", len(cn), len(intl))
	}
}

// The retired list is what keeps Import from offering models the gateway
// answers with 11102. Every entry must actually be filtered out.
func TestCodebuddyCatalog_RetiredModelsFiltered(t *testing.T) {
	got := codebuddyStaticModels()
	served := make(map[string]bool, len(got))
	for _, m := range got {
		served[m.ID] = true
	}
	for id := range codebuddyRetiredModelIDs {
		if served[id] {
			t.Errorf("retired model %q is still offered by Import", id)
		}
	}
}

// deepseek-v4.1-flash works on a live account while the sibling deepseek-v4-pro
// is rejected with 11102, so the two must be classified differently. This pins
// that distinction: the availability probe found them opposite.
func TestCodebuddyCatalog_DeepseekFlashAvailableProUnavailable(t *testing.T) {
	served := make(map[string]bool)
	for _, m := range codebuddyStaticModels() {
		served[m.ID] = true
	}
	if !served["deepseek-v4.1-flash"] {
		t.Error("deepseek-v4.1-flash is available on a live account but is not offered")
	}
	if served["deepseek-v4-pro"] {
		t.Error("deepseek-v4-pro returns 11102 on a live account but is still offered")
	}
}

// 14003 means rate limited, not unavailable. An early probe conflated the two
// and wrongly dropped glm-5.1, which does work, so this pins it as offered.
func TestCodebuddyCatalog_Glm51Offered(t *testing.T) {
	served := make(map[string]bool)
	for _, m := range codebuddyStaticModels() {
		served[m.ID] = true
	}
	for _, id := range []string{"glm-5.2", "glm-5.1", "glm-5v-turbo", "minimax-m3"} {
		if !served[id] {
			t.Errorf("%s works on a live account but is not offered", id)
		}
	}
}
