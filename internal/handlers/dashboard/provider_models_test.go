package dashboard

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// providerDetailBody is the subset of GET /api/dashboard/providers/{id} these
// tests assert on.
type providerDetailBody struct {
	Provider string           `json:"provider"`
	Models   []db.CachedModel `json:"models"`
}

// The provider detail page must list the models the operator imported, whatever
// table the importer wrote them to.
//
// Two stores hold imported models. AddCachedModel writes cachedProviderModels,
// but a database restored from the Next.js build carries its catalogue in kv
// scope customModels under the key "<provider>|<model>|<kind>". Only the first
// was read here, so a provider whose 72 models lived in customModels rendered an
// empty Models tab while /v1/models — which reads customModels — listed all 72.
// An operator saw an empty page and thousands of callable ids.
func TestProviderDetailListsModelsFromCustomModelScope(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	// Written the way a restored Next.js database stores it.
	if _, err := repo.DB().Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
		"nvidia|z-ai/glm-5.2|llm", `{"providerAlias":"nvidia","id":"z-ai/glm-5.2","type":"llm"}`,
	); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}

	var body providerDetailBody
	if code := getJSON(t, r, "/api/dashboard/providers/nvidia", &body); code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(body.Models) != 1 {
		t.Fatalf("models = %d, want 1 (the imported model): %+v", len(body.Models), body.Models)
	}
	if body.Models[0].ModelID != "z-ai/glm-5.2" {
		t.Errorf("modelId = %v, want z-ai/glm-5.2", body.Models[0].ModelID)
	}
}

// The cachedProviderModels store must keep working: this is the path a fresh
// import takes, and reading only customModels would break it instead.
func TestProviderDetailListsModelsFromCacheTable(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if err := repo.AddCachedModelWithName("nvidia", "deepseek-ai/deepseek-v4-pro", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}

	var body providerDetailBody
	if code := getJSON(t, r, "/api/dashboard/providers/nvidia", &body); code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(body.Models) != 1 {
		t.Fatalf("models = %d, want 1: %+v", len(body.Models), body.Models)
	}
	if body.Models[0].ModelID != "deepseek-ai/deepseek-v4-pro" {
		t.Errorf("modelId = %v", body.Models[0].ModelID)
	}
}

// A model present in both stores must appear once, not twice.
func TestProviderDetailDeduplicatesModelsAcrossStores(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if err := repo.AddCachedModelWithName("nvidia", "shared-model", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}
	if _, err := repo.DB().Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
		"nvidia|shared-model|llm", `{"providerAlias":"nvidia","id":"shared-model","type":"llm"}`,
	); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}

	var body providerDetailBody
	if code := getJSON(t, r, "/api/dashboard/providers/nvidia", &body); code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(body.Models) != 1 {
		t.Errorf("models = %d, want 1 (deduplicated): %+v", len(body.Models), body.Models)
	}
}

// Another provider's custom models must not leak into this one. The scope holds
// every provider's rows in one table, keyed by prefix.
func TestProviderDetailDoesNotLeakOtherProvidersCustomModels(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if _, err := repo.DB().Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
		"openrouter|some/or-model|llm", `{"providerAlias":"openrouter","id":"some/or-model","type":"llm"}`,
	); err != nil {
		t.Fatalf("seed foreign custom model: %v", err)
	}

	var body providerDetailBody
	if code := getJSON(t, r, "/api/dashboard/providers/nvidia", &body); code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(body.Models) != 0 {
		t.Errorf("another provider's models leaked into nvidia: %+v", body.Models)
	}
}

// Importing models from an OpenAI-compatible provider node must use the node's
// BaseURL even when the connection data blob only carries the apiKey.
func TestImportModelsCompatibleNodeBaseURL(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// Spin up fake upstream server with /models endpoint
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/models" && req.URL.Path != "/models" {
			http.NotFound(w, req)
			return
		}
		if auth := req.Header.Get("Authorization"); auth != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"custom-gpt-4o","name":"Custom GPT-4o"}]}`))
	}))
	defer ts.Close()

	nodeID := "openai-compatible-chat-testnode1"
	nodeData := fmt.Sprintf(`{"prefix":"testnode","apiType":"chat","baseUrl":%q,"nodeName":"Test Node"}`, ts.URL)
	if _, err := repo.DB().Exec(
		`INSERT INTO providerNodes (id, name, type, data, createdAt, updatedAt)
		 VALUES (?, 'Test Node', 'openai-compatible', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID, nodeData,
	); err != nil {
		t.Fatalf("seed provider node: %v", err)
	}

	// Connection only stores apiKey in data, NOT baseUrl
	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('c-1', ?, 'apikey', 'main', 1, 1, '{"apiKey":"test-key"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	// 1. GET /api/dashboard/providers/{nodeID}/models
	var fetchRes ModelFetchResult
	if code := getJSON(t, r, "/api/dashboard/providers/"+nodeID+"/models", &fetchRes); code != 200 {
		t.Fatalf("fetch models status = %d, want 200, error: %s", code, fetchRes.Error)
	}
	if len(fetchRes.Models) != 1 {
		t.Fatalf("got %d models, want 1: %+v", len(fetchRes.Models), fetchRes.Models)
	}
	if fetchRes.Models[0].ID != "custom-gpt-4o" {
		t.Errorf("model ID = %q, want custom-gpt-4o", fetchRes.Models[0].ID)
	}

	// 2. POST /api/dashboard/providers/{nodeID}/models (save model)
	w := postJSON(t, r, "/api/dashboard/providers/"+nodeID+"/models", `{"modelId":"custom-gpt-4o","name":"Custom GPT-4o","kind":"llm"}`)
	if w.Code != 200 {
		t.Fatalf("add model status = %d, want 200: %s", w.Code, w.Body.String())
	}

	// 3. GET /api/dashboard/providers/{nodeID} verifies the model is present
	var detail providerDetailBody
	if code := getJSON(t, r, "/api/dashboard/providers/"+nodeID, &detail); code != 200 {
		t.Fatalf("detail status = %d, want 200", code)
	}
	if len(detail.Models) != 1 {
		t.Fatalf("cached models = %d, want 1", len(detail.Models))
	}
	if detail.Models[0].ModelID != "custom-gpt-4o" {
		t.Errorf("cached model ID = %q, want custom-gpt-4o", detail.Models[0].ModelID)
	}
	if detail.Models[0].DisplayName != "Custom GPT-4o" {
		t.Errorf("cached model DisplayName = %q, want Custom GPT-4o", detail.Models[0].DisplayName)
	}
}

func TestAddModelUnhidesAcrossNodePrefix(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	nodeID := "openai-compatible-chat-prefixnode"
	nodeData := `{"prefix":"pfix","apiType":"chat","nodeName":"Prefix Node"}`
	if _, err := repo.DB().Exec(
		`INSERT INTO providerNodes (id, name, type, data, createdAt, updatedAt)
		 VALUES (?, 'Prefix Node', 'openai-compatible', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID, nodeData,
	); err != nil {
		t.Fatalf("seed provider node: %v", err)
	}

	// Hide model under the node prefix (e.g. from previous removal or auto-disable)
	if err := repo.HideModel([]string{"pfix"}, "model-to-import"); err != nil {
		t.Fatalf("hide model: %v", err)
	}

	// Add/Import model via POST /providers/{nodeID}/models
	w := postJSON(t, r, "/api/dashboard/providers/"+nodeID+"/models", `{"modelId":"model-to-import","name":"Model To Import","kind":"llm"}`)
	if w.Code != 200 {
		t.Fatalf("add model status = %d: %s", w.Code, w.Body.String())
	}

	// Verify model appears in GET /api/dashboard/providers/{nodeID}
	var detail providerDetailBody
	if code := getJSON(t, r, "/api/dashboard/providers/"+nodeID, &detail); code != 200 {
		t.Fatalf("detail status = %d, want 200", code)
	}
	if len(detail.Models) != 1 {
		t.Fatalf("detail models count = %d, want 1", len(detail.Models))
	}
	if detail.Models[0].ModelID != "model-to-import" {
		t.Errorf("model ID = %q, want model-to-import", detail.Models[0].ModelID)
	}
}
