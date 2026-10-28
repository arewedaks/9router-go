package dashboard

import (
	"path/filepath"
	"testing"

	"9router/proxy/internal/db"
)

// The per-account test asks the upstream for exactly one model. Pin it per
// provider so a retired free model (OpenRouter rotates its :free list) does not
// make every healthy account look broken.
func TestProbeModelPrefersPinnedPerProviderModel(t *testing.T) {
	database, err := db.OpenDatabase(filepath.Join(t.TempDir(), "probe_model.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.EnsureSchema(database); err != nil {
		t.Fatalf("schema: %v", err)
	}
	repo := db.NewRepo(database)
	h := &Handler{repo: repo}

	// Nothing pinned yet: the built-in default is in charge.
	if got := h.probeModel("openrouter"); got != "openai/gpt-4o-mini" {
		t.Fatalf("default probe model = %q, want openai/gpt-4o-mini", got)
	}
	if got := h.probeModel("unknown-provider"); got != "gpt-4o-mini" {
		t.Fatalf("fallback probe model = %q, want gpt-4o-mini", got)
	}

	if err := repo.SetProviderStrategy("openrouter", db.ProviderStrategy{TestModel: "z-ai/glm-4.5-air:free"}); err != nil {
		t.Fatalf("pin test model: %v", err)
	}
	if got := h.probeModel("openrouter"); got != "z-ai/glm-4.5-air:free" {
		t.Fatalf("pinned probe model = %q, want the operator's free model", got)
	}
	// A provider that was not pinned keeps its built-in default.
	if got := h.probeModel("wally"); got != genericProbeModel("wally") {
		t.Fatalf("wally probe model = %q, want the built-in default", got)
	}
}
