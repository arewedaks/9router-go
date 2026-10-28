package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// /v1/models must advertise the limits the upstream published at import time,
// not the guess made from the model's name. This is what makes a brand-new
// OpenAI-compatible node report the right context window without anyone
// editing the static tables.
func TestHandleModelsUsesLimitsFromImportCache(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('cb-conn', 'codebuddy', 'apikey', 'CB', 0, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	// A model id no token-family row can match: only the cache can be right.
	if err := repo.AddCachedModelWithLimits("oc", "mystery-model", "llm", "imported", "", 777000, 12345); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}

	rec := httptest.NewRecorder()
	h.HandleModels(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
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
		if m.ID != "oc/mystery-model" {
			continue
		}
		if m.ContextWindow == nil || *m.ContextWindow != 777000 {
			t.Fatalf("context_window = %v, want 777000 from the import cache", m.ContextWindow)
		}
		if m.MaxOut == nil || *m.MaxOut != 12345 {
			t.Fatalf("max_completion_tokens = %v, want 12345 from the import cache", m.MaxOut)
		}
		return
	}
	t.Fatalf("oc/mystery-model not advertised")
}
