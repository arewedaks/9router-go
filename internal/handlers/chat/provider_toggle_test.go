package chat

import (
	json "encoding/json/v2"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// The providers page offers a per-provider switch that disables every account
// at once. That control is only honest if the router then stops offering the
// provider's models — otherwise the switch moves, the card dims, and requests
// keep landing on an upstream the operator just turned off.
//
// This drives the real model-list endpoint, because that is what both the
// dashboard and a client's /v1/models call read.
func TestDisabledProviderDisappearsFromModelList(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	repo := db.NewRepo(database)
	h := NewChatHandler(repo, nil)

	// A custom endpoint is the case the switch is for: a compatible provider
	// the operator added by hand and can pause without deleting its key.
	conn := &models.ProviderConnection{
		ID:        "wb-conn-0",
		Provider:  "workbuddy",
		AuthType:  "apikey",
		Name:      ptr("main"),
		Priority:  ptr(1),
		IsActive:  1,
		Data:      `{"apiKey":"test"}`,
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-01T00:00:00Z",
	}
	if err := repo.UpsertProviderConnection(conn); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	listModels := func() []string {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		w := httptest.NewRecorder()
		h.HandleModels(w, req)
		if w.Code != 200 {
			t.Fatalf("HandleModels status = %d, body %s", w.Code, w.Body.String())
		}
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode model list: %v", err)
		}
		ids := make([]string, 0, len(body.Data))
		for _, m := range body.Data {
			ids = append(ids, m.ID)
		}
		return ids
	}

	countProvider := func() int {
		n := 0
		for _, id := range listModels() {
			if strings.Contains(id, "wb/") || strings.Contains(id, "workbuddy/") {
				n++
			}
		}
		return n
	}

	// Sanity: the request path is exercised regardless of whether this
	// particular seeded provider contributes registry models.
	before := len(listModels())

	// Same call the UI makes: the bulk endpoint, not a per-row flip.
	updated, err := repo.SetProviderActive("workbuddy", 0)
	if err != nil {
		t.Fatalf("SetProviderActive(0): %v", err)
	}
	if updated != 1 {
		t.Fatalf("SetProviderActive disabled %d connections, want 1", updated)
	}

	if n := countProvider(); n != 0 {
		t.Errorf("a disabled provider still offers %d models: %v", n, listModels())
	}
	if after := len(listModels()); after > before {
		t.Errorf("model list grew after disabling a provider: %d -> %d", before, after)
	}

	// Re-enabling must restore it: the switch is a pause, not a delete.
	if _, err := repo.SetProviderActive("workbuddy", 1); err != nil {
		t.Fatalf("SetProviderActive(1): %v", err)
	}
	var active int
	if err := database.QueryRow("SELECT isActive FROM providerConnections WHERE id = ?", "wb-conn-0").Scan(&active); err != nil {
		t.Fatalf("re-read connection: %v", err)
	}
	if active != 1 {
		t.Errorf("re-enabling left isActive=%d, want 1", active)
	}
}

// The bulk switch must move every account of the provider, not just one. A
// partially applied toggle is the failure that makes the card switch untrust-
// worthy: the UI would report the provider off while an account kept serving.
func TestProviderToggleAllMovesEveryAccount(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	repo := db.NewRepo(database)

	for _, id := range []string{"wb-1", "wb-2", "wb-3"} {
		conn := &models.ProviderConnection{
			ID: id, Provider: "workbuddy", AuthType: "apikey", Name: ptr(id),
			Priority: ptr(1), IsActive: 1, Data: `{"apiKey":"test"}`,
			CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
		}
		if err := repo.UpsertProviderConnection(conn); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// A different provider must be untouched by the bulk call.
	other := &models.ProviderConnection{
		ID: "cl-1", Provider: "cline", AuthType: "apikey", Name: ptr("solo"),
		Priority: ptr(1), IsActive: 1, Data: `{"apiKey":"test"}`,
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}
	if err := repo.UpsertProviderConnection(other); err != nil {
		t.Fatalf("seed other: %v", err)
	}

	n, err := repo.SetProviderActive("workbuddy", 0)
	if err != nil {
		t.Fatalf("SetProviderActive: %v", err)
	}
	if n != 3 {
		t.Errorf("bulk disable updated %d connections, want 3", n)
	}

	var off, clineActive int
	if err := database.QueryRow("SELECT COUNT(*) FROM providerConnections WHERE provider = 'workbuddy' AND isActive = 0").Scan(&off); err != nil {
		t.Fatalf("count off: %v", err)
	}
	if off != 3 {
		t.Errorf("%d of 3 workbuddy accounts disabled", off)
	}
	if err := database.QueryRow("SELECT isActive FROM providerConnections WHERE id = 'cl-1'").Scan(&clineActive); err != nil {
		t.Fatalf("read cline: %v", err)
	}
	if clineActive != 1 {
		t.Error("disabling one provider also disabled another provider's account")
	}
}

func ptr[T any](v T) *T { return &v }
