package chat

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// Alysis Code routes through the provider prefix `alys` (see the alias table)
// and its gateway speaks plain OpenAI with a static `slk_…` bearer key. This
// drives a real chat request through the handler against a mock upstream, so a
// missing alias, a wrong auth scheme or an unstripped prefix fails the request
// here instead of shipping a dashboard card that 401s in production.
func TestHandleChatCompletions_AlysisAliasRoutesToGateway(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	const testKey = "slk_test_key_for_unit_test"
	var seenAuth, seenModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		seenModel, _ = req["model"].(string)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","model":"deepseek-flash","choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer upstream.Close()

	data, _ := json.Marshal(map[string]any{"apiKey": testKey, "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-alysis', 'alysis', 'apikey', 'Alysis E2E', 0, 1, ?, '2026-10-24T00:00:00Z', '2026-10-24T00:00:00Z')`,
		string(data)); err != nil {
		t.Fatalf("seed alysis connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))

	reqBody := `{"model":"alys/deepseek-flash","messages":[{"role":"user","content":"ping"}],"stream":false}`
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if seenAuth != "Bearer "+testKey {
		t.Errorf("upstream Authorization = %q, want %q", seenAuth, "Bearer "+testKey)
	}
	if seenModel != "deepseek-flash" {
		t.Errorf("upstream model = %q, want the `alys/` prefix stripped", seenModel)
	}
}
