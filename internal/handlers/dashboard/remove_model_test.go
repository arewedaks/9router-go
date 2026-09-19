package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/handlers/chat"
)

// TestRemoveModelHidesFromV1Models is the end-to-end guard for the "Remove
// model" button. It drives DELETE /api/dashboard/providers/{id}/models exactly
// as the embedded UI does, then asks the chat handler for /v1/models and asserts
// the removed model is gone. Before the fix the delete only touched
// cachedProviderModels, a table the engine never reads, so the model reappeared.
func TestRemoveModelHidesFromV1Models(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// A provider connection with an imported catalogue: provider models are only
	// advertised once imported, so the baseline has to seed the cache.
	connData := `{"apiKey":"sk-test"}`
	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('ds-1','deepseek','apikey','DeepSeek',1,1,?,?,?)`,
		connData, "2026-07-18T00:00:00Z", "2026-07-18T00:00:00Z",
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	for _, m := range []string{"deepseek-chat", "deepseek-reasoner"} {
		if err := repo.AddCachedModelWithName("deepseek", m, "llm", "imported", ""); err != nil {
			t.Fatalf("seed cached model %s: %v", m, err)
		}
	}

	chatH := &chat.ChatHandler{Repo: repo}
	modelsBefore := v1ModelIDs(t, chatH)
	if _, ok := modelsBefore["ds/deepseek-chat"]; !ok {
		t.Fatalf("baseline: ds/deepseek-chat missing: %v", modelsBefore)
	}

	// Press "Remove model" for deepseek-chat via the real dashboard route.
	req := httptest.NewRequest("DELETE", "/api/dashboard/providers/deepseek/models?modelId=deepseek-chat", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("remove model status = %d, want 200 (%s)", w.Code, w.Body.String())
	}

	modelsAfter := v1ModelIDs(t, chatH)
	if _, ok := modelsAfter["ds/deepseek-chat"]; ok {
		t.Errorf("model still advertised after Remove: %v", modelsAfter)
	}
	// Sibling intact.
	if _, ok := modelsAfter["ds/deepseek-reasoner"]; !ok {
		t.Errorf("unrelated model vanished: %v", modelsAfter)
	}

	// Re-adding via the dashboard route must restore visibility.
	add := httptest.NewRequest("POST", "/api/dashboard/providers/deepseek/models",
		bytes.NewBufferString(`{"modelId":"deepseek-chat","kind":"llm"}`))
	add.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, add)
	if w2.Code != http.StatusOK {
		t.Fatalf("add model status = %d, want 200 (%s)", w2.Code, w2.Body.String())
	}
	restored := v1ModelIDs(t, chatH)
	if _, ok := restored["ds/deepseek-chat"]; !ok {
		t.Errorf("re-added model did not come back: %v", restored)
	}
}

// v1ModelIDs decodes /v1/models through the chat handler.
func v1ModelIDs(t *testing.T, h *chat.ChatHandler) map[string]bool {
	t.Helper()
	rec := httptest.NewRecorder()
	h.HandleModels(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/models status = %d (%s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode /v1/models: %v", err)
	}
	out := make(map[string]bool, len(payload.Data))
	for _, m := range payload.Data {
		out[m.ID] = true
	}
	return out
}
