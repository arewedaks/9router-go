package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// The production shape: an openai-compatible node is stored under a generated
// id, its models under the short prefix, and the limits pinned in
// catalogLimitOverrides are keyed by that prefix. The listing used to resolve
// with the bare model id and the opaque provider, so LunaRoute's 1M-token
// deepseek was advertised as the family's 1000000/128000.
func TestHandleModelsPrefersPinnedOverrideForOpaqueNode(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	const node = "openai-compatible-chat-d76ef300-b269-4a63-973f-e54dd52d985f"
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('lr-conn', ?, 'apikey', 'LunaRoute', 0, 1, '{"apiKey":"k","prefix":"lr"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		node); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	// Cached with no limits: this upstream publishes none, so only the pinned
	// override can produce the real window.
	if err := repo.AddCachedModelWithLimits(node, "deepseek-4.1-flash", "llm", "imported", "", 0, 0); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}

	rec := httptest.NewRecorder()
	h.HandleModels(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data []struct {
			ID            string `json:"id"`
			ContextWindow *int   `json:"context_window"`
			MaxOut        *int   `json:"max_completion_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, m := range resp.Data {
		if m.ID != "lr/deepseek-4.1-flash" {
			continue
		}
		if m.ContextWindow == nil || *m.ContextWindow != 1048576 {
			t.Fatalf("context_window = %v, want 1048576", m.ContextWindow)
		}
		if m.MaxOut == nil || *m.MaxOut != 393216 {
			t.Fatalf("max_completion_tokens = %v, want 393216", m.MaxOut)
		}
		return
	}
	t.Fatalf("lr/deepseek-4.1-flash not advertised; got %d models", len(resp.Data))
}
