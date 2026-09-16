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

	// Update settings
	updatePayload := `{"rtkEnabled":true,"cavemanEnabled":true,"cavemanLevel":"medium"}`
	req = httptest.NewRequest("POST", "/api/dashboard/settings", bytes.NewBufferString(updatePayload))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update settings failed: %d: %s", w.Code, w.Body.String())
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
