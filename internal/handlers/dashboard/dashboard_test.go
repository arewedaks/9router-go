package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

func setupTestDashboard(t *testing.T) (*Handler, *db.Repo, chi.Router) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite")
	database, err := db.OpenDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := dbtest.CreateTables(database); err != nil {
		t.Fatalf("failed to create tables: %v", err)
	}

	repo := db.NewRepo(database)
	ts := shared.NewTokenSaverConfig(true, false, false)
	h := NewHandler(repo, ts)
	r := chi.NewRouter()
	// Auth routes are mounted unprotected in production (router.go); mirror that
	// so tests exercising /api/dashboard/auth/* see the same paths.
	h.RegisterAuthRoutes(r)
	// Provider logos are mounted publicly in router.go, OUTSIDE the session
	// group, because an <img> tag cannot send an Authorization header. Mount it
	// here too so tests exercise the real path rather than a session-gated one.
	r.Get("/provider-logos/{file}", h.ServeProviderLogo)
	h.RegisterRoutes(r)
	return h, repo, r
}

func TestServeUI(t *testing.T) {
	_, _, r := setupTestDashboard(t)
	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !bytes.Contains([]byte(body), []byte("9Router")) {
		t.Fatalf("dashboard UI does not contain '9Router'")
	}
}

func TestDashboardStatsAndAuth(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// Create test API key
	key, err := repo.CreateApiKeyWithDetails("key-1", "sk-test-secret-123", "test-client")
	if err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	// Verify Auth success
	authBody := `{"key":"sk-test-secret-123"}`
	req := httptest.NewRequest("POST", "/api/dashboard/auth/verify", bytes.NewBufferString(authBody))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid key, got %d: %s", w.Code, w.Body.String())
	}

	// Verify Auth failure
	badBody := `{"key":"sk-invalid"}`
	req = httptest.NewRequest("POST", "/api/dashboard/auth/verify", bytes.NewBufferString(badBody))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid key, got %d", w.Code)
	}

	// Test Stats endpoint
	req = httptest.NewRequest("GET", "/api/dashboard/stats", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for stats, got %d: %s", w.Code, w.Body.String())
	}

	var stats map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatalf("failed to unmarshal stats: %v", err)
	}
	if stats["totalKeys"] == nil {
		t.Fatalf("missing totalKeys in stats")
	}
	_ = key
}

func TestDashboardProviderCRUD(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// Create provider
	pPayload := `{"provider":"openrouter","name":"Test OR","apiKey":"sk-or-test","priority":1}`
	req := httptest.NewRequest("POST", "/api/dashboard/providers", bytes.NewBufferString(pPayload))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create provider failed: %d: %s", w.Code, w.Body.String())
	}

	var created models.ProviderConnection
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created provider: %v", err)
	}
	if created.Provider != "openrouter" {
		t.Fatalf("expected openrouter, got %s", created.Provider)
	}

	// List providers
	req = httptest.NewRequest("GET", "/api/dashboard/providers", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list providers failed: %d", w.Code)
	}

	// Toggle provider
	req = httptest.NewRequest("POST", "/api/dashboard/providers/"+created.ID+"/toggle", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("toggle provider failed: %d", w.Code)
	}

	// Delete provider
	req = httptest.NewRequest("DELETE", "/api/dashboard/providers/"+created.ID, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete provider failed: %d", w.Code)
	}

	_ = repo
}

func TestDashboardCombosAndSettings(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	// Create combo
	cPayload := `{"name":"test-combo","models":["gemini-3.7-flash","claude-fable"],"strategy":"fallback"}`
	req := httptest.NewRequest("POST", "/api/dashboard/combos", bytes.NewBufferString(cPayload))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create combo failed: %d: %s", w.Code, w.Body.String())
	}

	// List combos
	req = httptest.NewRequest("GET", "/api/dashboard/combos", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list combos failed: %d", w.Code)
	}

	// Get settings
	req = httptest.NewRequest("GET", "/api/dashboard/settings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get settings failed: %d", w.Code)
	}

	// Update settings. The level must be one the prompt lookup understands:
	// an unknown value is now rejected rather than silently stored.
	updatePayload := `{"rtkEnabled":true,"cavemanEnabled":true,"cavemanLevel":"full"}`
	req = httptest.NewRequest("POST", "/api/dashboard/settings", bytes.NewBufferString(updatePayload))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update settings failed: %d: %s", w.Code, w.Body.String())
	}
}

func TestDashboardComboStrategyAndValidation(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	post := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/dashboard/combos", bytes.NewBufferString(payload))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// A non-fallback strategy has to survive the write/read cycle: the list
	// endpoint used to omit it entirely, which made the dashboard show
	// "fallback" for every combo no matter what was saved.
	if w := post(`{"name":"combo-fusion","models":["a/b"],"strategy":"fusion"}`); w.Code != http.StatusOK {
		t.Fatalf("create failed: %d: %s", w.Code, w.Body.String())
	}

	req := httptest.NewRequest("GET", "/api/dashboard/combos", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list failed: %d", w.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 combo, got %d: %s", len(list), w.Body.String())
	}
	if list[0]["strategy"] != "fusion" {
		t.Fatalf("list dropped strategy: %v", list[0])
	}

	// Names become client-facing model IDs, so reject path-unsafe ones.
	if w := post(`{"name":"bad name!","models":["a/b"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid name, got %d", w.Code)
	}
	// An unknown strategy would silently behave like fallback.
	if w := post(`{"name":"combo-bad","models":["a/b"],"strategy":"nonsense"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown strategy, got %d", w.Code)
	}
	// Duplicate names collide on the UNIQUE constraint; catch it up front.
	if w := post(`{"name":"combo-fusion","models":["c/d"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate name, got %d", w.Code)
	}
}

func TestDashboardProviderCategorizationAndCatalog(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// Seed providers across categories
	err := repo.UpsertProviderConnection(&models.ProviderConnection{
		ID:       "p-ag",
		Provider: "antigravity",
		AuthType: "oauth",
		IsActive: 1,
	})
	if err != nil {
		t.Fatalf("upsert antigravity: %v", err)
	}
	err = repo.UpsertProviderConnection(&models.ProviderConnection{
		ID:       "p-nv",
		Provider: "nvidia",
		AuthType: "apikey",
		IsActive: 1,
	})
	if err != nil {
		t.Fatalf("upsert nvidia: %v", err)
	}
	err = repo.UpsertProviderConnection(&models.ProviderConnection{
		ID:       "p-cust",
		Provider: "openai-compatible-chat-node1",
		AuthType: "apikey",
		IsActive: 1,
	})
	if err != nil {
		t.Fatalf("upsert custom: %v", err)
	}

	// 1. Check Catalog endpoint
	req := httptest.NewRequest("GET", "/api/dashboard/providers/catalog", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("catalog endpoint failed: %d", w.Code)
	}
	var catalog map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("unmarshal catalog failed: %v", err)
	}
	if catalog["oauth"] == nil || catalog["apikey"] == nil || catalog["custom"] == nil {
		t.Fatalf("missing expected categories in catalog: %v", catalog)
	}

	// 2. Check Provider Summaries with Category & DisplayName
	req = httptest.NewRequest("GET", "/api/dashboard/providers", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list providers failed: %d", w.Code)
	}
	var summaries []ProviderSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summaries); err != nil {
		t.Fatalf("unmarshal summaries failed: %v", err)
	}
	// The response also carries synthesised no-auth cards (OpenCode Free, the
	// local TTS/search servers) that own no connection row; this test is about
	// the three seeded connections, so scope to those.
	summaries = filterConnected(summaries)
	if len(summaries) != 3 {
		t.Fatalf("expected 3 summaries, got %d", len(summaries))
	}

	for _, s := range summaries {
		if s.Category == "" || s.CategoryLabel == "" || s.DisplayName == "" {
			t.Fatalf("empty category fields for provider %s: %+v", s.Provider, s)
		}
		if s.Provider == "antigravity" && s.Category != "oauth" {
			t.Fatalf("expected oauth category for antigravity, got %s", s.Category)
		}
		if s.Provider == "nvidia" && s.Category != "freeTier" {
			t.Fatalf("expected freeTier category for nvidia, got %s", s.Category)
		}
		if s.Provider == "openai-compatible-chat-node1" && s.Category != "custom" {
			t.Fatalf("expected custom category for custom node, got %s", s.Category)
		}
	}

	// 3. Check Stats category breakdown
	req = httptest.NewRequest("GET", "/api/dashboard/stats", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("stats failed: %d", w.Code)
	}
	var stats map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatalf("unmarshal stats failed: %v", err)
	}
	cats, ok := stats["categories"].(map[string]any)
	if !ok || cats == nil {
		t.Fatalf("missing categories map in stats: %v", stats)
	}
	if cats["oauth"] == nil || cats["freeTier"] == nil || cats["custom"] == nil {
		t.Fatalf("incomplete categories in stats: %v", cats)
	}
}

// TestDashboardAccountsGrouping verifies that per-provider account details are
// exposed so the UI can list which accounts are connected under a provider.
func TestDashboardAccountsGrouping(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	emails := []string{"alice@example.com", "bob@example.com"}
	for i, em := range emails {
		mail := em
		conn := &models.ProviderConnection{
			ID:       "ag-" + em,
			Provider: "antigravity",
			AuthType: "oauth",
			IsActive: 1,
			Email:    &mail,
			Data:     `{"email":"` + em + `","testStatus":"active","expiresAt":"2099-01-01T00:00:00Z"}`,
		}
		prio := i + 1
		conn.Priority = &prio
		if err := repo.UpsertProviderConnection(conn); err != nil {
			t.Fatalf("upsert account %d: %v", i, err)
		}
	}

	req := httptest.NewRequest("GET", "/api/dashboard/providers", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list providers failed: %d", w.Code)
	}

	var summaries []ProviderSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summaries); err != nil {
		t.Fatalf("unmarshal summaries failed: %v", err)
	}
	// Synthesised no-auth cards ride along; this test counts the two seeded
	// antigravity accounts (it also asserts every remaining row is antigravity).
	summaries = filterConnected(summaries)
	if len(summaries) != 2 {
		t.Fatalf("expected 2 antigravity accounts, got %d", len(summaries))
	}

	got := map[string]bool{}
	for _, s := range summaries {
		if s.Provider != "antigravity" {
			t.Fatalf("unexpected provider: %s", s.Provider)
		}
		if s.AccountLabel == "" {
			t.Fatalf("missing account label for %s", s.ID)
		}
		if s.TestStatus != "active" {
			t.Fatalf("expected active status, got %q", s.TestStatus)
		}
		if s.Expired {
			t.Fatalf("expected non-expired token for %s", s.AccountLabel)
		}
		got[s.AccountLabel] = true
	}
	for _, em := range emails {
		if !got[em] {
			t.Fatalf("account %s not surfaced in provider list", em)
		}
	}
}

// TestExtractAccountInfo verifies credential-blob parsing edge cases.
func TestExtractAccountInfo(t *testing.T) {
	// Email wins over projectId (regression: projectId was wrongly used).
	info := extractAccountInfo(`{"email":"user@x.com","projectId":"proj-1","testStatus":"active"}`)
	if info.AccountLabel != "user@x.com" {
		t.Fatalf("expected email label, got %q", info.AccountLabel)
	}

	// GitHub-style username fallback.
	info = extractAccountInfo(`{"githubEmail":"","username":"octocat"}`)
	if info.AccountLabel != "octocat" {
		t.Fatalf("expected username fallback, got %q", info.AccountLabel)
	}

	// Expired token detection.
	info = extractAccountInfo(`{"expiresAt":"2000-01-01T00:00:00Z"}`)
	if !info.Expired {
		t.Fatalf("expected expired=true for past timestamp")
	}

	// Malformed input must not panic.
	if got := extractAccountInfo("not-json"); got.AccountLabel != "" {
		t.Fatalf("expected empty info for malformed json, got %+v", got)
	}
}

// TestProviderDetailEndpoint verifies the provider sub-page payload
// (upstream /dashboard/providers/[id]) including alias resolution.
func TestProviderDetailEndpoint(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	mail := "detail@example.com"
	if err := repo.UpsertProviderConnection(&models.ProviderConnection{
		ID:       "ag-detail-1",
		Provider: "antigravity",
		AuthType: "oauth",
		IsActive: 1,
		Email:    &mail,
		Data:     `{"email":"detail@example.com","testStatus":"active"}`,
	}); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	// Canonical ID
	req := httptest.NewRequest("GET", "/api/dashboard/providers/antigravity", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("detail endpoint failed: %d", w.Code)
	}
	var d ProviderDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if d.Provider != "antigravity" {
		t.Fatalf("expected canonical provider, got %q", d.Provider)
	}
	if d.DisplayName == "" || d.Category != "oauth" {
		t.Fatalf("missing registry metadata: %+v", d)
	}
	if d.Icon == "" || d.Color == "" || d.PriorityRank == 0 {
		t.Fatalf("missing display fields: icon=%q color=%q rank=%d", d.Icon, d.Color, d.PriorityRank)
	}
	if len(d.ServiceKinds) == 0 {
		t.Fatalf("expected service kinds for antigravity")
	}
	if !d.Deprecated || d.RiskNotice == "" {
		t.Fatalf("expected deprecation risk notice for antigravity")
	}
	if d.TotalCount != 1 || d.ActiveCount != 1 {
		t.Fatalf("expected 1/1 connections, got %d/%d", d.ActiveCount, d.TotalCount)
	}
	if len(d.Connections) != 1 || d.Connections[0].AccountLabel != "detail@example.com" {
		t.Fatalf("unexpected connections: %+v", d.Connections)
	}

	// Short alias must resolve to the same canonical provider.
	req = httptest.NewRequest("GET", "/api/dashboard/providers/ag", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var aliasD ProviderDetail
	if err := json.Unmarshal(w.Body.Bytes(), &aliasD); err != nil {
		t.Fatalf("unmarshal alias detail: %v", err)
	}
	if aliasD.Provider != "antigravity" {
		t.Fatalf("alias 'ag' did not resolve to antigravity, got %q", aliasD.Provider)
	}

	// Unknown provider must still return a usable payload, not an error.
	req = httptest.NewRequest("GET", "/api/dashboard/providers/does-not-exist", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unknown provider should return 200, got %d", w.Code)
	}
}

// TestProviderToggleAll verifies the Enable All / Disable All bulk control.
func TestProviderToggleAll(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	for _, id := range []string{"nv-1", "nv-2"} {
		if err := repo.UpsertProviderConnection(&models.ProviderConnection{
			ID:       id,
			Provider: "nvidia",
			AuthType: "apikey",
			IsActive: 1,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	req := httptest.NewRequest("POST", "/api/dashboard/providers/nvidia/toggle-all",
		strings.NewReader(`{"active":0}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("toggle-all failed: %d", w.Code)
	}

	conns, err := repo.ListConnectionsByProvider("nvidia")
	if err != nil {
		t.Fatalf("list after toggle: %v", err)
	}
	for _, c := range conns {
		if c.IsActive != 0 {
			t.Fatalf("connection %s still active after bulk disable", c.ID)
		}
	}
}

// TestAddAndRemoveModel verifies the custom-model flow on the provider page.
func TestAddAndRemoveModel(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// Seed a cached model under the short alias so we can confirm new models
	// land under the same key the router reads from.
	if _, err := repo.DB().Exec(
		`INSERT INTO cachedProviderModels (providerId, modelId, kind, ownedBy, capabilities, updatedAt)
		 VALUES ('ag','ag/gemini-3-flash','llm','google','',0)`); err != nil {
		t.Fatalf("seed cached model: %v", err)
	}

	// Add a custom model via the canonical provider name.
	req := httptest.NewRequest("POST", "/api/dashboard/providers/antigravity/models",
		strings.NewReader(`{"modelId":"ag/my-custom-model","kind":"embedding"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("add model failed: %d %s", w.Code, w.Body.String())
	}

	var addResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &addResp); err != nil {
		t.Fatalf("unmarshal add response: %v", err)
	}
	// Must reuse the alias key 'ag', not the canonical id.
	if addResp["cacheKey"] != "ag" {
		t.Fatalf("expected cacheKey 'ag', got %v", addResp["cacheKey"])
	}

	models, err := repo.ListCachedModels("antigravity", "ag")
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	found := false
	for _, m := range models {
		if m.ModelID == "ag/my-custom-model" {
			found = true
			if m.Kind != "embedding" {
				t.Fatalf("expected kind embedding, got %q", m.Kind)
			}
		}
	}
	if !found {
		t.Fatalf("custom model not persisted")
	}

	// Missing model ID must be rejected.
	req = httptest.NewRequest("POST", "/api/dashboard/providers/antigravity/models",
		strings.NewReader(`{"modelId":"  "}`))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for blank model ID, got %d", w.Code)
	}

	// Remove it again.
	req = httptest.NewRequest("DELETE",
		"/api/dashboard/providers/antigravity/models?modelId="+url.QueryEscape("ag/my-custom-model"), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("remove model failed: %d", w.Code)
	}
	models, _ = repo.ListCachedModels("ag")
	for _, m := range models {
		if m.ModelID == "ag/my-custom-model" {
			t.Fatalf("model still present after removal")
		}
	}
}

// TestCredentialFromData ensures secrets are pulled from the right fields.
func TestCredentialFromData(t *testing.T) {
	tok, base := credentialFromData(`{"apiKey":"sk-abc","baseUrl":"https://x.example"}`)
	if tok != "sk-abc" || base != "https://x.example" {
		t.Fatalf("basic parse failed: tok=%q base=%q", tok, base)
	}

	tok, base = credentialFromData(`{"providerSpecificData":{"baseUrl":"https://psd.example","copilotToken":"cp-1"}}`)
	if tok != "cp-1" || base != "https://psd.example" {
		t.Fatalf("providerSpecificData parse failed: tok=%q base=%q", tok, base)
	}

	if tok, _ := credentialFromData("not-json"); tok != "" {
		t.Fatalf("malformed json should yield empty token, got %q", tok)
	}
}

// TestInferModelKind checks the identifier heuristics.
func TestInferModelKind(t *testing.T) {
	cases := map[string]string{
		"text-embedding-3-small": "embedding",
		"whisper-1":              "stt",
		"tts-1":                  "tts",
		"dall-e-3":               "image",
		"veo-3":                  "video",
		"gpt-4o":                 "llm",
	}
	for id, want := range cases {
		if got := inferModelKind(id); got != want {
			t.Fatalf("inferModelKind(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestImportModelsRequiresConnection verifies the graceful guard from upstream:
// "Add a connection to enable importing models."
func TestImportModelsRequiresConnection(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	req := httptest.NewRequest("GET", "/api/dashboard/providers/anthropic/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without a connection, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Add a connection") {
		t.Fatalf("unexpected message: %s", w.Body.String())
	}
}

// TestImportModelsUnsupportedProvider verifies providers without a listing
// endpoint report cleanly instead of erroring.
func TestImportModelsUnsupportedProvider(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	if err := repo.UpsertProviderConnection(&models.ProviderConnection{
		ID:       "zz-1",
		Provider: "some-exotic-provider",
		AuthType: "apikey",
		IsActive: 1,
		Data:     `{"apiKey":"x"}`,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/dashboard/providers/some-exotic-provider/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for unsupported provider, got %d", w.Code)
	}
	var res ModelFetchResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Supported {
		t.Fatalf("expected supported=false for unknown provider")
	}
	if !strings.Contains(res.Error, "does not support models listing") {
		t.Fatalf("unexpected error text: %q", res.Error)
	}
}

// TestKnownModelFetchers_OpenCodeTiers pins the OpenCode registry entries.
//
// They were missing, so the provider page answered "Provider opencode does not
// support models listing" for a route that returns 200 — which also meant the
// Free badge could never appear, since the badge is computed over the fetched
// catalogue. Both tiers must stay registered.
func TestKnownModelFetchers_OpenCodeTiers(t *testing.T) {
	cases := map[string]string{
		"opencode":    "https://opencode.ai/zen/v1/models",
		"opencode-go": "https://opencode.ai/zen/go/v1/models",
	}
	for id, wantURL := range cases {
		f, ok := knownModelFetchers[id]
		if !ok {
			t.Errorf("knownModelFetchers[%q] is missing; the provider page would report unsupported", id)
			continue
		}
		if f.url != wantURL {
			t.Errorf("knownModelFetchers[%q].url = %q, want %q", id, f.url, wantURL)
		}
		if !f.bearer {
			t.Errorf("knownModelFetchers[%q] must send the key as a bearer token", id)
		}
		if f.headers["x-opencode-client"] == "" {
			t.Errorf("knownModelFetchers[%q] must send x-opencode-client", id)
		}
	}
}

// TestImportModelsOpenCodeBadgesFreeModels drives the whole free-badge chain
// through the import route against a stub upstream, so it needs no network:
// a keyless OpenCode connection must produce a supported catalogue whose
// "-free" models are flagged isFree and whose paid models are not.
//
// This is the end-to-end guard for the two defects that made "OpenCode Free"
// unusable in the dashboard: the provider had no model fetcher, and the
// provider was absent from the free-model catalogue.
func TestImportModelsOpenCodeBadgesFreeModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer public" {
			t.Errorf("upstream Authorization = %q, want the keyless \"public\" token", got)
		}
		if got := r.Header.Get("x-opencode-client"); got == "" {
			t.Errorf("upstream request is missing x-opencode-client")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[
			{"id":"mimo-v2.5-free","name":"MiMo V2.5 Free"},
			{"id":"big-pickle","name":"Big Pickle"},
			{"id":"claude-opus-5","name":"Claude Opus 5"}
		]}`))
	}))
	defer srv.Close()

	old, hadOld := knownModelFetchers["opencode"]
	knownModelFetchers["opencode"] = modelFetcher{
		url:     srv.URL,
		method:  http.MethodGet,
		bearer:  true,
		headers: map[string]string{"x-opencode-client": "desktop"},
	}
	defer func() {
		if hadOld {
			knownModelFetchers["opencode"] = old
		} else {
			delete(knownModelFetchers, "opencode")
		}
	}()

	_, repo, r := setupTestDashboard(t)
	if err := repo.UpsertProviderConnection(&models.ProviderConnection{
		ID:       "oc-1",
		Provider: "opencode",
		AuthType: "none",
		IsActive: 1,
		Data:     `{"apiKey":"public"}`,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/dashboard/providers/oc/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var res ModelFetchResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !res.Supported {
		t.Fatalf("opencode must report supported=true, got error %q", res.Error)
	}

	got := map[string]bool{}
	for _, m := range res.Models {
		got[m.ID] = m.IsFree
	}
	for _, id := range []string{"mimo-v2.5-free", "big-pickle"} {
		if _, ok := got[id]; !ok {
			t.Errorf("model %q missing from the fetched catalogue", id)
			continue
		}
		if !got[id] {
			t.Errorf("model %q should be badged free", id)
		}
	}
	if free, ok := got["claude-opus-5"]; !ok {
		t.Error("paid model claude-opus-5 missing from the fetched catalogue")
	} else if free {
		t.Error("paid model claude-opus-5 was badged free")
	}
}

// TestDoModelRequestParsesUpstream lists models from a stub server, covering
// both the OpenAI {"data":[]} and Google {"models":[]} envelopes.
func TestDoModelRequestParsesUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"text-embedding-3-small"},{"name":"llama-3"}]}`))
	}))
	defer srv.Close()

	h := &Handler{}
	res, err := h.doModelRequest(
		&http.Client{Timeout: 5 * time.Second},
		"openai", srv.URL, "GET", map[string]string{}, nil,
	)
	if err != nil {
		t.Fatalf("doModelRequest: %v", err)
	}
	if len(res.Models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(res.Models))
	}
	if res.Models[1].Kind != "embedding" {
		t.Fatalf("expected embedding kind, got %q", res.Models[1].Kind)
	}
}

// TestDoModelRequestHTTPError verifies non-2xx responses surface cleanly.
func TestDoModelRequestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	h := &Handler{}
	res, err := h.doModelRequest(
		&http.Client{Timeout: 5 * time.Second},
		"openai", srv.URL, "GET", map[string]string{}, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("expected 401 status, got %d", res.Status)
	}
	if !strings.Contains(res.Error, "bad key") {
		t.Fatalf("error body not surfaced: %q", res.Error)
	}
}

// TestProviderDetail_ModelDisplayNameAndFreeBadge guards the two additions to
// each model row: the friendly upstream label (persisted in the cache) and the
// per-provider Free flag computed at read time.
func TestProviderDetail_ModelDisplayNameAndFreeBadge(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// Antigravity is a keyless free-tier provider (see providers.freeModelCatalog),
	// so a catalogued id must come back flagged free while an arbitrary one must not.
	if err := repo.AddCachedModelWithName("antigravity", "claude-sonnet-4-6", "llm", "imported", "Claude Sonnet 4.6"); err != nil {
		t.Fatalf("seed free model: %v", err)
	}
	if err := repo.AddCachedModelWithName("antigravity", "mystery-model", "llm", "imported", "Mystery"); err != nil {
		t.Fatalf("seed paid model: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/dashboard/providers/antigravity", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("detail endpoint failed: %d", w.Code)
	}
	var d ProviderDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}

	byID := map[string]db.CachedModel{}
	for _, m := range d.Models {
		byID[m.ModelID] = m
	}
	free, ok := byID["claude-sonnet-4-6"]
	if !ok {
		t.Fatalf("free model missing from detail: %+v", d.Models)
	}
	if free.DisplayName != "Claude Sonnet 4.6" {
		t.Errorf("display name not persisted/returned: %q", free.DisplayName)
	}
	if !free.IsFree {
		t.Error("catalogued free model was not flagged free")
	}
	paid, ok := byID["mystery-model"]
	if !ok {
		t.Fatalf("paid model missing from detail: %+v", d.Models)
	}
	if paid.IsFree {
		t.Error("uncatalogued model was wrongly flagged free")
	}
	if paid.DisplayName != "Mystery" {
		t.Errorf("display name not returned for paid model: %q", paid.DisplayName)
	}
}

// TestAddCachedModelWithName_DoesNotClobber guards that a re-import without a
// label never erases an already-stored display name.
func TestAddCachedModelWithName_DoesNotClobber(t *testing.T) {
	_, repo, _ := setupTestDashboard(t)

	if err := repo.AddCachedModelWithName("antigravity", "glm-test", "llm", "imported", "Nice Label"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := repo.AddCachedModel("antigravity", "glm-test", "llm", "imported"); err != nil {
		t.Fatalf("second add: %v", err)
	}
	models, err := repo.ListCachedModels("antigravity")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, m := range models {
		if m.ModelID == "glm-test" {
			if m.DisplayName != "Nice Label" {
				t.Errorf("display name was clobbered by an empty re-import: %q", m.DisplayName)
			}
			return
		}
	}
	t.Fatal("model not found after re-import")
}

// The Proxy Routing tab reads providerStrategies from GET /settings and writes
// the whole map back through POST /settings, so both directions must round-trip
// — including targetProxyPoolIds, which narrows rotation.
func TestSettingsProviderStrategiesRoundTrip(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	payload := `{"providerStrategies":{"opencode":{"proxyPoolId":"pool-1","rotateStrategy":"round-robin","targetProxyPoolIds":["pool-1","pool-2"]}}}`
	req := httptest.NewRequest("POST", "/api/dashboard/settings", bytes.NewBufferString(payload))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update settings failed: %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/dashboard/settings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get settings failed: %d", w.Code)
	}

	var got struct {
		ProviderStrategies map[string]struct {
			ProxyPoolID        string   `json:"proxyPoolId"`
			RotateStrategy     string   `json:"rotateStrategy"`
			TargetProxyPoolIds []string `json:"targetProxyPoolIds"`
		} `json:"providerStrategies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode settings: %v (body=%s)", err, w.Body.String())
	}
	strat, ok := got.ProviderStrategies["opencode"]
	if !ok {
		t.Fatalf("providerStrategies.opencode missing from response: %s", w.Body.String())
	}
	if strat.ProxyPoolID != "pool-1" {
		t.Errorf("proxyPoolId: want pool-1, got %q", strat.ProxyPoolID)
	}
	if strat.RotateStrategy != "round-robin" {
		t.Errorf("rotateStrategy: want round-robin, got %q", strat.RotateStrategy)
	}
	if len(strat.TargetProxyPoolIds) != 2 {
		t.Errorf("targetProxyPoolIds: want 2 entries, got %v", strat.TargetProxyPoolIds)
	}
}

// Clearing every field must remove the provider key entirely, so the settings
// blob does not accumulate empty override objects.
func TestSettingsProviderStrategiesPrunesEmptyEntries(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	seed := `{"providerStrategies":{"opencode":{"rotateStrategy":"random"}}}`
	req := httptest.NewRequest("POST", "/api/dashboard/settings", bytes.NewBufferString(seed))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("seed settings failed: %d: %s", w.Code, w.Body.String())
	}

	clear := `{"providerStrategies":{"opencode":{}}}`
	req = httptest.NewRequest("POST", "/api/dashboard/settings", bytes.NewBufferString(clear))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear settings failed: %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/dashboard/settings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var got struct {
		ProviderStrategies map[string]any `json:"providerStrategies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if _, present := got.ProviderStrategies["opencode"]; present {
		t.Errorf("an all-empty strategy should be pruned, got %s", w.Body.String())
	}
}

// providerStrategies must always serialize as an object, never null, so the UI
// can index into it without a guard.
func TestSettingsProviderStrategiesDefaultsToObject(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	req := httptest.NewRequest("GET", "/api/dashboard/settings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get settings failed: %d", w.Code)
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	val, ok := raw["providerStrategies"]
	if !ok {
		t.Fatalf("providerStrategies missing from settings payload: %s", w.Body.String())
	}
	if val == nil {
		t.Errorf("providerStrategies must be an object, got null")
	}
	if _, isObj := val.(map[string]any); !isObj {
		t.Errorf("providerStrategies must decode to an object, got %T", val)
	}
}

// The proxy pools list is read-only and must never leak proxy URLs, which can
// embed credentials.
func TestListProxyPoolsEndpointHidesURLs(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	if _, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "relay",
		ProxyURL: "http://user:secret@relay.example.com:8080",
		Type:     "http",
	}); err != nil {
		t.Fatalf("insert pool: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/dashboard/proxy-pools", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list proxy pools failed: %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "secret") {
		t.Errorf("proxy URL credentials leaked to the client: %s", body)
	}
	if !strings.Contains(body, "\"proxyPools\"") {
		t.Errorf("expected a proxyPools key, got %s", body)
	}
}

// Writing one provider's routing must not wipe every other provider's. The
// settings blob is shared with the Next.js dashboard and with scripts that patch
// a single provider, so providerStrategies merges per key rather than replacing
// the whole map.
func TestSettingsProviderStrategiesMergePreservesOtherProviders(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	seed := `{"providerStrategies":{"antigravity":{"rotateStrategy":"round-robin"},"freebuff":{"stickyLimit":2}}}`
	req := httptest.NewRequest("POST", "/api/dashboard/settings", bytes.NewBufferString(seed))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("seed failed: %d: %s", w.Code, w.Body.String())
	}

	// A caller that only knows about "opencode" writes just that provider.
	patch := `{"providerStrategies":{"opencode":{"rotateStrategy":"random"}}}`
	req = httptest.NewRequest("POST", "/api/dashboard/settings", bytes.NewBufferString(patch))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("patch failed: %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/dashboard/settings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var got struct {
		ProviderStrategies map[string]struct {
			RotateStrategy string `json:"rotateStrategy"`
			StickyLimit    int    `json:"stickyLimit"`
		} `json:"providerStrategies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ProviderStrategies["antigravity"].RotateStrategy != "round-robin" {
		t.Errorf("antigravity strategy was wiped by an unrelated write: %s", w.Body.String())
	}
	if got.ProviderStrategies["freebuff"].StickyLimit != 2 {
		t.Errorf("freebuff strategy was wiped by an unrelated write: %s", w.Body.String())
	}
	if got.ProviderStrategies["opencode"].RotateStrategy != "random" {
		t.Errorf("opencode strategy was not written: %s", w.Body.String())
	}
}

// Clearing one provider must remove only that key, leaving the others intact.
func TestSettingsProviderStrategiesClearTargetsOneProvider(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	seed := `{"providerStrategies":{"antigravity":{"rotateStrategy":"round-robin"},"opencode":{"rotateStrategy":"random"}}}`
	req := httptest.NewRequest("POST", "/api/dashboard/settings", bytes.NewBufferString(seed))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("seed failed: %d", w.Code)
	}

	clear := `{"providerStrategies":{"opencode":{"proxyPoolId":"__none__"}}}`
	req = httptest.NewRequest("POST", "/api/dashboard/settings", bytes.NewBufferString(clear))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear failed: %d", w.Code)
	}

	req = httptest.NewRequest("GET", "/api/dashboard/settings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var got struct {
		ProviderStrategies map[string]any `json:"providerStrategies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := got.ProviderStrategies["opencode"]; present {
		t.Errorf("__none__ should clear only opencode, got %s", w.Body.String())
	}
	if _, present := got.ProviderStrategies["antigravity"]; !present {
		t.Errorf("clearing opencode must not remove antigravity: %s", w.Body.String())
	}
}

// The settings blob is shared with VansRouter, which stores provider strategy
// keys this codebase does not model (stickyRoundRobinLimit, fallbackStrategy,
// strictModelAssignment). A read/write round-trip must preserve them verbatim;
// dropping them destroys the operator's real configuration.
func TestSettingsProviderStrategiesPreserveForeignKeys(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	// Shape taken from a real production settings row.
	seed := `{"providerStrategies":{` +
		`"antigravity":{"stickyRoundRobinLimit":1,"fallbackStrategy":"round-robin"},` +
		`"clinepass":{"strictModelAssignment":false}}}`
	req := httptest.NewRequest("POST", "/api/dashboard/settings", bytes.NewBufferString(seed))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("seed failed: %d: %s", w.Code, w.Body.String())
	}

	// A later write for an unrelated provider must not touch the foreign keys.
	patch := `{"providerStrategies":{"opencode":{"rotateStrategy":"random"}}}`
	req = httptest.NewRequest("POST", "/api/dashboard/settings", bytes.NewBufferString(patch))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("patch failed: %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/dashboard/settings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	body := w.Body.String()

	var got struct {
		ProviderStrategies map[string]map[string]any `json:"providerStrategies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	ag, ok := got.ProviderStrategies["antigravity"]
	if !ok {
		t.Fatalf("antigravity strategy was dropped entirely: %s", body)
	}
	if ag["fallbackStrategy"] != "round-robin" {
		t.Errorf("fallbackStrategy lost: %v", ag)
	}
	if ag["stickyRoundRobinLimit"] != float64(1) {
		t.Errorf("stickyRoundRobinLimit lost: %v", ag)
	}
	cp, ok := got.ProviderStrategies["clinepass"]
	if !ok {
		t.Fatalf("clinepass strategy was dropped entirely: %s", body)
	}
	if cp["strictModelAssignment"] != false {
		t.Errorf("strictModelAssignment lost: %v", cp)
	}
	// The newly written provider must still be present.
	if got.ProviderStrategies["opencode"]["rotateStrategy"] != "random" {
		t.Errorf("opencode write lost: %s", body)
	}
}

// A provider whose strategy object holds only foreign keys must not be treated
// as an empty entry and pruned.
func TestProviderStrategyIsEmptyCountsForeignKeys(t *testing.T) {
	empty := db.ProviderStrategy{}
	if !empty.IsEmpty() {
		t.Error("a zero ProviderStrategy must be empty")
	}
	foreign := db.ProviderStrategy{Extra: map[string]any{"fallbackStrategy": "round-robin"}}
	if foreign.IsEmpty() {
		t.Error("a strategy holding only passthrough keys must not be considered empty")
	}
	typed := db.ProviderStrategy{RotateStrategy: "random"}
	if typed.IsEmpty() {
		t.Error("a strategy with rotateStrategy must not be empty")
	}
}

// A keyless provider owns no connection row, so gating model import on "do we
// have a connection?" made a provider the operator can chat with look broken —
// the exact "Add a connection to enable importing models." dead end. It must
// import using its registry default key instead.
func TestImportModelsNoAuthProviderWithoutConnection(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// Registry truth: opencode is the keyless provider.
	if !providers.IsNoAuthProvider("opencode") {
		t.Fatal("opencode must be recognised as a no-auth provider")
	}
	// And it genuinely has no connection in this fixture.
	conns, err := repo.ListConnectionsByProvider("opencode")
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if len(conns) != 0 {
		t.Fatalf("fixture must have zero opencode connections, got %d", len(conns))
	}

	req := httptest.NewRequest("GET", "/api/dashboard/providers/opencode/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if strings.Contains(body, "Add a connection") {
		t.Fatalf("a keyless provider must not be told to add a connection: %s", body)
	}
	// The upstream call may fail offline, but it must never be refused for
	// lacking a connection — that refusal is the bug this test pins.
	if w.Code == http.StatusBadRequest {
		t.Fatalf("keyless import must not be refused with 400: %s", body)
	}
}

// The gate must stay shut for providers that really do need a credential.
func TestImportModelsCredentialProviderStillNeedsConnection(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	for _, id := range []string{"mistral", "groq", "anthropic"} {
		if providers.IsNoAuthProvider(id) {
			t.Fatalf("%s must not be treated as keyless", id)
		}
		conns, err := repo.ListConnectionsByProvider(id)
		if err != nil {
			t.Fatalf("list %s: %v", id, err)
		}
		if len(conns) != 0 {
			continue // fixture has one; the refuse-path cannot be exercised
		}

		req := httptest.NewRequest("GET", "/api/dashboard/providers/"+id+"/models", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400 without a connection, got %d: %s", id, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "Add a connection") {
			t.Errorf("%s: expected the add-a-connection message, got %s", id, w.Body.String())
		}
	}
}

// The alias must resolve too: the UI addresses providers by either id or alias.
func TestIsNoAuthProviderResolvesAlias(t *testing.T) {
	if !providers.IsNoAuthProvider("opencode") {
		t.Error("opencode (id) must be keyless")
	}
	if !providers.IsNoAuthProvider("oc") {
		t.Error("oc (alias) must resolve to the keyless opencode")
	}
	if providers.IsNoAuthProvider("opencode-go") {
		t.Error("opencode-go is a paid tier and must not be keyless")
	}
	if providers.IsNoAuthProvider("anthropic") {
		t.Error("anthropic must not be keyless")
	}
}

// A keyless provider's fetcher must fall back to the registry default key, or
// the fetch fails even though the endpoint is reachable without credentials.
func TestModelFetcherUsesDefaultAPIKeyForKeylessProvider(t *testing.T) {
	f, ok := knownModelFetchers["opencode"]
	if !ok {
		t.Fatal("opencode must have a model fetcher registered")
	}
	if !f.bearer {
		t.Fatal("opencode's fetcher must send a bearer token")
	}
	cfg, ok := providers.KnownProviders["opencode"]
	if !ok {
		t.Fatal("opencode must exist in KnownProviders")
	}
	if cfg.DefaultAPIKey == "" {
		t.Fatal("opencode must expose a DefaultAPIKey for the keyless fallback")
	}
}

// The keyless fetch path must actually send the registry default key. Without
// this the /models call is refused before it ever leaves the process, so assert
// on the Authorization header the upstream receives rather than on a status.
func TestKeylessModelFetchSendsDefaultAPIKey(t *testing.T) {
	var gotAuth string
	var gotClient string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		gotClient = req.Header.Get("x-opencode-client")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1","name":"M1"},{"id":"m2","name":"M2"}]}`))
	}))
	defer srv.Close()

	// Point the registered opencode fetcher at the stub instead of the network.
	orig, had := knownModelFetchers["opencode"]
	f := orig
	f.url = srv.URL
	knownModelFetchers["opencode"] = f
	defer func() {
		if had {
			knownModelFetchers["opencode"] = orig
		} else {
			delete(knownModelFetchers, "opencode")
		}
	}()

	h, _, _ := setupTestDashboard(t)
	// Empty data == no stored connection, which is the whole point.
	res, err := h.fetchUpstreamModels("opencode", "", 10*time.Second)
	if err != nil {
		t.Fatalf("keyless fetch must succeed using the default key, got: %v", err)
	}
	if len(res.Models) != 2 {
		t.Fatalf("expected 2 models from the stub, got %d", len(res.Models))
	}
	cfg := providers.KnownProviders["opencode"]
	if gotAuth != "Bearer "+cfg.DefaultAPIKey {
		t.Fatalf("upstream Authorization = %q, want %q", gotAuth, "Bearer "+cfg.DefaultAPIKey)
	}
	if gotClient != "desktop" {
		t.Fatalf("opencode client header = %q, want %q", gotClient, "desktop")
	}
}

// A credential provider with no stored token must NOT silently borrow someone
// else's default key — only providers that declare one may.
func TestModelFetchWithoutCredentialFailsForNonDefaultedProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	orig, had := knownModelFetchers["openrouter"]
	f := orig
	f.url = srv.URL
	knownModelFetchers["openrouter"] = f
	defer func() {
		if had {
			knownModelFetchers["openrouter"] = orig
		} else {
			delete(knownModelFetchers, "openrouter")
		}
	}()

	h, _, _ := setupTestDashboard(t)
	if _, err := h.fetchUpstreamModels("openrouter", "", 5*time.Second); err == nil {
		t.Fatal("expected an error: openrouter declares no DefaultAPIKey")
	}
}
