package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedNode inserts a compatible provider node. providerNodes has no upsert
// helper (upstream inserts directly), so the SQL matches the schema columns.
func seedNode(t *testing.T, h *Handler, id, typ, name, data string) {
	t.Helper()
	if _, err := h.repo.DB().Exec(
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		id, typ, name, data, "2026-07-18T00:00:00Z", "2026-07-18T00:00:00Z",
	); err != nil {
		t.Fatalf("seed providerNodes: %v", err)
	}
}

// TestListProvidersDoesNotLeakGeneratedNodeIDAsRegistryName pins the display
// rule for compatible endpoints: the registry has no entry for
// "openai-compatible-chat-<uuid>", so the card must fall back to the node's own
// name ("Atria AI") rather than echoing the uuid. `provider` stays the internal
// handle the UI writes back with, so only the display fields are checked.
func TestListProvidersDoesNotLeakGeneratedNodeIDAsRegistryName(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	seedNode(t, h, nodeID, "openai-compatible", "Atria AI",
		`{"prefix":"atri","apiType":"chat","baseUrl":"https://api.atria-asi.ai/v1"}`)
	seedStatusConnection(t, h, "at-1", nodeID, 1, "", nil)

	req := httptest.NewRequest("GET", "/api/dashboard/providers", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}

	var list []ProviderSummary
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 connection, got %d", len(list))
	}
	got := list[0]
	if got.RegistryName != "Atria AI" {
		t.Fatalf("registryName = %q, want \"Atria AI\"", got.RegistryName)
	}
	if got.DisplayName != "Atria AI" {
		t.Fatalf("displayName = %q, want \"Atria AI\"", got.DisplayName)
	}
	if got.RegistryName == nodeID || got.DisplayName == nodeID {
		t.Fatalf("a display field echoed the generated node id: %+v", got)
	}
}

// TestProviderDetailIdentifiesNodeByPrefix pins that the detail page shows the
// node's prefix and name, and that it can be opened by prefix as well as by the
// generated id — the model ids use the prefix, so links/users will use it.
func TestProviderDetailIdentifiesNodeByPrefix(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	seedNode(t, h, nodeID, "openai-compatible", "Atria AI",
		`{"prefix":"atri","apiType":"chat","baseUrl":"https://api.atria-asi.ai/v1"}`)
	seedStatusConnection(t, h, "at-1", nodeID, 1, "", nil)

	for _, id := range []string{nodeID, "atri"} {
		req := httptest.NewRequest("GET", "/api/dashboard/providers/"+id, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("(%s) status = %d, body %s", id, w.Code, w.Body.String())
		}
		var detail ProviderDetail
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
			t.Fatalf("(%s) decode: %v", id, err)
		}
		if detail.DisplayName != "Atria AI" {
			t.Fatalf("(%s) displayName = %q, want \"Atria AI\"", id, detail.DisplayName)
		}
		if detail.Prefix != "atri" {
			t.Fatalf("(%s) prefix = %q, want atri", id, detail.Prefix)
		}
		if detail.TotalCount != 1 {
			t.Fatalf("(%s) totalCount = %d, want 1", id, detail.TotalCount)
		}
		if detail.Provider != nodeID {
			t.Fatalf("(%s) provider = %q, want the internal node id", id, detail.Provider)
		}
		if detail.DisplayName == nodeID || detail.Prefix == nodeID {
			t.Fatalf("(%s) display leaked the node id: %+v", id, detail)
		}
	}
}

// TestStatsChipUsesNodeNameForCompatibleEndpoint is a guard against re-adding the
// uuid, now that display fields resolve names on the provider list.
func TestStatsChipUsesNodeNameForCompatibleEndpoint(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	seedNode(t, h, nodeID, "openai-compatible", "Atria AI",
		`{"prefix":"atri","apiType":"chat","baseUrl":"https://api.atria-asi.ai/v1"}`)
	seedStatusConnection(t, h, "at-1", nodeID, 1, "", nil)

	stats := getStats(t, r)
	counts, ok := stats["providerTypeCounts"].(map[string]any)
	if !ok {
		t.Fatalf("providerTypeCounts missing or wrong type: %T", stats["providerTypeCounts"])
	}
	if _, leaked := counts[nodeID]; leaked {
		t.Fatalf("stats chip keyed by generated node id: %v", counts)
	}
	if counts["Atria AI"] == nil {
		t.Fatalf("stats chip missing node name \"Atria AI\": %v", counts)
	}
}
