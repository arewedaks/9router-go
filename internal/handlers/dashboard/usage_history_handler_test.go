package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// seedUsage writes one usageDaily rollup and matching raw history so the
// handler tests exercise both the rollup path and the history path.
func seedUsage(t *testing.T, repo *db.Repo) {
	t.Helper()
	now := time.Now()
	today := now.Format("2006-01-02")

	data := `{
		"requests": 4, "promptTokens": 400, "completionTokens": 200,
		"cachedTokens": 40, "cost": 0.40,
		"byProvider": {"openai": {"requests": 4, "promptTokens": 400, "completionTokens": 200, "cachedTokens": 40, "cost": 0.40}},
		"byModel": {"gpt-4o|openai": {"requests": 4, "promptTokens": 400, "completionTokens": 200, "cachedTokens": 40, "cost": 0.40, "rawModel": "gpt-4o", "provider": "openai"}},
		"byAccount": {"conn-1": {"requests": 4, "promptTokens": 400, "completionTokens": 200, "cachedTokens": 40, "cost": 0.40, "rawModel": "gpt-4o", "provider": "openai"}},
		"byApiKey": {"sk-t***test|gpt-4o|openai": {"requests": 4, "promptTokens": 400, "completionTokens": 200, "cachedTokens": 40, "cost": 0.40, "rawModel": "gpt-4o", "provider": "openai", "apiKey": "sk-t***test"}},
		"byEndpoint": {"/v1/chat/completions|gpt-4o|openai": {"requests": 4, "promptTokens": 400, "completionTokens": 200, "cachedTokens": 40, "cost": 0.40, "endpoint": "/v1/chat/completions", "rawModel": "gpt-4o", "provider": "openai"}}
	}`
	if err := repo.UpsertUsageDaily(today, data); err != nil {
		t.Fatalf("seed usageDaily: %v", err)
	}

	ts := now.UTC().Format(time.RFC3339)
	if err := repo.InsertUsageHistory("openai", "gpt-4o", "conn-1", "", "/v1/chat/completions",
		400, 200, 0.40, "success", 600, `{}`, `{"prompt_tokens":400,"completion_tokens":200}`); err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}
	_ = ts
}

func getJSON(t *testing.T, r http.Handler, path string, out any) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if out != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %s: %v (body=%s)", path, err, w.Body.String())
		}
	}
	return w.Code
}

func TestHandleUsageHistoryStats(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	seedUsage(t, repo)

	var stats db.UsageStats
	if code := getJSON(t, r, "/api/dashboard/usage/stats?period=7d", &stats); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	if stats.TotalRequests != 4 {
		t.Errorf("TotalRequests = %d, want 4", stats.TotalRequests)
	}
	if stats.TotalPromptTokens != 400 || stats.TotalCompletionTokens != 200 {
		t.Errorf("tokens = %d/%d, want 400/200", stats.TotalPromptTokens, stats.TotalCompletionTokens)
	}
	if len(stats.ByProvider) != 1 || len(stats.ByModel) != 1 {
		t.Errorf("byProvider=%d byModel=%d, want 1 each", len(stats.ByProvider), len(stats.ByModel))
	}
	// The response must carry the full set of maps, never null, so the renderer
	// can iterate without a guard.
	if stats.ByAccount == nil || stats.ByAPIKey == nil || stats.ByEndpoint == nil {
		t.Errorf("one of the by* maps is nil: acct=%v key=%v ep=%v", stats.ByAccount, stats.ByAPIKey, stats.ByEndpoint)
	}
}

// A malformed period must not 500; it falls back to the default window.
func TestHandleUsageHistoryStatsBadPeriodFallsBack(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	seedUsage(t, repo)
	var stats db.UsageStats
	if code := getJSON(t, r, "/api/dashboard/usage/stats?period=zzz", &stats); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if stats.TotalRequests != 4 {
		t.Errorf("TotalRequests = %d, want 4", stats.TotalRequests)
	}
}

func TestHandleUsageHistoryChartReturnsArray(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	seedUsage(t, repo)

	var points []db.UsagePoint
	if code := getJSON(t, r, "/api/dashboard/usage/chart?period=7d", &points); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(points) != 7 {
		t.Fatalf("points = %d, want 7", len(points))
	}
	if points[6].Tokens != 600 {
		t.Errorf("today bucket = %+v, want 600 tokens", points[6])
	}
}

// The chart endpoint must emit a JSON array, not an object wrapper, because the
// renderer iterates the response directly.
func TestHandleUsageHistoryChartIsBareArray(t *testing.T) {
	_, _, r := setupTestDashboard(t)
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/usage/chart?period=7d", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if len(body) == 0 || body[0] != '[' {
		t.Errorf("body must start with '['; got %q", body)
	}
}

func TestHandleUsageHistory(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	seedUsage(t, repo)

	var rows []map[string]any
	if code := getJSON(t, r, "/api/dashboard/usage/history?limit=10", &rows); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0]["model"] != "gpt-4o" {
		t.Errorf("model = %v, want gpt-4o", rows[0]["model"])
	}
	// The masked key is exposed, never the raw credential.
	if _, ok := rows[0]["apiKeyMasked"]; !ok {
		t.Error("apiKeyMasked missing from the history row")
	}
}

func TestHandleRequestDetails(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	for i := 0; i < 25; i++ {
		id := "req-" + string(rune('a'+i))
		payload := `{"id":"` + id + `","model":"gpt-4o","provider":"openai"}`
		if err := repo.InsertRequestDetail(id, "openai", "gpt-4o", "conn-1", "success", payload); err != nil {
			t.Fatalf("seed detail: %v", err)
		}
	}

	var page db.RequestDetailPage
	if code := getJSON(t, r, "/api/dashboard/usage/request-details?page=1&pageSize=10", &page); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if page.Pagination.TotalItems != 25 || page.Pagination.TotalPages != 3 {
		t.Errorf("pagination = %+v, want 25/3", page.Pagination)
	}
	if len(page.Details) != 10 {
		t.Errorf("details = %d, want 10", len(page.Details))
	}
}

func TestHandleRequestDetailsRejectsOversizePageSize(t *testing.T) {
	_, _, r := setupTestDashboard(t)
	if code := getJSON(t, r, "/api/dashboard/usage/request-details?pageSize=500", nil); code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestHandleRequestDetailByID(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	err := repo.InsertRequestDetail("abc", "openai", "gpt-4o", "conn-1", "success",
		`{"id":"abc","model":"gpt-4o","provider":"openai"}`)
	if err != nil {
		t.Fatalf("seed detail: %v", err)
	}

	var resp struct {
		Detail map[string]any `json:"detail"`
	}
	if code := getJSON(t, r, "/api/dashboard/usage/request-details/abc", &resp); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.Detail == nil || resp.Detail["model"] != "gpt-4o" {
		t.Errorf("detail = %v, want model gpt-4o", resp.Detail)
	}

	if code := getJSON(t, r, "/api/dashboard/usage/request-details/missing", nil); code != http.StatusNotFound {
		t.Errorf("missing id status = %d, want 404", code)
	}
}

func TestHandleUsageFilterOptions(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	err := repo.InsertRequestDetail("abc", "openai", "gpt-4o", "conn-1", "success",
		`{"id":"abc","model":"gpt-4o","provider":"openai"}`)
	if err != nil {
		t.Fatalf("seed detail: %v", err)
	}

	var resp struct {
		Providers []struct {
			Value string `json:"value"`
			Label string `json:"label"`
		} `json:"providers"`
		Models []string `json:"models"`
	}
	if code := getJSON(t, r, "/api/dashboard/usage/filters", &resp); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(resp.Providers) != 1 || resp.Providers[0].Value != "openai" {
		t.Errorf("providers = %+v, want one openai option", resp.Providers)
	}
	if len(resp.Models) != 1 || resp.Models[0] != "gpt-4o" {
		t.Errorf("models = %v, want [gpt-4o]", resp.Models)
	}
}

// Every usage endpoint must answer with an empty-but-valid payload on a fresh
// database rather than a null body or a 500.
func TestUsageEndpointsOnEmptyDatabase(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	t.Run("stats", func(t *testing.T) {
		var stats db.UsageStats
		if code := getJSON(t, r, "/api/dashboard/usage/stats?period=7d", &stats); code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
		if stats.ByProvider == nil {
			t.Error("ByProvider is nil; renderer would crash iterating null")
		}
	})

	t.Run("chart", func(t *testing.T) {
		var points []db.UsagePoint
		if code := getJSON(t, r, "/api/dashboard/usage/chart?period=7d", &points); code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
		if len(points) != 7 {
			t.Errorf("points = %d, want 7 empty buckets", len(points))
		}
	})

	t.Run("history", func(t *testing.T) {
		var rows []map[string]any
		if code := getJSON(t, r, "/api/dashboard/usage/history", &rows); code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
		if len(rows) != 0 {
			t.Errorf("rows = %d, want 0", len(rows))
		}
	})

	t.Run("request-details", func(t *testing.T) {
		var page db.RequestDetailPage
		if code := getJSON(t, r, "/api/dashboard/usage/request-details", &page); code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
		if page.Details == nil || len(page.Details) != 0 {
			t.Errorf("details = %v, want empty non-nil", page.Details)
		}
	})

	t.Run("filters", func(t *testing.T) {
		var resp struct {
			Providers []any `json:"providers"`
		}
		if code := getJSON(t, r, "/api/dashboard/usage/filters", &resp); code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
		if resp.Providers == nil {
			t.Error("providers is null; the dropdown would break")
		}
	})
}
