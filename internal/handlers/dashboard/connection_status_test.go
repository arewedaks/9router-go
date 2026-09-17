package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/models"
)

// seedStatusConnection inserts a provider connection whose credential blob
// carries the given testStatus plus optional model locks, mirroring how
// upstream stores account health in providerConnections.data.
func seedStatusConnection(t *testing.T, h *Handler, id, provider string, isActive int, testStatus string, locks map[string]time.Time) {
	t.Helper()
	seedStatusConnectionWithError(t, h, id, provider, isActive, testStatus, locks, "")
}

// seedStatusConnectionWithError is seedStatusConnection plus the stored
// lastError, so tests can reproduce an account whose budget is spent.
func seedStatusConnectionWithError(t *testing.T, h *Handler, id, provider string, isActive int, testStatus string, locks map[string]time.Time, lastError string) {
	t.Helper()
	blob := map[string]any{}
	if testStatus != "" {
		blob["testStatus"] = testStatus
	}
	if lastError != "" {
		blob["lastError"] = lastError
	}
	for model, until := range locks {
		blob["modelLock_"+model] = until.UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(blob)
	if err != nil {
		t.Fatalf("marshal blob: %v", err)
	}
	name := id
	conn := &models.ProviderConnection{
		ID:       id,
		Provider: provider,
		AuthType: "apikey",
		Name:     &name,
		IsActive: isActive,
		Data:     string(raw),
	}
	if err := h.repo.UpsertProviderConnection(conn); err != nil {
		t.Fatalf("upsert connection: %v", err)
	}
}

func getStats(t *testing.T, r http.Handler) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/dashboard/stats", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("stats status = %d, body %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	return out
}

// TestStatsDoesNotCountCreditExhaustedAccountsAsActive pins the production bug:
// 33 openai-compatible keys were enabled (isActive=1) while every upstream call
// failed with "credit insufficient balance". The dashboard counted all 33 as
// active because it equated the toggle with connectivity.
func TestStatsDoesNotCountCreditExhaustedAccountsAsActive(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	// Two connections whose cooldown lapsed long ago and whose status is a
	// terminal failure must NOT be counted as active.
	seedStatusConnection(t, h, "dead-1", "openai-compatible-chat-dead", 1, "quota_exhausted", nil)
	seedStatusConnection(t, h, "dead-2", "openai-compatible-chat-dead", 1, "error", nil)
	// One genuinely healthy connection.
	seedStatusConnection(t, h, "ok-1", "openai-compatible-chat-ok", 1, "active", nil)
	// One switched off, even though its status claims success.
	seedStatusConnection(t, h, "off-1", "openai-compatible-chat-off", 0, "active", nil)

	stats := getStats(t, r)
	active, _ := stats["activeProviders"].(float64)
	if int(active) != 1 {
		t.Fatalf("activeProviders = %v, want 1 (only the healthy connection). "+
			"A disabled or terminally-failed account must not count.", active)
	}

	cats, _ := stats["categories"].(map[string]any)
	custom, _ := cats["custom"].(map[string]any)
	if custom == nil {
		t.Fatal("stats.categories.custom missing")
	}
	if got, _ := custom["total"].(float64); int(got) != 4 {
		t.Fatalf("custom.total = %v, want 4", got)
	}
	if got, _ := custom["active"].(float64); int(got) != 1 {
		t.Fatalf("custom.active = %v, want 1: the category ticker must not claim "+
			"credit-exhausted accounts are connected.", got)
	}
}

// TestStatsCountsStaleUnavailableWithoutCooldownAsActive documents the upstream
// parity rule: an "unavailable" marker with no live cooldown is not evidence the
// account is broken, so it counts as active again.
func TestStatsCountsStaleUnavailableWithoutCooldownAsActive(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	// Cooldown expired two hours ago -> the marker is stale.
	expired := time.Now().UTC().Add(-2 * time.Hour)
	seedStatusConnection(t, h, "stale", "openai-compatible-chat-x", 1, "unavailable",
		map[string]time.Time{"some-model": expired})

	// Cooldown still running -> genuinely unavailable.
	live := time.Now().UTC().Add(30 * time.Minute)
	seedStatusConnection(t, h, "locked", "openai-compatible-chat-y", 1, "unavailable",
		map[string]time.Time{"some-model": live})

	stats := getStats(t, r)
	active, _ := stats["activeProviders"].(float64)
	if int(active) != 1 {
		t.Fatalf("activeProviders = %v, want 1: a lapsed cooldown recovers, "+
			"a live one does not.", active)
	}
}

// TestStatsDoesNotReviveCreditExhaustedAccounts reproduces the reported data
// shape exactly: 33 enabled keys whose model lock lapsed days ago but whose
// last error was "credit insufficient balance: balance=0". Upstream's timer
// rule alone would call them "active" again; the dashboard must not, because
// nothing refilled a zero balance.
func TestStatsDoesNotReviveCreditExhaustedAccounts(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const credited = `[400]: {"error":{"message":"credit insufficient balance: balance=0 required=110"}}`
	// Lock expired long ago (the real rows locked for 30s on 2026-09-13).
	stale := time.Now().UTC().Add(-96 * time.Hour)

	seedStatusConnectionWithError(t, h, "bai-1", "openai-compatible-chat-bai", 1, "unavailable",
		map[string]time.Time{"glm-5.3-flash": stale}, credited)
	seedStatusConnectionWithError(t, h, "bai-2", "openai-compatible-chat-bai", 1, "unavailable",
		map[string]time.Time{"glm-5.3-flash": stale}, credited)
	// A healthy sibling must still count.
	seedStatusConnection(t, h, "ok-1", "openai-compatible-chat-ok", 1, "active", nil)

	stats := getStats(t, r)
	cats, _ := stats["categories"].(map[string]any)
	custom, _ := cats["custom"].(map[string]any)
	if custom == nil {
		t.Fatal("stats.categories.custom missing")
	}
	if got, _ := custom["total"].(float64); int(got) != 3 {
		t.Fatalf("custom.total = %v, want 3", got)
	}
	if got, _ := custom["active"].(float64); int(got) != 1 {
		t.Fatalf("custom.active = %v, want 1: the lapsed lock must not revive a "+
			"credit-exhausted account, and the healthy sibling must still count.", got)
	}

	// And the per-row status must name the real problem.
	req := httptest.NewRequest("GET", "/api/dashboard/providers", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	for _, row := range rows {
		if row["id"] == "bai-1" {
			if got := row["effectiveStatus"]; got != "quota_exhausted" {
				t.Fatalf("bai-1 effectiveStatus = %v, want quota_exhausted", got)
			}
			if got := row["isEffectivelyActive"]; got != false {
				t.Fatalf("bai-1 isEffectivelyActive = %v, want false", got)
			}
		}
	}
}

// TestListProvidersReportsEffectiveStatus checks the per-connection fields the
// UI now relies on instead of guessing from isActive.
func TestListProvidersReportsEffectiveStatus(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	seedStatusConnection(t, h, "a", "openai-compatible-chat-x", 1, "active", nil)
	seedStatusConnection(t, h, "b", "openai-compatible-chat-x", 1, "quota_exhausted", nil)
	seedStatusConnection(t, h, "c", "openai-compatible-chat-x", 0, "active", nil)

	req := httptest.NewRequest("GET", "/api/dashboard/providers", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("providers status = %d", w.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	byID := map[string]map[string]any{}
	for _, row := range rows {
		id, _ := row["id"].(string)
		byID[id] = row
	}

	if got := byID["a"]["effectiveStatus"]; got != "active" {
		t.Fatalf("a.effectiveStatus = %v, want active", got)
	}
	if got := byID["a"]["isEffectivelyActive"]; got != true {
		t.Fatalf("a.isEffectivelyActive = %v, want true", got)
	}
	if got := byID["b"]["effectiveStatus"]; got != "quota_exhausted" {
		t.Fatalf("b.effectiveStatus = %v, want quota_exhausted", got)
	}
	if got := byID["b"]["isEffectivelyActive"]; got != false {
		t.Fatalf("b.isEffectivelyActive = %v, want false", got)
	}
	if got := byID["c"]["effectiveStatus"]; got != "disabled" {
		t.Fatalf("c.effectiveStatus = %v, want disabled", got)
	}
	if got := byID["c"]["isEffectivelyActive"]; got != false {
		t.Fatalf("c.isEffectivelyActive = %v, want false", got)
	}
}

// TestProviderDetailActiveCountUsesEffectiveStatus guards the detail page's
// "N of M active" badge against the same toggle-vs-health confusion.
func TestProviderDetailActiveCountUsesEffectiveStatus(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	seedStatusConnection(t, h, "one", "openai-compatible-chat-x", 1, "active", nil)
	seedStatusConnection(t, h, "two", "openai-compatible-chat-x", 1, "error", nil)
	seedStatusConnection(t, h, "three", "openai-compatible-chat-x", 1, "error", nil)

	req := httptest.NewRequest("GET", "/api/dashboard/providers/openai-compatible-chat-x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body %s", w.Code, w.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if got, _ := detail["totalCount"].(float64); int(got) != 3 {
		t.Fatalf("totalCount = %v, want 3", got)
	}
	if got, _ := detail["activeCount"].(float64); int(got) != 1 {
		t.Fatalf("activeCount = %v, want 1: failing accounts must not be counted "+
			"as active just because their toggle is on.", got)
	}
}

// TestStatsChipsUseNodeNameNotGeneratedKey pins the label fix. A custom
// OpenAI-compatible endpoint's provider key is a generated identifier
// ("openai-compatible-chat-<uuid>"); upstream renders node.name and treats the
// id as internal. The Overview chips must therefore read "BAI: 1", not the UUID.
func TestStatsChipsUseNodeNameNotGeneratedKey(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	// providerNodes has no public upsert helper, so insert it the way the node
	// API does: id/type/name plus a JSON blob holding prefix/apiType/baseUrl.
	blob := `{"prefix":"atri","apiType":"chat","baseUrl":"https://api.atria-asi.ai/v1"}`
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.repo.DB().Exec(
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		nodeID, "openai-compatible", "Atria AI", blob, now, now,
	); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	seedStatusConnection(t, h, "atri-1", nodeID, 1, "active", nil)
	seedStatusConnection(t, h, "atri-2", nodeID, 1, "active", nil)

	stats := getStats(t, r)
	counts, _ := stats["providerTypeCounts"].(map[string]any)
	if counts == nil {
		t.Fatal("stats.providerTypeCounts missing")
	}
	if _, leaked := counts[nodeID]; leaked {
		t.Fatalf("providerTypeCounts still keyed by the generated id %q: %v", nodeID, counts)
	}
	if got, _ := counts["Atria AI"].(float64); int(got) != 2 {
		t.Fatalf("providerTypeCounts[\"Atria AI\"] = %v, want 2 (got %v)", got, counts)
	}
}
