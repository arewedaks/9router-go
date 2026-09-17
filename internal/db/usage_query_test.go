package db

import (
	"testing"
	"time"
)

// usageTestDB builds a database with the three usage tables and returns the
// repo plus the raw handle so tests can seed rows directly.
func usageTestDB(t *testing.T) *Repo {
	t.Helper()
	db, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS usageDaily (
			dateKey TEXT PRIMARY KEY,
			data TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS usageHistory (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT, provider TEXT, model TEXT, connectionId TEXT,
			apiKey TEXT, endpoint TEXT, promptTokens INTEGER,
			completionTokens INTEGER, cost REAL, status TEXT, tokens TEXT,
			meta TEXT, apiKeyName TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS requestDetails (
			id TEXT PRIMARY KEY, timestamp TEXT, provider TEXT, model TEXT,
			connectionId TEXT, status TEXT, data TEXT, apiKey TEXT,
			apiKeyName TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS providerNodes (
			id TEXT PRIMARY KEY, name TEXT, data TEXT
		);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	return NewRepo(db)
}

func (r *Repo) rawExec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := r.db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func dateKey(t time.Time) string { return dateKeyFor(t) }

func TestUsagePeriodDays(t *testing.T) {
	cases := []struct {
		period   string
		days     int
		useDaily bool
		ok       bool
	}{
		{"today", 1, false, true},
		{"24h", 1, false, true},
		{"7d", 7, true, true},
		{"30d", 30, true, true},
		{"60d", 60, true, true},
		{"all", 0, true, true},
		{"bogus", 0, false, false},
	}
	for _, c := range cases {
		days, useDaily, ok := UsagePeriodDays(c.period)
		if days != c.days || useDaily != c.useDaily || ok != c.ok {
			t.Errorf("UsagePeriodDays(%q) = (%d,%v,%v), want (%d,%v,%v)",
				c.period, days, useDaily, ok, c.days, c.useDaily, c.ok)
		}
	}
}

func TestPeriodCutoffTodayStartsAtLocalMidnight(t *testing.T) {
	now := time.Date(2026, 7, 18, 15, 30, 0, 0, time.Local)
	got := PeriodCutoff("today", now)
	want := time.Date(2026, 7, 18, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("today cutoff = %v, want %v", got, want)
	}
}

func TestPeriodCutoffRollingWindows(t *testing.T) {
	now := time.Date(2026, 7, 18, 15, 30, 0, 0, time.Local)

	got24 := PeriodCutoff("24h", now)
	if !got24.Equal(now.Add(-24 * time.Hour)) {
		t.Errorf("24h cutoff = %v, want %v", got24, now.Add(-24*time.Hour))
	}

	// 7d spans today plus the six preceding days, so the cutoff is midnight
	// six days before today.
	got7 := PeriodCutoff("7d", now)
	want7 := time.Date(2026, 7, 12, 0, 0, 0, 0, time.Local)
	if !got7.Equal(want7) {
		t.Errorf("7d cutoff = %v, want %v", got7, want7)
	}

	if !PeriodCutoff("all", now).IsZero() {
		t.Error("all should have no cutoff")
	}
}

func TestGetUsageStatsAggregatesDailyRollup(t *testing.T) {
	r := usageTestDB(t)
	now := time.Now()
	today := dateKey(now)
	yesterday := dateKey(now.AddDate(0, 0, -1))

	// Two days of rollups. Totals must be summed across both.
	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`, yesterday, `{
		"requests": 3, "promptTokens": 300, "completionTokens": 150, "cachedTokens": 30, "cost": 0.30,
		"byProvider": {"openai": {"requests": 3, "promptTokens": 300, "completionTokens": 150, "cachedTokens": 30, "cost": 0.30}},
		"byModel": {"gpt-4o|openai": {"requests": 3, "promptTokens": 300, "completionTokens": 150, "cachedTokens": 30, "cost": 0.30, "rawModel": "gpt-4o", "provider": "openai"}}
	}`)
	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`, today, `{
		"requests": 2, "promptTokens": 200, "completionTokens": 100, "cachedTokens": 20, "cost": 0.20,
		"byProvider": {"openai": {"requests": 2, "promptTokens": 200, "completionTokens": 100, "cachedTokens": 20, "cost": 0.20}},
		"byModel": {"gpt-4o|openai": {"requests": 2, "promptTokens": 200, "completionTokens": 100, "cachedTokens": 20, "cost": 0.20, "rawModel": "gpt-4o", "provider": "openai"}}
	}`)

	stats, err := r.GetUsageStats("7d", now)
	if err != nil {
		t.Fatalf("GetUsageStats: %v", err)
	}

	if stats.TotalRequests != 5 {
		t.Errorf("TotalRequests = %d, want 5", stats.TotalRequests)
	}
	if stats.TotalPromptTokens != 500 {
		t.Errorf("TotalPromptTokens = %d, want 500", stats.TotalPromptTokens)
	}
	if stats.TotalCompletionTokens != 250 {
		t.Errorf("TotalCompletionTokens = %d, want 250", stats.TotalCompletionTokens)
	}
	if stats.TotalCachedTokens != 50 {
		t.Errorf("TotalCachedTokens = %d, want 50", stats.TotalCachedTokens)
	}
	if stats.TotalCost < 0.499 || stats.TotalCost > 0.501 {
		t.Errorf("TotalCost = %f, want ~0.50", stats.TotalCost)
	}

	if len(stats.ByProvider) != 1 || stats.ByProvider["openai"].Requests != 5 {
		t.Errorf("ByProvider = %+v, want one openai bucket with 5 requests", stats.ByProvider)
	}

	modelKey := "gpt-4o (openai)"
	m, ok := stats.ByModel[modelKey]
	if !ok {
		t.Fatalf("ByModel missing %q; got keys %v", modelKey, sortedKeys(stats.ByModel))
	}
	if m.Requests != 5 || m.PromptTokens != 500 {
		t.Errorf("ByModel[%q] = %+v, want 5 requests / 500 prompt tokens", modelKey, m)
	}
}

func TestGetUsageStatsExcludesDaysOutsidePeriod(t *testing.T) {
	r := usageTestDB(t)
	now := time.Now()

	// One day inside the 7d window and one day long outside it.
	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		dateKey(now.AddDate(0, 0, -2)),
		`{"requests": 4, "promptTokens": 40, "byProvider": {"openai": {"requests": 4, "promptTokens": 40}}}`)
	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		dateKey(now.AddDate(0, 0, -40)),
		`{"requests": 99, "promptTokens": 990, "byProvider": {"openai": {"requests": 99, "promptTokens": 990}}}`)

	stats, err := r.GetUsageStats("7d", now)
	if err != nil {
		t.Fatalf("GetUsageStats: %v", err)
	}
	if stats.TotalRequests != 4 {
		t.Errorf("TotalRequests = %d, want 4 (the -40d row must be excluded)", stats.TotalRequests)
	}

	// "all" must include it.
	all, err := r.GetUsageStats("all", now)
	if err != nil {
		t.Fatalf("GetUsageStats(all): %v", err)
	}
	if all.TotalRequests != 103 {
		t.Errorf("all TotalRequests = %d, want 103", all.TotalRequests)
	}
}

func TestGetUsageStatsAccountsAndAPIKeys(t *testing.T) {
	r := usageTestDB(t)

	r.rawExec(t, `INSERT INTO providerConnections (id, provider, authType, name, email, priority, data, createdAt, updatedAt)
		VALUES (?, 'openai', 'oauth', 'Work Account', 'work@example.com', 1, '{}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, "conn-1")
	r.rawExec(t, `INSERT INTO apiKeys (id, key, name, isActive, createdAt)
		VALUES ('k1', 'sk-abcdefghijklmnop', 'Team Key', 1, '2026-01-01T00:00:00Z')`)

	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`, dateKey(time.Now()), `{
		"requests": 2, "promptTokens": 20,
		"byProvider": {"openai": {"requests": 2, "promptTokens": 20}},
		"byAccount": {"conn-1": {"requests": 2, "promptTokens": 20, "rawModel": "gpt-4o", "provider": "openai"}},
		"byApiKey": {"sk-a***mnop|gpt-4o|openai": {"requests": 2, "promptTokens": 20, "rawModel": "gpt-4o", "provider": "openai", "apiKey": "sk-a***mnop"}},
		"byEndpoint": {"/v1/chat/completions": {"requests": 2, "promptTokens": 20, "rawModel": "gpt-4o", "provider": "openai"}}
	}`)

	stats, err := r.GetUsageStats("7d", time.Now())
	if err != nil {
		t.Fatalf("GetUsageStats: %v", err)
	}

	acctKey := "gpt-4o (openai - Work Account)"
	a, ok := stats.ByAccount[acctKey]
	if !ok {
		t.Fatalf("ByAccount missing %q; got %v", acctKey, sortedKeys(stats.ByAccount))
	}
	if a.ConnectionID != "conn-1" || a.AccountName != "Work Account" {
		t.Errorf("account bucket = %+v, want conn-1 / Work Account", a)
	}

	keyBucket, ok := stats.ByAPIKey["sk-a***mnop"]
	if !ok {
		t.Fatalf("ByAPIKey missing masked key; got %v", sortedKeys(stats.ByAPIKey))
	}
	if keyBucket.KeyName != "sk-a***mnop" {
		t.Errorf("KeyName = %q, want the masked value when no friendly name matches", keyBucket.KeyName)
	}

	epKey := "/v1/chat/completions|gpt-4o|openai"
	if _, ok := stats.ByEndpoint[epKey]; !ok {
		t.Errorf("ByEndpoint missing %q; got %v", epKey, sortedKeys(stats.ByEndpoint))
	}
}

func TestGetUsageStatsShortPeriodReadsRawHistory(t *testing.T) {
	r := usageTestDB(t)
	now := time.Now()

	// A row two hours ago must appear in the 24h window.
	recent := now.Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	r.rawExec(t, `INSERT INTO usageHistory
		(timestamp, provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokens)
		VALUES (?, 'openai', 'gpt-4o', 'conn-1', '', '/v1/chat/completions', 100, 50, 0.10, 'success', '{"prompt_tokens":100,"completion_tokens":50}')`, recent)

	// A row three days old must NOT appear in the 24h window.
	old := now.AddDate(0, 0, -3).UTC().Format(time.RFC3339)
	r.rawExec(t, `INSERT INTO usageHistory
		(timestamp, provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokens)
		VALUES (?, 'openai', 'gpt-4o', 'conn-2', '', '/v1/chat/completions', 10, 5, 0.01, 'success', '{"prompt_tokens":10,"completion_tokens":5}')`, old)

	stats, err := r.GetUsageStats("24h", now)
	if err != nil {
		t.Fatalf("GetUsageStats(24h): %v", err)
	}
	if stats.TotalRequests != 1 {
		t.Errorf("TotalRequests = %d, want 1 (only the recent row)", stats.TotalRequests)
	}
	if stats.TotalPromptTokens != 100 || stats.TotalCompletionTokens != 50 {
		t.Errorf("tokens = %d/%d, want 100/50", stats.TotalPromptTokens, stats.TotalCompletionTokens)
	}

	b := stats.ByProvider["openai"]
	if b.Requests != 1 || b.Cost < 0.099 {
		t.Errorf("ByProvider[openai] = %+v, want 1 request / 0.10 cost", b)
	}
}

func TestGetUsageChartDailyBuckets(t *testing.T) {
	r := usageTestDB(t)
	now := time.Now()

	// Seed one point for today and one for two days ago; the 7d chart must
	// contain seven buckets in chronological order, ending today.
	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		dateKey(now), `{"promptTokens": 100, "completionTokens": 50, "cost": 0.5}`)
	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		dateKey(now.AddDate(0, 0, -2)), `{"promptTokens": 20, "completionTokens": 10, "cost": 0.2}`)

	points, err := r.GetUsageChart("7d", now)
	if err != nil {
		t.Fatalf("GetUsageChart: %v", err)
	}
	if len(points) != 7 {
		t.Fatalf("points = %d, want 7", len(points))
	}

	last := points[len(points)-1]
	if last.Tokens != 150 || last.Cost != 0.5 {
		t.Errorf("last bucket = %+v, want 150 tokens / 0.5 cost", last)
	}
	if last.Label != now.Format("Jan 2") {
		t.Errorf("last label = %q, want %q", last.Label, now.Format("Jan 2"))
	}

	thirdFromEnd := points[len(points)-3]
	if thirdFromEnd.Tokens != 30 {
		t.Errorf("bucket two days back = %+v, want 30 tokens", thirdFromEnd)
	}

	// The middle four buckets have no data and must read zero, not be omitted.
	if points[3].Tokens != 0 {
		t.Errorf("empty bucket = %+v, want zero", points[3])
	}
}

func TestGetUsageChartUnknownPeriodFallsBackTo7d(t *testing.T) {
	r := usageTestDB(t)
	now := time.Now()

	// A sample 30 days back lands inside a 60d window but outside a 7d one, so
	// the bucket count tells the two fallbacks apart.
	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		dateKey(now), `{"promptTokens": 100}`)
	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		dateKey(now.AddDate(0, 0, -30)), `{"promptTokens": 999}`)

	points, err := r.GetUsageChart("not-a-period", now)
	if err != nil {
		t.Fatalf("GetUsageChart: %v", err)
	}
	if len(points) != 7 {
		t.Fatalf("points = %d, want 7: an unknown period must fall back to 7d", len(points))
	}
	for _, p := range points {
		if p.Tokens == 999 {
			t.Errorf("the 30-day-old sample leaked into a 7d chart: %+v", p)
		}
	}
}

func TestGetUsageChartHourlyBuckets(t *testing.T) {
	r := usageTestDB(t)
	now := time.Now()

	recent := now.Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	r.rawExec(t, `INSERT INTO usageHistory
		(timestamp, provider, model, promptTokens, completionTokens, cost, status, tokens)
		VALUES (?, 'openai', 'gpt-4o', 25, 5, 0.05, 'success', '{"prompt_tokens":25,"completion_tokens":5}')`, recent)

	points, err := r.GetUsageChart("24h", now)
	if err != nil {
		t.Fatalf("GetUsageChart(24h): %v", err)
	}
	if len(points) != 24 {
		t.Fatalf("points = %d, want 24", len(points))
	}

	total := 0
	totalCost := 0.0
	for _, p := range points {
		total += p.Tokens
		totalCost += p.Cost
	}
	if total != 30 {
		t.Errorf("summed tokens = %d, want 30", total)
	}
	if totalCost < 0.049 || totalCost > 0.051 {
		t.Errorf("summed cost = %f, want ~0.05", totalCost)
	}
	// The most recent bucket is the last one.
	if points[23].Tokens != 30 {
		t.Errorf("last hourly bucket = %+v, want the 30 tokens just recorded", points[23])
	}
}

func TestRecentUsageEntriesDedupesIdenticalMinute(t *testing.T) {
	r := usageTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)

	// Three identical rows in the same minute collapse to one entry; a fourth
	// with different token counts stays separate.
	for i := 0; i < 3; i++ {
		r.rawExec(t, `INSERT INTO usageHistory
			(timestamp, provider, model, promptTokens, completionTokens, cost, status, tokens)
			VALUES (?, 'openai', 'gpt-4o', 10, 5, 0.01, 'success', '{"prompt_tokens":10,"completion_tokens":5}')`, now)
	}
	r.rawExec(t, `INSERT INTO usageHistory
		(timestamp, provider, model, promptTokens, completionTokens, cost, status, tokens)
		VALUES (?, 'openai', 'gpt-4o', 99, 88, 0.02, 'success', '{"prompt_tokens":99,"completion_tokens":88}')`, now)

	stats, err := r.GetUsageStats("24h", time.Now())
	if err != nil {
		t.Fatalf("GetUsageStats: %v", err)
	}
	if len(stats.RecentRequests) != 2 {
		t.Fatalf("recent entries = %d, want 2; got %+v", len(stats.RecentRequests), stats.RecentRequests)
	}
	for _, e := range stats.RecentRequests {
		if e.Status != "success" {
			t.Errorf("status = %q, want success", e.Status)
		}
	}
}

func TestRecentUsageEntriesSkipsZeroTokenRows(t *testing.T) {
	r := usageTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)

	r.rawExec(t, `INSERT INTO usageHistory
		(timestamp, provider, model, promptTokens, completionTokens, cost, status, tokens)
		VALUES (?, 'openai', 'gpt-4o', 0, 0, 0, 'error', '{}')`, now)
	r.rawExec(t, `INSERT INTO usageHistory
		(timestamp, provider, model, promptTokens, completionTokens, cost, status, tokens)
		VALUES (?, 'openai', 'gpt-4o', 5, 5, 0.01, 'success', '{"prompt_tokens":5,"completion_tokens":5}')`, now)

	stats, err := r.GetUsageStats("24h", time.Now())
	if err != nil {
		t.Fatalf("GetUsageStats: %v", err)
	}
	if len(stats.RecentRequests) != 1 {
		t.Fatalf("recent entries = %d, want 1 (zero-token row skipped)", len(stats.RecentRequests))
	}
}

func TestGetUsageStatsUnknownPeriodFallsBackTo7d(t *testing.T) {
	r := usageTestDB(t)
	now := time.Now()

	// Today is inside every window; the day-10-ago sample only falls inside a
	// 30d window. That difference is what distinguishes the 7d fallback from a
	// longer one.
	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		dateKey(now), `{"requests": 7, "byProvider": {"openai": {"requests": 7}}}`)
	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		dateKey(now.AddDate(0, 0, -10)), `{"requests": 100, "byProvider": {"openai": {"requests": 100}}}`)

	stats, err := r.GetUsageStats("not-a-period", now)
	if err != nil {
		t.Fatalf("GetUsageStats: %v", err)
	}
	if stats.TotalRequests != 7 {
		t.Errorf("TotalRequests = %d, want 7: an unknown period must fall back to 7d, not a longer window", stats.TotalRequests)
	}
}

func TestGetUsageStatsSkipsMalformedDay(t *testing.T) {
	r := usageTestDB(t)
	now := time.Now()

	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`, dateKey(now), `not json`)
	r.rawExec(t, `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		dateKey(now.AddDate(0, 0, -1)), `{"requests": 3, "byProvider": {"openai": {"requests": 3}}}`)

	stats, err := r.GetUsageStats("7d", now)
	if err != nil {
		t.Fatalf("a malformed day must not fail the whole request: %v", err)
	}
	if stats.TotalRequests != 3 {
		t.Errorf("TotalRequests = %d, want 3 from the valid day", stats.TotalRequests)
	}
}

func TestGetRequestDetailsPagination(t *testing.T) {
	r := usageTestDB(t)

	for i := 0; i < 25; i++ {
		id := "req-" + itoa(i)
		r.rawExec(t, `INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data)
			VALUES (?, ?, 'openai', 'gpt-4o', 'conn-1', 'success', ?)`,
			id, time.Now().Add(-time.Duration(i)*time.Minute).UTC().Format(time.RFC3339),
			`{"id":"`+id+`"}`)
	}

	page, err := r.GetRequestDetails(RequestDetailFilter{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("GetRequestDetails: %v", err)
	}
	if len(page.Details) != 10 {
		t.Errorf("page 1 details = %d, want 10", len(page.Details))
	}
	if page.Pagination.TotalItems != 25 || page.Pagination.TotalPages != 3 {
		t.Errorf("pagination = %+v, want 25 items / 3 pages", page.Pagination)
	}
	if !page.Pagination.HasNext || page.Pagination.HasPrev {
		t.Errorf("page 1 flags = %+v, want HasNext only", page.Pagination)
	}

	last, err := r.GetRequestDetails(RequestDetailFilter{Page: 3, PageSize: 10})
	if err != nil {
		t.Fatalf("GetRequestDetails page 3: %v", err)
	}
	if len(last.Details) != 5 {
		t.Errorf("page 3 details = %d, want 5", len(last.Details))
	}
	if last.Pagination.HasNext || !last.Pagination.HasPrev {
		t.Errorf("page 3 flags = %+v, want HasPrev only", last.Pagination)
	}
}

func TestGetRequestDetailsRejectsOversizePage(t *testing.T) {
	r := usageTestDB(t)
	if _, err := r.GetRequestDetails(RequestDetailFilter{PageSize: 101}); err == nil {
		t.Error("pageSize 101 must be rejected")
	}
	if _, err := r.GetRequestDetails(RequestDetailFilter{PageSize: 100}); err != nil {
		t.Errorf("pageSize 100 must be accepted: %v", err)
	}
}

func TestGetRequestDetailsFilters(t *testing.T) {
	r := usageTestDB(t)
	ts := time.Now().UTC().Format(time.RFC3339)

	r.rawExec(t, `INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data)
		VALUES ('a', ?, 'openai', 'gpt-4o', 'conn-1', 'success', '{}')`, ts)
	r.rawExec(t, `INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data)
		VALUES ('b', ?, 'anthropic', 'claude-3', 'conn-2', 'error', '{}')`, ts)

	page, err := r.GetRequestDetails(RequestDetailFilter{Provider: "anthropic", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("GetRequestDetails: %v", err)
	}
	if page.Pagination.TotalItems != 1 || len(page.Details) != 1 {
		t.Fatalf("provider filter returned %d items, want 1", page.Pagination.TotalItems)
	}

	page, err = r.GetRequestDetails(RequestDetailFilter{Status: "error", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("GetRequestDetails: %v", err)
	}
	if page.Pagination.TotalItems != 1 {
		t.Errorf("status filter returned %d items, want 1", page.Pagination.TotalItems)
	}
}

// Regression guard: an empty base must not be an error, and the returned
// pagination envelope must still carry sensible zeros so the UI can render
// "no results" instead of crashing on a nil map.
func TestGetRequestDetailsEmptyBase(t *testing.T) {
	r := usageTestDB(t)
	page, err := r.GetRequestDetails(RequestDetailFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("GetRequestDetails: %v", err)
	}
	if page.Details == nil || len(page.Details) != 0 {
		t.Errorf("details = %v, want empty non-nil slice", page.Details)
	}
	if page.Pagination.TotalItems != 0 || page.Pagination.TotalPages != 0 {
		t.Errorf("pagination = %+v, want zeros", page.Pagination)
	}
	if page.Pagination.HasNext || page.Pagination.HasPrev {
		t.Errorf("empty base must have no next/prev: %+v", page.Pagination)
	}
}

func TestGetRequestDetailByID(t *testing.T) {
	r := usageTestDB(t)
	r.rawExec(t, `INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data)
		VALUES ('abc', '2026-07-18T10:00:00Z', 'openai', 'gpt-4o', 'conn-1', 'success',
		        '{"id":"abc","model":"gpt-4o"}')`)

	detail, err := r.GetRequestDetailByID("abc")
	if err != nil {
		t.Fatalf("GetRequestDetailByID: %v", err)
	}
	if detail == nil || detail["model"] != "gpt-4o" {
		t.Errorf("detail = %v, want model gpt-4o", detail)
	}

	missing, err := r.GetRequestDetailByID("nope")
	if err != nil {
		t.Fatalf("missing id must not error: %v", err)
	}
	if missing != nil {
		t.Errorf("missing id = %v, want nil", missing)
	}
}

func TestDistinctRequestDetailValues(t *testing.T) {
	r := usageTestDB(t)
	r.rawExec(t, `INSERT INTO requestDetails (id, timestamp, provider, model, data) VALUES ('a','t','openai','gpt-4o','{}')`)
	r.rawExec(t, `INSERT INTO requestDetails (id, timestamp, provider, model, data) VALUES ('b','t','anthropic','claude-3','{}')`)

	providers := r.DistinctRequestDetailProviders()
	if len(providers) != 2 || providers[0] != "anthropic" || providers[1] != "openai" {
		t.Errorf("providers = %v, want [anthropic openai]", providers)
	}

	models := r.DistinctRequestDetailModels()
	if len(models) != 2 || models[0] != "claude-3" || models[1] != "gpt-4o" {
		t.Errorf("models = %v, want [claude-3 gpt-4o]", models)
	}
}

func TestDistinctRequestDetailValuesEmptyBaseIsEmptySlice(t *testing.T) {
	r := usageTestDB(t)
	if p := r.DistinctRequestDetailProviders(); p == nil || len(p) != 0 {
		t.Errorf("providers = %v, want empty non-nil slice", p)
	}
	if m := r.DistinctRequestDetailModels(); m == nil || len(m) != 0 {
		t.Errorf("models = %v, want empty non-nil slice", m)
	}
}

func TestMaskAPIKey(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"short":               "***",
		"12345678":            "***",
		"sk-abcdefghijklmnop": "sk-a***mnop",
	}
	for in, want := range cases {
		if got := MaskAPIKey(in); got != want {
			t.Errorf("MaskAPIKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseUsageTokensAcceptsBothSpellings(t *testing.T) {
	openai := parseUsageTokens(`{"prompt_tokens":10,"completion_tokens":5,"cached_tokens":2}`)
	if openai.prompt() != 10 || openai.completion() != 5 || openai.cached() != 2 {
		t.Errorf("openai spelling = %+v", openai)
	}

	anthropic := parseUsageTokens(`{"input_tokens":20,"output_tokens":7,"cache_read_input_tokens":3}`)
	if anthropic.prompt() != 20 || anthropic.completion() != 7 || anthropic.cached() != 3 {
		t.Errorf("anthropic spelling = %+v", anthropic)
	}

	if (usageTokens{}).prompt() != 0 {
		t.Error("empty tokens must read zero")
	}
	if parseUsageTokens("not json").prompt() != 0 {
		t.Error("malformed token JSON must read zero, not panic")
	}
}
