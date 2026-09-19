package dashboard

import (
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
