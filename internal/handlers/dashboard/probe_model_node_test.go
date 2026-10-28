package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
)

// A custom OpenAI-compatible node: the test button must ask for the model the
// operator pinned, not the first model in the cache. Nodes whose upstream lists
// a paid model first (or whose only free model changes weekly) otherwise report
// every healthy key as broken.
func TestCustomNodeProbeUsesPinnedModel(t *testing.T) {
	var gotModel atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// No catalogue endpoint: the probe falls through to a chat call.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel.Store(body.Model)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	t.Cleanup(srv.Close)

	h, database := newProfileTestHandler(t)
	repo := db.NewRepo(database)

	const nodeID = "openai-compatible-chat-pin1"
	nodeData := fmt.Sprintf(`{"prefix":"pin1","apiType":"chat","baseUrl":%q,"nodeName":"Pinned"}`, srv.URL+"/v1")
	if _, err := repo.DB().Exec(
		`INSERT INTO providerNodes (id, name, type, data, createdAt, updatedAt)
		 VALUES (?, 'Pinned', 'openai-compatible', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID, nodeData,
	); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	// A cached model that would otherwise be probed first.
	if err := repo.AddCachedModelWithLimits(nodeID, "expensive-model", "llm", "imported", "", 0, 0); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}
	seedConnection(t, repo, "c-pin", nodeID, `{"apiKey":"k"}`)

	if res := h.testConnection(context.Background(), "c-pin"); !res.Valid {
		t.Fatalf("probe failed: %s / %s", res.Error, res.Warning)
	}
	if got, _ := gotModel.Load().(string); got != "expensive-model" {
		t.Fatalf("unpinned probe model = %q, want the cached model", got)
	}

	if err := repo.SetProviderStrategy(nodeID, db.ProviderStrategy{TestModel: "z-ai/glm-4.5-air:free"}); err != nil {
		t.Fatalf("pin test model: %v", err)
	}
	if res := h.testConnection(context.Background(), "c-pin"); !res.Valid {
		t.Fatalf("pinned probe failed: %s / %s", res.Error, res.Warning)
	}
	if got, _ := gotModel.Load().(string); got != "z-ai/glm-4.5-air:free" {
		t.Fatalf("pinned probe model = %q, want the operator's choice", got)
	}
}
