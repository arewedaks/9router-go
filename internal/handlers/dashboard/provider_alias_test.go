package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
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

// filterConnected drops the synthesised no-auth cards (NoConnection) from a
// /providers response, leaving only the rows backed by a real connection. Tests
// that count "the connections I seeded" use this so they stay about their own
// fixture instead of being coupled to how many keyless providers the registry
// happens to declare.
func filterConnected(list []ProviderSummary) []ProviderSummary {
	out := make([]ProviderSummary, 0, len(list))
	for _, s := range list {
		if !s.NoConnection {
			out = append(out, s)
		}
	}
	return out
}

func TestListProvidersIncludesNoAuthProvidersWithoutConnections(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	// Deliberately seed nothing: a keyless provider needs no connection row, and
	// that absence is the whole bug — the grid is built from connections, so
	// OpenCode Free was invisible until this card was synthesised.
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

	byID := map[string]ProviderSummary{}
	for _, s := range list {
		byID[s.Provider] = s
	}

	oc, ok := byID["opencode"]
	if !ok {
		t.Fatal("opencode is absent from /providers with zero connections; the keyless provider must still be listed")
	}
	if oc.DisplayName != "OpenCode Free" {
		t.Errorf("displayName = %q, want %q", oc.DisplayName, "OpenCode Free")
	}
	if oc.AuthType != "none" {
		t.Errorf("authType = %q, want none", oc.AuthType)
	}
	if !oc.NoConnection {
		t.Error("opencode must be flagged noConnection so the UI does not show a 0/0 counter")
	}
	if oc.Category != "free" {
		t.Errorf("category = %q, want the registry category free", oc.Category)
	}
	if oc.ID != "opencode" {
		t.Errorf("id = %q; the detail page resolves a registry id, so the card must carry it", oc.ID)
	}

	// Every declared no-auth provider must appear, not just opencode.
	for _, meta := range providers.NoAuthProviders() {
		if _, ok := byID[meta.ID]; !ok {
			t.Errorf("no-auth provider %q is missing from /providers", meta.ID)
		}
	}

	// …but the local media/utility servers must NOT. They are no-auth too, yet
	// they cannot serve a chat request, so a provider card for them is noise.
	// Upstream draws the same line: noauth.ts lists only LLM providers, while the
	// TTS servers live in audio.ts and SearXNG in search.ts.
	for _, id := range []string{"coqui", "edge-tts", "google-tts", "local-device", "searxng", "tortoise"} {
		if _, ok := byID[id]; ok {
			t.Errorf("media/utility provider %q must not be promoted onto the providers grid; it cannot serve chat", id)
		}
		if _, ok := byID[providers.ResolveAlias(id)]; ok {
			t.Errorf("media/utility provider %q leaked in via its alias", id)
		}
	}

	// A provider that DOES have a connection must not gain a duplicate card.
	h2, repo2, r2 := setupTestDashboard(t)
	_ = h2
	if err := repo2.UpsertProviderConnection(&models.ProviderConnection{
		ID: "oc-1", Provider: "opencode", AuthType: "none", IsActive: 1,
		Data: `{"apiKey":"public"}`,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req2 := httptest.NewRequest("GET", "/api/dashboard/providers", nil)
	w2 := httptest.NewRecorder()
	r2.ServeHTTP(w2, req2)
	var list2 []ProviderSummary
	if err := json.Unmarshal(w2.Body.Bytes(), &list2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	n := 0
	for _, s := range list2 {
		if s.Provider == "opencode" {
			n++
			if s.NoConnection {
				t.Error("a provider with a real connection must not be marked noConnection")
			}
		}
	}
	if n != 1 {
		t.Errorf("opencode appears %d times, want exactly 1 (no synthetic duplicate)", n)
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
	// Ignore the synthesised no-auth cards; this test is about the node's card.
	list = filterConnected(list)
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

// TestProviderDetailNoAuthReportsNoConnection pins that the detail payload tells
// the UI a keyless provider needs no account. The hero renders
// "<active>/<total> active" from these counts, so without the flag OpenCode Free
// would advertise "0/0 active" — indistinguishable from a broken provider.
func TestProviderDetailNoAuthReportsNoConnection(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	for _, id := range []string{"opencode", "oc"} {
		req := httptest.NewRequest("GET", "/api/dashboard/providers/"+id, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /providers/%s status = %d", id, w.Code)
		}
		var d ProviderDetail
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !d.NoConnection {
			t.Errorf("%s: noConnection = false; the hero would read 0/0 active", id)
		}
		if d.AuthType != "none" {
			t.Errorf("%s: authType = %q, want none", id, d.AuthType)
		}
		if d.DisplayName != "OpenCode Free" {
			t.Errorf("%s: displayName = %q", id, d.DisplayName)
		}
	}

	// A provider that does need a credential must not claim otherwise.
	req := httptest.NewRequest("GET", "/api/dashboard/providers/opencode-go", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var goDetail ProviderDetail
	if err := json.Unmarshal(w.Body.Bytes(), &goDetail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if goDetail.NoConnection {
		t.Error("opencode-go requires an API key; it must not be marked noConnection")
	}
}
