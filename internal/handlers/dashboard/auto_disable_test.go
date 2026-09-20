package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestProviderModelsAutoDisableHidesModel is the guard for the "Auto-disable
// failed" checkbox on the provider Test toolbar.
//
// The checkbox has two halves and both were broken against a database restored
// from the Next.js build, whose catalogue lives in kv scope customModels rather
// than cachedProviderModels:
//
//  1. The provider's model list was read with ListCachedModels alone, so the
//     test endpoint saw an empty list and never tested anything — ticking the
//     box changed nothing because no model was ever reported as failed.
//  2. maybeAutoDisable only called RemoveCachedModel, which mutates a table the
//     model list does not read. Even a model that did fail stayed advertised.
//
// This drives the endpoint the way the UI does and asserts the failed model
// disappears from what /v1/models would report.
func TestProviderModelsAutoDisableHidesModel(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	// Written the way a restored Next.js database stores it. The model is
	// expected to fail its probe (no credential, no network), which is what
	// makes auto-disable apply.
	if _, err := repo.DB().Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
		"nvidia|01-ai/yi-large|llm", `{"providerAlias":"nvidia","id":"01-ai/yi-large","type":"llm"}`,
	); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}

	// The endpoint must find the model at all. Before the fix it saw none.
	body, _ := json.Marshal(map[string]any{
		"parallel":          false,
		"autoDisableFailed": true,
		"models":            []string{"nvidia/01-ai/yi-large"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/providers/nvidia/test-models", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("test-models status = %d (%s)", w.Code, w.Body.String())
	}

	// The failed model must now be hidden, the same marker the Remove button
	// writes. This is what keeps it out of /v1/models.
	hidden, err := repo.GetAllHiddenModels()
	if err != nil {
		t.Fatalf("GetAllHiddenModels: %v", err)
	}
	if !hidden["nvidia|01-ai/yi-large"] {
		t.Errorf("failed model was not hidden by auto-disable; hidden set = %v", hidden)
	}
}

// Ticking the box off must not remove anything, or every probe run would prune
// the catalogue.
func TestProviderModelsAutoDisableOffRemovesNothing(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if _, err := repo.DB().Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
		"nvidia|01-ai/yi-large|llm", `{"providerAlias":"nvidia","id":"01-ai/yi-large","type":"llm"}`,
	); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"parallel":          false,
		"autoDisableFailed": false,
		"models":            []string{"nvidia/01-ai/yi-large"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/providers/nvidia/test-models", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	hidden, err := repo.GetAllHiddenModels()
	if err != nil {
		t.Fatalf("GetAllHiddenModels: %v", err)
	}
	if hidden["nvidia|01-ai/yi-large"] {
		t.Errorf("auto-disable was off but the model was hidden anyway")
	}
}

// A timeout must not disable a model.
//
// The probe has a fixed budget (MODEL_TEST_TIMEOUT_MS, 30s by default) and a
// batch run over a node with 110 models will very often exceed it for reasons
// that say nothing about the model: a slow upstream, a busy relay, a cold
// start. Treating that as a failure deleted healthy entries, which is the
// opposite of what the box is for.
func TestAutoDisableSkipsTimedOutModel(t *testing.T) {
	_, repo, _ := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if _, err := repo.DB().Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
		"nvidia|slow-but-alive|llm", `{"providerAlias":"nvidia","id":"slow-but-alive","type":"llm"}`,
	); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}

	// Drive the guard directly: it is the piece that decides on removal, and a
	// real 30s timeout would make the test unusable.
	h := &Handler{repo: repo}
	h.maybeAutoDisable("nvidia/slow-but-alive", modelTestResult{
		ModelID: "slow-but-alive", OK: false, TimedOut: true,
	}, true)

	hidden, err := repo.GetAllHiddenModels()
	if err != nil {
		t.Fatalf("GetAllHiddenModels: %v", err)
	}
	if hidden["nvidia|slow-but-alive"] {
		t.Errorf("a timed-out model was disabled; timeouts mean slow, not dead")
	}

	// The same call with a real failure must still disable it.
	h.maybeAutoDisable("nvidia/slow-but-alive", modelTestResult{
		ModelID: "slow-but-alive", OK: false, Status: 404,
	}, true)
	hidden, err = repo.GetAllHiddenModels()
	if err != nil {
		t.Fatalf("GetAllHiddenModels: %v", err)
	}
	if !hidden["nvidia|slow-but-alive"] {
		t.Errorf("a model that answered 404 was not disabled")
	}
}
