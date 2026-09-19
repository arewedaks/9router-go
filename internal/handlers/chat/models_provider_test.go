package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleModels_ActiveCodexConnection(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Clear seeded connections
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}

	// Insert Codex (cx) connection
	cxData := `{"prefix":"cx","apiKey":"tok-codex-test"}`
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cx-1', 'codex', 'oauth', 'Codex Account', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, cxData)
	if err != nil {
		t.Fatalf("seed codex connection: %v", err)
	}

	repo := db.NewRepo(database)
	// Codex exposes no live catalogue, so the operator's imported ids are the
	// only source. The registry fallback that used to feed this list was
	// removed: it advertised models the account could not call.
	for _, m := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		if err := repo.AddCachedModelWithName("cx", m, "llm", "imported", ""); err != nil {
			t.Fatalf("seed cached model %s: %v", m, err)
		}
	}
	handler := NewChatHandler(repo)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	handler.HandleModels(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID           string `json:"id"`
			Object       string `json:"object"`
			OwnedBy      string `json:"owned_by"`
			Capabilities *struct {
				Vision bool `json:"vision"`
			} `json:"capabilities"`
			ContextLength int `json:"context_length"`
		} `json:"data"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Object != "list" {
		t.Errorf("expected object 'list', got %s", resp.Object)
	}

	var foundAstra, foundSol bool
	for _, m := range resp.Data {
		if m.ID == "cx/gpt-6-astra" {
			foundAstra = true
			if m.OwnedBy != "cx" {
				t.Errorf("expected owned_by 'cx', got %s", m.OwnedBy)
			}
			if m.Capabilities == nil || !m.Capabilities.Vision {
				t.Errorf("expected capabilities.vision=true for gpt-6-astra, got %+v", m.Capabilities)
			}
			if m.ContextLength <= 0 {
				t.Errorf("expected positive context_length for gpt-6-astra, got %d", m.ContextLength)
			}
		}
		if m.ID == "cx/gpt-5.6-sol" {
			foundSol = true
		}
	}

	if !foundAstra {
		t.Error("expected cx/gpt-6-astra in /v1/models for active codex connection")
	}
	if !foundSol {
		t.Error("expected cx/gpt-5.6-sol in /v1/models for active codex connection")
	}
}

// With no connections at all the registry-driven fallback must not run either:
// advertising models nobody imported is exactly the failure this guards against.
func TestHandleModels_NoConnectionsAdvertisesNoRegistryModels(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Delete all connections
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	handler.HandleModels(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if strings.Contains(body, `"cx/gpt-6-astra"`) {
		t.Errorf("registry model cx/gpt-6-astra leaked into an empty install: %s", body)
	}
}

func TestHandleModelLookup_CodexModel(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	cxData := `{"prefix":"cx"}`
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cx-1', 'codex', 'oauth', 'Codex Account', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, cxData)
	if err != nil {
		t.Fatalf("seed codex connection: %v", err)
	}

	repo := db.NewRepo(database)
	// The lookup resolves against the advertised list, so the model has to be
	// imported for the id to exist.
	if err := repo.AddCachedModelWithName("cx", "gpt-6-astra", "llm", "imported", ""); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}
	handler := NewChatHandler(repo)

	req := httptest.NewRequest("GET", "/v1/models/cx/gpt-6-astra", nil)
	w := httptest.NewRecorder()
	handler.HandleModelLookup(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if !strings.Contains(w.Body.String(), `"id":"cx/gpt-6-astra"`) {
		t.Errorf("expected lookup result with id 'cx/gpt-6-astra', got %s", w.Body.String())
	}
}
