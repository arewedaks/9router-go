package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A provider node advertises its models under the operator's chosen prefix, but
// markers were written under the node's generated id. The two never match, so a
// failed model stayed in /v1/models after auto-disable removed it.
//
// Live shape: node "openai-compatible-chat-71c69b17-…" carries prefix "xkiro",
// /v1/models listed xkiro/*, and every hidden marker for it was written as
// "openai-compatible-chat-71c69b17-…|<model>" — 13 markers, none of which the
// list could match.
func TestAutoDisableMarksNodePrecededModelUnderItsPrefix(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-aaaa"
	if _, err := repo.DB().Exec(
		`INSERT INTO providerNodes (id, data, createdAt, updatedAt)
		 VALUES (?, '{"prefix":"xkiro","baseUrl":"https://example.test/v1"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID,
	); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('c-1', ?, 'apikey', 'xkiro', 1, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if _, err := repo.DB().Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
		nodeID+"|google/gemini-3.1-pro|llm",
		`{"providerAlias":"`+nodeID+`","id":"google/gemini-3.1-pro","type":"llm"}`,
	); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}

	// Remove it the way the UI does after a failed probe.
	req := httptest.NewRequest(http.MethodDelete,
		"/api/dashboard/providers/"+nodeID+"/models?modelId=google%2Fgemini-3.1-pro", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("remove status = %d (%s)", w.Code, w.Body.String())
	}

	hidden, err := repo.GetAllHiddenModels()
	if err != nil {
		t.Fatalf("GetAllHiddenModels: %v", err)
	}
	// The list renders these models as "xkiro/<id>", so that is the key the
	// marker must carry.
	if !hidden["xkiro|google/gemini-3.1-pro"] {
		t.Errorf("marker missing under the node prefix; hidden = %v", hidden)
	}
}

// The batch auto-disable path must apply the same prefix-key rule.
func TestAutoDisableBatchMarksNodeModelUnderItsPrefix(t *testing.T) {
	_, repo, _ := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-bbbb"
	if _, err := repo.DB().Exec(
		`INSERT INTO providerNodes (id, data, createdAt, updatedAt)
		 VALUES (?, '{"prefix":"xkiro","baseUrl":"https://example.test/v1"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID,
	); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('c-1', ?, 'apikey', 'xkiro', 1, 1, '{"apiKey":"k"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if _, err := repo.DB().Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
		nodeID+"|z-ai/glm-4.5-air|llm",
		`{"providerAlias":"`+nodeID+`","id":"z-ai/glm-4.5-air","type":"llm"}`,
	); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}

	// Simulate a permanent probe failure (404) — no running server needed.
	// The model is addressed as "xkiro/<id>" the way the UI sends it.
	h := &Handler{repo: repo}
	h.maybeAutoDisable("xkiro/z-ai/glm-4.5-air", modelTestResult{
		ModelID: "z-ai/glm-4.5-air", OK: false, Status: 404,
	}, true)

	hidden, err := repo.GetAllHiddenModels()
	if err != nil {
		t.Fatalf("GetAllHiddenModels: %v", err)
	}
	if !hidden["xkiro|z-ai/glm-4.5-air"] {
		t.Errorf("marker missing under the node prefix; hidden = %v", hidden)
	}
}
