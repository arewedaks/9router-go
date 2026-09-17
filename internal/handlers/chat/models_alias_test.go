package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// TestHandleModelsUsesNodePrefixForCustomModels pins the fix for uuid-prefixed
// model names. A restored VansRouter backup stores custom models under the
// generated node id ("openai-compatible-chat-<uuid>"), but clients must be told
// the configured prefix ("bai"), exactly as upstream builds
// `${outputAlias}/${modelId}` with `outputAlias = prefix || alias || providerId`.
func TestHandleModelsUsesNodePrefixForCustomModels(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	const nodeID = "openai-compatible-chat-8e96ef73-6f8a-4043-bcf4-42285b6de114"
	nodeBlob := `{"prefix":"bai","apiType":"chat","baseUrl":"https://api.b.ai/v1"}`
	if _, err := database.Exec(
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		nodeID, "openai-compatible", "BAI", nodeBlob, "2026-07-18T00:00:00Z", "2026-07-18T00:00:00Z",
	); err != nil {
		t.Fatalf("seed providerNodes: %v", err)
	}

	// Custom models are stored as scope='customModels' with the identity in the
	// key and the record in the value (providerAlias/id live in the JSON, not in
	// the key — see Repo.GetCustomModels).
	nodeKeyed := nodeID + "|glm-5.3-flash|llm"
	prefixKeyed := "bai|glm-5.2|llm"
	seed := map[string]string{
		nodeKeyed:   `{"id":"glm-5.3-flash","providerAlias":"` + nodeID + `","type":"llm"}`,
		prefixKeyed: `{"id":"glm-5.2","providerAlias":"bai","type":"llm"}`,
	}
	for k, v := range seed {
		if _, err := database.Exec(
			`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`, k, v,
		); err != nil {
			t.Fatalf("seed customModels %q: %v", k, err)
		}
	}

	h := &ChatHandler{Repo: db.NewRepo(database)}
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

	if _, leaked := ids[nodeID+"/glm-5.3-flash"]; leaked {
		t.Fatalf("model still advertised under the generated node id: %v", ids)
	}
	if _, ok := ids["bai/glm-5.3-flash"]; !ok {
		t.Fatalf("node-keyed custom model not exported under the prefix \"bai\": %v", ids)
	}
	if owner := ids["bai/glm-5.3-flash"]; owner != "bai" {
		t.Fatalf("owned_by = %q, want bai", owner)
	}
	// A model already keyed by the prefix must not be lost or renamed.
	if _, ok := ids["bai/glm-5.2"]; !ok {
		t.Fatalf("prefix-keyed custom model missing: %v", ids)
	}

	// No model id may contain the generated node id anywhere.
	for id := range ids {
		if strings.Contains(id, "openai-compatible-chat-") {
			t.Fatalf("model id leaks a generated provider key: %q", id)
		}
	}
}

// TestHandleModelsInfoOwnsByPrefixNotNodeID pins that /v1/models/info agrees with
// /v1/models. handleModels exports "atri/..." with owned_by "atri"; info must
// not answer with the generated node id for the same model.
func TestHandleModelsInfoOwnsByPrefixNotNodeID(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	const nodeID = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	nodeBlob := `{"prefix":"atri","apiType":"chat","baseUrl":"https://api.atria-asi.ai/v1"}`
	if _, err := database.Exec(
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		nodeID, "openai-compatible", "Atria AI", nodeBlob, "2026-07-18T00:00:00Z", "2026-07-18T00:00:00Z",
	); err != nil {
		t.Fatalf("seed providerNodes: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('at-1',?,'apikey','Atria',1,1,'{}',?,?)`,
		nodeID, "2026-07-18T00:00:00Z", "2026-07-18T00:00:00Z",
	); err != nil {
		t.Fatalf("seed providerConnections: %v", err)
	}

	h := &ChatHandler{Repo: db.NewRepo(database)}
	req := httptest.NewRequest("GET", "/v1/models/info?id=atri/Atria-Dawn-Preview", nil)
	w := httptest.NewRecorder()
	h.HandleModelsInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var info struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.OwnedBy != "atri" {
		t.Fatalf("owned_by = %q, want atri (node id must never leak)", info.OwnedBy)
	}
	if strings.Contains(info.OwnedBy, "openai-compatible-chat-") {
		t.Fatalf("owned_by leaks a generated node id: %q", info.OwnedBy)
	}
}
