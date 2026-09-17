package db

import (
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Usage totals for one bucket (a provider, a model, an account, ...).
// Field order and JSON names mirror the VansRouter dashboard contract so the
// same UI renderer can consume both.
type UsageBucket struct {
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	CachedTokens     int     `json:"cachedTokens"`
	Cost             float64 `json:"cost"`
	RawModel         string  `json:"rawModel,omitempty"`
	Provider         string  `json:"provider,omitempty"`
	LastUsed         string  `json:"lastUsed,omitempty"`
	ConnectionID     string  `json:"connectionId,omitempty"`
	AccountName      string  `json:"accountName,omitempty"`
	APIKey           string  `json:"apiKey,omitempty"`
	APIKeyMasked     string  `json:"apiKeyMasked,omitempty"`
	KeyName          string  `json:"keyName,omitempty"`
	APIKeyKey        string  `json:"apiKeyKey,omitempty"`
	Endpoint         string  `json:"endpoint,omitempty"`
}

// UsagePoint is one bucket of the usage chart.
type UsagePoint struct {
	Label  string  `json:"label"`
	Tokens int     `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// RecentUsageEntry backs the "recent requests" list on the overview.
type RecentUsageEntry struct {
	Timestamp        string `json:"timestamp"`
	Model            string `json:"model"`
	Provider         string `json:"provider"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	CachedTokens     int    `json:"cachedTokens"`
	Status           string `json:"status"`
}

// ActiveUsageRequest describes requests currently in flight.
type ActiveUsageRequest struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Count    int    `json:"count"`
}

// UsageStats is the payload of GET /api/dashboard/usage/stats.
// It is a superset of what the dashboard renders; every map is keyed the same
// way the daily aggregation keys its entries.
type UsageStats struct {
	TotalRequests         int     `json:"totalRequests"`
	TotalPromptTokens     int     `json:"totalPromptTokens"`
	TotalCompletionTokens int     `json:"totalCompletionTokens"`
	TotalCachedTokens     int     `json:"totalCachedTokens"`
	TotalCost             float64 `json:"totalCost"`

	ByProvider map[string]UsageBucket `json:"byProvider"`
	ByModel    map[string]UsageBucket `json:"byModel"`
	ByAccount  map[string]UsageBucket `json:"byAccount"`
	ByAPIKey   map[string]UsageBucket `json:"byApiKey"`
	ByEndpoint map[string]UsageBucket `json:"byEndpoint"`

	RecentRequests []RecentUsageEntry   `json:"recentRequests"`
	ActiveRequests []ActiveUsageRequest `json:"activeRequests"`
}

// usageDailyDoc is the decoded shape of the usageDaily.data JSON blob.
// Only the fields the dashboard reads are declared; unknown keys are ignored.
type usageDailyDoc struct {
	Requests         int                    `json:"requests"`
	PromptTokens     int                    `json:"promptTokens"`
	CompletionTokens int                    `json:"completionTokens"`
	CachedTokens     int                    `json:"cachedTokens"`
	Cost             float64                `json:"cost"`
	ByProvider       map[string]UsageBucket `json:"byProvider"`
	ByModel          map[string]UsageBucket `json:"byModel"`
	ByAccount        map[string]UsageBucket `json:"byAccount"`
	ByApiKey         map[string]UsageBucket `json:"byApiKey"`
	ByEndpoint       map[string]UsageBucket `json:"byEndpoint"`
}

// GetUsageDailyRange returns the usageDaily rows whose dateKey falls inside
// [fromKey, toKey] (inclusive, lexicographic compare — dateKey is YYYY-MM-DD).
// Pass an empty toKey for "no upper bound".
func (r *Repo) GetUsageDailyRange(fromKey, toKey string) (map[string]usageDailyDoc, error) {
	query := `SELECT dateKey, data FROM usageDaily WHERE dateKey >= ?`
	args := []any{fromKey}
	if toKey != "" {
		query += ` AND dateKey <= ?`
		args = append(args, toKey)
	}
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query usageDaily range: %w", err)
	}
	defer rows.Close()

	out := make(map[string]usageDailyDoc)
	for rows.Next() {
		var dateKey, data string
		if err := rows.Scan(&dateKey, &data); err != nil {
			return nil, fmt.Errorf("scan usageDaily row: %w", err)
		}
		var doc usageDailyDoc
		if err := json.Unmarshal([]byte(data), &doc); err != nil {
			// A malformed day must not take down the whole dashboard; skip it.
			continue
		}
		out[dateKey] = doc
	}
	return out, rows.Err()
}

// usageHistoryRow is one raw usage record.
type usageHistoryRow struct {
	Timestamp        string
	Provider         string
	Model            string
	ConnectionID     string
	APIKey           string
	Endpoint         string
	PromptTokens     int
	CompletionTokens int
	Cost             float64
	Status           string
	Tokens           string
}

// GetUsageHistorySince returns raw history rows at or after the given RFC3339
// timestamp, oldest first.
func (r *Repo) GetUsageHistorySince(since string) ([]usageHistoryRow, error) {
	rows, err := r.db.Query(
		`SELECT timestamp, provider, model, connectionId, apiKey, endpoint,
		        promptTokens, completionTokens, cost, status, tokens
		   FROM usageHistory WHERE timestamp >= ? ORDER BY timestamp ASC`, since)
	if err != nil {
		return nil, fmt.Errorf("query usage history: %w", err)
	}
	defer rows.Close()
	return scanUsageHistory(rows)
}

// GetUsageHistoryRecent returns the newest raw history rows, newest first.
func (r *Repo) GetUsageHistoryRecent(limit int) ([]usageHistoryRow, error) {
	rows, err := r.db.Query(
		`SELECT timestamp, provider, model, connectionId, apiKey, endpoint,
		        promptTokens, completionTokens, cost, status, tokens
		   FROM usageHistory ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent usage history: %w", err)
	}
	defer rows.Close()
	return scanUsageHistory(rows)
}

type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanUsageHistory(rows rowScanner) ([]usageHistoryRow, error) {
	var out []usageHistoryRow
	for rows.Next() {
		// Columns are nullable in databases written by earlier versions, so scan
		// through Null* wrappers rather than failing the whole dashboard on a
		// legacy row that never recorded, say, a connection id.
		var h usageHistoryRow
		var ts, provider, model, connID, apiKey, endpoint, status, tokens sql.NullString
		var prompt, completion sql.NullInt64
		var cost sql.NullFloat64
		if err := rows.Scan(&ts, &provider, &model, &connID, &apiKey, &endpoint,
			&prompt, &completion, &cost, &status, &tokens); err != nil {
			return nil, fmt.Errorf("scan usage history: %w", err)
		}
		h.Timestamp = ts.String
		h.Provider = provider.String
		h.Model = model.String
		h.ConnectionID = connID.String
		h.APIKey = apiKey.String
		h.Endpoint = endpoint.String
		h.PromptTokens = int(prompt.Int64)
		h.CompletionTokens = int(completion.Int64)
		h.Cost = cost.Float64
		h.Status = status.String
		h.Tokens = tokens.String
		out = append(out, h)
	}
	return out, rows.Err()
}

// usageTokens is the decoded `tokens` JSON column. Upstream field names vary by
// translator, so both spellings are accepted.
type usageTokens struct {
	PromptTokens     int `json:"prompt_tokens"`
	InputTokens      int `json:"input_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens"`
}

func (t usageTokens) prompt() int {
	if t.PromptTokens != 0 {
		return t.PromptTokens
	}
	return t.InputTokens
}

func (t usageTokens) completion() int {
	if t.CompletionTokens != 0 {
		return t.CompletionTokens
	}
	return t.OutputTokens
}

func (t usageTokens) cached() int {
	if t.CachedTokens != 0 {
		return t.CachedTokens
	}
	return t.CacheReadTokens
}

func parseUsageTokens(raw string) usageTokens {
	if raw == "" {
		return usageTokens{}
	}
	var t usageTokens
	_ = json.Unmarshal([]byte(raw), &t)
	return t
}

// ConnectionNameMap maps connection id -> display name, preferring the
// dedicated name column then email then the id itself. Older/imported rows
// sometimes only carry the name inside the data JSON, so that is used as a
// fallback before giving up.
func (r *Repo) ConnectionNameMap() map[string]string {
	out := make(map[string]string)
	rows, err := r.db.Query(`SELECT id, COALESCE(name,''), COALESCE(email,''), COALESCE(data,'{}') FROM providerConnections`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, email, data string
		if err := rows.Scan(&id, &name, &email, &data); err != nil {
			continue
		}
		if name == "" && email == "" {
			var d struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			}
			if json.Unmarshal([]byte(data), &d) == nil {
				name, email = d.Name, d.Email
			}
		}
		display := name
		if display == "" {
			display = email
		}
		if display == "" {
			display = id
		}
		out[id] = display
	}
	return out
}

// ProviderNodeNameMap maps provider-node id -> display name.
func (r *Repo) ProviderNodeNameMap() map[string]string {
	out := make(map[string]string)
	rows, err := r.db.Query(`SELECT id, name FROM providerNodes`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		if id != "" && name != "" {
			out[id] = name
		}
	}
	return out
}

// APIKeyNameMap maps a full API key value -> friendly name.
func (r *Repo) APIKeyNameMap() map[string]string {
	out := make(map[string]string)
	rows, err := r.db.Query(`SELECT key, name FROM apiKeys`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var key, name string
		if err := rows.Scan(&key, &name); err != nil {
			continue
		}
		if key != "" {
			out[key] = name
		}
	}
	return out
}

// MaskAPIKey returns the same masked form the writer stores, so lookups by
// masked value succeed.
func MaskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "***" + key[len(key)-4:]
}

// shortID renders the "Account <8 chars>..." fallback used when a connection
// id has no matching row.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// UsagePeriodDays maps a period keyword to the number of days to load, and
// whether the period is served from the daily rollup (as opposed to raw
// history). Returns (days, useDaily, ok); a nil days value with useDaily=true
// means "all days".
func UsagePeriodDays(period string) (days int, useDaily bool, ok bool) {
	switch period {
	case "today", "24h":
		return 1, false, true
	case "7d":
		return 7, true, true
	case "30d":
		return 30, true, true
	case "60d":
		return 60, true, true
	case "all":
		return 0, true, true
	default:
		return 0, false, false
	}
}

// PeriodCutoff returns the inclusive RFC3339 lower bound for a period.
// "today" means local midnight; every other period is a rolling window.
func PeriodCutoff(period string, now time.Time) time.Time {
	switch period {
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	case "24h":
		return now.Add(-24 * time.Hour)
	default:
		days, useDaily, _ := UsagePeriodDays(period)
		if !useDaily || days == 0 {
			return time.Time{}
		}
		y, m, d := now.Date()
		startOfToday := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
		return startOfToday.AddDate(0, 0, -(days - 1))
	}
}

// dateKeyFor formats a time as the YYYY-MM-DD key used by usageDaily.
func dateKeyFor(t time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
}

// sortedKeys returns map keys in deterministic order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isZeroBucket reports whether a bucket carries no activity, so empty days
// never introduce phantom rows.
func isZeroBucket(b UsageBucket) bool {
	return b.Requests == 0 && b.PromptTokens == 0 && b.CompletionTokens == 0 && b.CachedTokens == 0 && b.Cost == 0
}

// splitModelKey splits a "model|provider" aggregation key. Keys without a
// separator yield the whole string as the model.
func splitModelKey(key string) (model, provider string) {
	if i := strings.LastIndex(key, "|"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}
