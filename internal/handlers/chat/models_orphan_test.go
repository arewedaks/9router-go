package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// decodeModelIDs runs HandleModels and returns id -> owned_by.
func decodeModelIDs(t *testing.T, h *ChatHandler) map[string]string {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	h.HandleModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	ids := make(map[string]string, len(payload.Data))
	for _, m := range payload.Data {
		ids[m.ID] = m.OwnedBy
	}
	return ids
}

// TestHandleModelsHidesOrphanCustomModels pins the "do not advertise dead
// models" rule. A restored VansRouter backup can hold custom models whose
// provider node was deleted upstream: no node, no connection. Upstream only
// exports aliases present in `activeConnectionByProvider`, so such an orphan
// must not appear in /v1/models. Real production data carried 1504 of these
// (e.g. `openai-compatible-chat-16dc7b96-...`), which flooded clients.
func TestHandleModelsHidesOrphanCustomModels(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	orphan := "openai-compatible-chat-16dc7b96-9c2f-4cf5-9903-2543176744fb"
	// A name that is neither a KnownProvider nor an alias. Deliberately
	// fictional: using a real provider id here would break the moment that
	// provider is added to the registry.
	unknownPlain := "not-a-real-provider-zzz"
	seeds := map[string]string{
		orphan + "|gpt-5.5|llm":        `{"id":"gpt-5.5","providerAlias":"` + orphan + `","type":"llm"}`,
		unknownPlain + "|router-x|llm": `{"id":"router-x","providerAlias":"` + unknownPlain + `","type":"llm"}`,
		"gh|gpt-4o|llm":                `{"id":"gpt-4o","providerAlias":"gh","type":"llm"}`,
		"openrouter|deepseek-v3|llm":   `{"id":"deepseek-v3","providerAlias":"openrouter","type":"llm"}`,
	}
	for k, v := range seeds {
		if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`, k, v); err != nil {
			t.Fatalf("seed customModels %q: %v", k, err)
		}
	}

	// Only OpenRouter has a connection; `gh` is kept alive by its static alias
	// (github), mirroring the real database where `gh` had no connection row.
	connData := `{"apiKey":"sk-or-test"}`
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('or-1','openrouter','apikey','OpenRouter',1,1,?,?,?)`,
		connData, "2026-07-18T00:00:00Z", "2026-07-18T00:00:00Z",
	); err != nil {
		t.Fatalf("seed providerConnections: %v", err)
	}

	ids := decodeModelIDs(t, &ChatHandler{Repo: db.NewRepo(database)})

	if _, ok := ids[orphan+"/gpt-5.5"]; ok {
		t.Fatalf("orphan custom model was advertised: %v", ids)
	}
	if _, ok := ids[unknownPlain+"/router-x"]; ok {
		t.Fatalf("custom model with an unresolvable alias was advertised: %v", ids)
	}
	// A model whose alias maps to a known provider stays, even without a
	// connection row of its own — hiding it would drop working routes.
	if _, ok := ids["gh/gpt-4o"]; !ok {
		t.Fatalf("aliased provider model `gh/gpt-4o` was hidden: %v", ids)
	}
	// A model backed by a live connection stays.
	if _, ok := ids["openrouter/deepseek-v3"]; !ok {
		t.Fatalf("connected provider model `openrouter/deepseek-v3` was hidden: %v", ids)
	}
}
