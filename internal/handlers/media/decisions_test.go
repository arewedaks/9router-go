package media

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleDecisions_ForwardsToNace(t *testing.T) {
	database, cleanup := setupResponsesTestDB(t)
	defer cleanup()

	var gotPath, gotAuth, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		bodyBytes, _ := io.ReadAll(r.Body)
		var m struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(bodyBytes, &m)
		gotModel = m.Model

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"model": "drex-v2.0",
			"answers": [
				{"type":"predicate","name":"wants_refund","probability":0.98}
			],
			"usage": {"input_tokens":10,"output_tokens":20,"total_tokens":30},
			"request_id": "req_12345"
		}`))
	}))
	defer upstream.Close()

	connData, _ := json.Marshal(map[string]any{
		"apiKey":  "nace_sk_test123",
		"baseUrl": upstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-nace', 'nace', 'apikey', 'Nace Test', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(connData))
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{
		"model": "nace/drex-v2.0",
		"input": "Refund please",
		"questions": [{"type":"predicate","name":"wants_refund","instructions":"Does user want refund?"}]
	}`
	req := httptest.NewRequest("POST", "/decisions", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleDecisions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/decisions" {
		t.Errorf("upstream path = %q, want /decisions", gotPath)
	}
	if gotAuth != "Bearer nace_sk_test123" {
		t.Errorf("upstream auth = %q, want Bearer nace_sk_test123", gotAuth)
	}
	if gotModel != "drex-v2.0" {
		t.Errorf("upstream model = %q, want drex-v2.0", gotModel)
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if res["model"] != "drex-v2.0" {
		t.Errorf("model = %v, want drex-v2.0", res["model"])
	}
}

func TestHandleSystemOne_ForwardsToNace(t *testing.T) {
	database, cleanup := setupResponsesTestDB(t)
	defer cleanup()

	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"model":"drex-latest","answers":[]}`))
	}))
	defer upstream.Close()

	connData, _ := json.Marshal(map[string]any{
		"apiKey":  "nace_sk_test123",
		"baseUrl": upstream.URL,
	})
	_, _ = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-nace', 'nace', 'apikey', 'Nace Test', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(connData))

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	// Omitting model should default to drex-latest and route to nace
	body := `{"input":"testing systemone","questions":[]}`
	req := httptest.NewRequest("POST", "/systemone", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleSystemOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/systemone" {
		t.Errorf("upstream path = %q, want /systemone", gotPath)
	}
}
