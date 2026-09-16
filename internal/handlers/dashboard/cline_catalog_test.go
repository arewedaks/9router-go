package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsClineFamily pins the id set: both namespaces share a host and auth
// contract, so both must be routed to the Cline catalogue code.
func TestIsClineFamily(t *testing.T) {
	for _, id := range []string{"cline", "cl", "clinepass", "cline-pass", "cp", "CLINE"} {
		if !isClineFamily(id) {
			t.Fatalf("isClineFamily(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"github", "codebuddy-intl", "antigravity", ""} {
		if isClineFamily(id) {
			t.Fatalf("isClineFamily(%q) = true, want false", id)
		}
	}
	if !isClinePassProvider("clinepass") || isClinePassProvider("cline") {
		t.Fatal("isClinePassProvider misclassifies cline vs clinepass")
	}
}

// TestParseClineCatalogKeepsClineNamespace pins the namespace split: the free
// Cline catalogue must NOT advertise ClinePass subscription ids, because those
// answer `403 ENTITLEMENT_ERROR` without an active subscription.
func TestParseClineCatalogKeepsClineNamespace(t *testing.T) {
	raw := []byte(`{"data":[
		{"id":"cline-free/deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash"},
		{"id":"cline-pass/glm-5.3","name":"GLM 5.3"},
		{"id":"stealth/union-alpha","name":"Union Alpha"}
	]}`)

	models := parseClineCatalog(raw, false)
	ids := idsOf(models)

	for _, id := range ids {
		if strings.HasPrefix(id, clinePassModelPrefix) {
			t.Fatalf("cline catalogue leaked ClinePass id %q", id)
		}
	}
	if !contains(ids, "cline-free/deepseek-v4.1-flash") {
		t.Fatalf("free model dropped: %v", ids)
	}
	if !contains(ids, "stealth/union-alpha") {
		t.Fatalf("regular model dropped: %v", ids)
	}
	if got := nameOf(models, "cline-free/deepseek-v4.1-flash"); got != "DeepSeek V4.1 Flash" {
		t.Fatalf("display name = %q, want %q", got, "DeepSeek V4.1 Flash")
	}
}

// TestParseClineCatalogPassKeepsOnlySubscriptionNamespace is the mirror rule:
// ClinePass must expose only `cline-pass/*`.
func TestParseClineCatalogPassKeepsOnlySubscriptionNamespace(t *testing.T) {
	raw := []byte(`{
		"recommended":[{"id":"openai/gpt-6-astra","name":"GPT-6 Astra"}],
		"free":[{"id":"cline-free/deepseek-v4.1-flash","name":"DeepSeek"}],
		"clinePass":[{"id":"cline-pass/glm-5.3","name":"GLM 5.3"},{"id":"cline-pass/kimi-k3","name":"Kimi K3"}],
		"clineCloud":[{"id":"cline-cloud/glm-5.2","name":"GLM Cloud"}]
	}`)

	models := parseClineCatalog(raw, true)
	ids := idsOf(models)

	if len(ids) != 2 {
		t.Fatalf("got %d rows %v, want 2 cline-pass rows", len(ids), ids)
	}
	for _, id := range ids {
		if !strings.HasPrefix(id, clinePassModelPrefix) {
			t.Fatalf("clinepass catalogue leaked id %q", id)
		}
	}
}

// TestParseClineCatalogFiltersNonTextOutput drops embedding/rerank rows that
// cannot serve a chat completion.
func TestParseClineCatalogFiltersNonTextOutput(t *testing.T) {
	raw := []byte(`{"data":[
		{"id":"chat/ok","name":"OK","architecture":{"modality":"text->text"}},
		{"id":"embed/only","name":"Embed","architecture":{"modality":"text->embeddings"}},
		{"id":"img/only","name":"Img","architecture":{"output_modalities":["image"]}},
		{"id":"multi/ok","name":"Multi","architecture":{"output_modalities":["text","image"]}}
	]}`)

	ids := idsOf(parseClineCatalog(raw, false))

	if !contains(ids, "chat/ok") {
		t.Fatalf("text model dropped: %v", ids)
	}
	if !contains(ids, "multi/ok") {
		t.Fatalf("multimodal-with-text model dropped: %v", ids)
	}
	if contains(ids, "embed/only") {
		t.Fatalf("embedding row admitted: %v", ids)
	}
	if contains(ids, "img/only") {
		t.Fatalf("image-only row admitted: %v", ids)
	}
}

// TestParseClineCatalogFailsOpenWithoutArchitecture guards against a schema
// change silently emptying the catalogue: a row with no architecture metadata
// is admitted, not dropped.
func TestParseClineCatalogFailsOpenWithoutArchitecture(t *testing.T) {
	raw := []byte(`{"data":[{"id":"no/arch","name":"No Arch"}]}`)
	ids := idsOf(parseClineCatalog(raw, false))
	if !contains(ids, "no/arch") {
		t.Fatalf("row without architecture was dropped: %v", ids)
	}
}

// TestParseClineCatalogDeduplicates keeps the Import list free of repeats.
func TestParseClineCatalogDeduplicates(t *testing.T) {
	raw := []byte(`{"data":[{"id":"a/b","name":"A"},{"id":"a/b","name":"A again"}]}`)
	models := parseClineCatalog(raw, false)
	if len(models) != 1 {
		t.Fatalf("got %d rows, want 1 after dedupe", len(models))
	}
}

// TestParseClineCatalogNameFallsBackToID ensures a nameless row still produces a
// usable label instead of an empty string.
func TestParseClineCatalogNameFallsBackToID(t *testing.T) {
	models := parseClineCatalog([]byte(`{"data":[{"id":"no/name"}]}`), false)
	if len(models) != 1 || models[0].Name != "no/name" {
		t.Fatalf("name = %q, want the id as fallback", models[0].Name)
	}
}

// TestParseClineCatalogAcceptsBareArray covers an upstream that returns the
// catalogue as a top-level array rather than {"data":[...]}.
func TestParseClineCatalogAcceptsBareArray(t *testing.T) {
	ids := idsOf(parseClineCatalog([]byte(`[{"id":"bare/array","name":"Bare"}]`), false))
	if !contains(ids, "bare/array") {
		t.Fatalf("bare array not parsed: %v", ids)
	}
}

// TestParseClineCatalogFreeBadge verifies free entries are flagged so the Import
// modal shows the same badge as the detail page. Cline namespaces free models as
// `cline-free/*`, so the badge rule must consider the id, not only the human
// name ("DeepSeek V4.1 Flash" contains no "free" word).
func TestParseClineCatalogFreeBadge(t *testing.T) {
	models := parseClineCatalog([]byte(`{"data":[
		{"id":"cline-free/deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash"},
		{"id":"anthropic/claude-opus-5","name":"Claude Opus 5"}
	]}`), false)
	if !isFree(models, "cline-free/deepseek-v4.1-flash") {
		t.Fatal("cline-free/* entry was not flagged free")
	}
	if isFree(models, "anthropic/claude-opus-5") {
		t.Fatal("paid entry was flagged free")
	}
}

// TestClineFallbackModelsExcludesSubscriptionIds keeps the offline catalogue on
// the same side of the namespace split as the live one.
func TestClineFallbackModelsExcludesSubscriptionIds(t *testing.T) {
	for _, id := range idsOf(clineFallbackModels(false)) {
		if strings.HasPrefix(id, clinePassModelPrefix) {
			t.Fatalf("fallback leaked ClinePass id %q", id)
		}
	}
	if got := clineFallbackModels(true); len(got) != 0 {
		t.Fatalf("clinepass fallback = %d rows, want 0", len(got))
	}
}

// TestParseClineCatalogAgainstLiveFixture parses a captured live response when
// one is present. The fixture is optional so the suite stays hermetic.
func TestParseClineCatalogAgainstLiveFixture(t *testing.T) {
	fixture := filepath.Join(os.TempDir(), "cline_models_fixture.json")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Skip("no live fixture captured")
	}
	models := parseClineCatalog(raw, false)
	if len(models) == 0 {
		t.Fatal("live fixture parsed to zero models")
	}
	for _, m := range models {
		if strings.HasPrefix(m.ID, clinePassModelPrefix) {
			t.Fatalf("live catalogue leaked ClinePass id %q", m.ID)
		}
		if m.Name == "" {
			t.Fatalf("model %q has no display name", m.ID)
		}
	}
}

// --- helpers ---

func idsOf(models []UpstreamModel) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func nameOf(models []UpstreamModel, id string) string {
	for _, m := range models {
		if m.ID == id {
			return m.Name
		}
	}
	return ""
}

func isFree(models []UpstreamModel, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return m.IsFree
		}
	}
	return false
}
