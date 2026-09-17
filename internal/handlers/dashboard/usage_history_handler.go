package dashboard

import (
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// usagePeriods are the period keywords the dashboard accepts. An unknown value
// falls back to 7d rather than erroring, so a stale bookmark still renders.
var usagePeriods = map[string]bool{
	"today": true,
	"24h":   true,
	"7d":    true,
	"30d":   true,
	"60d":   true,
	"all":   true,
}

// normalizePeriod validates a period query parameter.
func normalizePeriod(raw string) string {
	if usagePeriods[raw] {
		return raw
	}
	return "7d"
}

// HandleUsageHistoryStats serves GET /api/dashboard/usage/stats.
//
// This is the historical rollup the Usage page renders. It is distinct from the
// live /api/usage/stats endpoint, which streams in-flight requests for the
// topology graph.
func (h *Handler) HandleUsageHistoryStats(w http.ResponseWriter, r *http.Request) {
	period := normalizePeriod(r.URL.Query().Get("period"))
	stats, err := h.repo.GetUsageStats(period, time.Now())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, stats)
}

// HandleUsageHistoryChart serves GET /api/dashboard/usage/chart, returning the
// chart buckets as a bare array so the renderer can iterate directly.
func (h *Handler) HandleUsageHistoryChart(w http.ResponseWriter, r *http.Request) {
	period := normalizePeriod(r.URL.Query().Get("period"))
	points, err := h.repo.GetUsageChart(period, time.Now())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if points == nil {
		points = []db.UsagePoint{}
	}
	handlerutil.WriteJSON(w, http.StatusOK, points)
}

// HandleUsageHistory serves GET /api/dashboard/usage/history, the raw request
// log used by the "logs" tab.
func (h *Handler) HandleUsageHistory(w http.ResponseWriter, r *http.Request) {
	period := normalizePeriod(r.URL.Query().Get("period"))
	limit := db.ParsePageParam(r.URL.Query().Get("limit"), 100)
	if limit < 1 || limit > 1000 {
		limit = 100
	}

	rows, err := h.repo.GetUsageHistoryRecent(limit)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = period

	connNames := h.repo.ConnectionNameMap()
	apiKeyNames := h.repo.APIKeyNameMap()

	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		name := apiKeyNames[e.APIKey]
		masked := db.MaskAPIKey(e.APIKey)
		out = append(out, map[string]any{
			"timestamp":        e.Timestamp,
			"provider":         e.Provider,
			"model":            e.Model,
			"connectionId":     e.ConnectionID,
			"accountName":      connNames[e.ConnectionID],
			"apiKeyMasked":     masked,
			"apiKeyName":       name,
			"endpoint":         e.Endpoint,
			"promptTokens":     e.PromptTokens,
			"completionTokens": e.CompletionTokens,
			"cost":             e.Cost,
			"status":           e.Status,
		})
	}
	handlerutil.WriteJSON(w, http.StatusOK, out)
}

// HandleRequestDetails serves GET /api/dashboard/usage/request-details.
//
// The list intentionally keeps the stored payload intact (unlike the upstream
// route, which strips request/response bodies on the list endpoint), because
// the detail drawer here reads them straight from the list row.
func (h *Handler) HandleRequestDetails(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := db.RequestDetailFilter{
		Provider:     q.Get("provider"),
		Model:        q.Get("model"),
		ConnectionID: q.Get("connectionId"),
		Status:       q.Get("status"),
		StartDate:    q.Get("startDate"),
		EndDate:      q.Get("endDate"),
		Page:         db.ParsePageParam(q.Get("page"), 1),
		PageSize:     db.ParsePageParam(q.Get("pageSize"), 20),
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "pageSize must be between 1 and 100")
		return
	}

	page, err := h.repo.GetRequestDetails(filter)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, page)
}

// HandleRequestDetailByID serves GET /api/dashboard/usage/request-details/{id}.
func (h *Handler) HandleRequestDetailByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/dashboard/usage/request-details/")
	id = strings.TrimSpace(id)
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing request detail id")
		return
	}
	detail, err := h.repo.GetRequestDetailByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if detail == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "request detail not found")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"detail": detail})
}

// HandleUsageFilterOptions serves GET /api/dashboard/usage/filters, powering the
// request-details filter dropdowns.
func (h *Handler) HandleUsageFilterOptions(w http.ResponseWriter, r *http.Request) {
	providers := h.repo.DistinctRequestDetailProviders()
	models := h.repo.DistinctRequestDetailModels()
	if providers == nil {
		providers = []string{}
	}
	if models == nil {
		models = []string{}
	}

	type providerOption struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	opts := make([]providerOption, 0, len(providers))
	for _, p := range providers {
		opts = append(opts, providerOption{Value: p, Label: p})
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"providers": opts,
		"models":    models,
	})
}
