package db

import (
	json "encoding/json/v2"
	"fmt"
	"strconv"
	"strings"
)

// RequestDetailFilter narrows the request-detail listing.
type RequestDetailFilter struct {
	Provider     string
	Model        string
	ConnectionID string
	Status       string
	StartDate    string
	EndDate      string
	Page         int
	PageSize     int
}

// RequestDetailPagination mirrors the upstream pagination envelope.
type RequestDetailPagination struct {
	Page       int  `json:"page"`
	PageSize   int  `json:"pageSize"`
	TotalItems int  `json:"totalItems"`
	TotalPages int  `json:"totalPages"`
	HasNext    bool `json:"hasNext"`
	HasPrev    bool `json:"hasPrev"`
}

// RequestDetailPage is the payload of the request-details endpoint.
type RequestDetailPage struct {
	Details    []map[string]any        `json:"details"`
	Pagination RequestDetailPagination `json:"pagination"`
}

// maxRequestDetailPageSize caps page size exactly like the upstream route,
// which rejects anything above 100.
const maxRequestDetailPageSize = 100

// GetRequestDetails returns one page of request details, newest first.
func (r *Repo) GetRequestDetails(f RequestDetailFilter) (*RequestDetailPage, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 20
	}
	if f.PageSize > maxRequestDetailPageSize {
		return nil, fmt.Errorf("pageSize must be between 1 and %d", maxRequestDetailPageSize)
	}

	var conds []string
	var args []any
	if f.Provider != "" {
		conds = append(conds, "provider = ?")
		args = append(args, f.Provider)
	}
	if f.Model != "" {
		conds = append(conds, "model = ?")
		args = append(args, f.Model)
	}
	if f.ConnectionID != "" {
		conds = append(conds, "connectionId = ?")
		args = append(args, f.ConnectionID)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	if f.StartDate != "" {
		conds = append(conds, "timestamp >= ?")
		args = append(args, f.StartDate)
	}
	if f.EndDate != "" {
		conds = append(conds, "timestamp <= ?")
		args = append(args, f.EndDate)
	}

	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var totalItems int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM requestDetails`+where, args...).Scan(&totalItems); err != nil {
		return nil, fmt.Errorf("count request details: %w", err)
	}

	totalPages := 0
	if f.PageSize > 0 {
		totalPages = (totalItems + f.PageSize - 1) / f.PageSize
	}
	offset := (f.Page - 1) * f.PageSize

	rows, err := r.db.Query(
		`SELECT data FROM requestDetails`+where+` ORDER BY timestamp DESC LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), f.PageSize, offset)...)
	if err != nil {
		return nil, fmt.Errorf("query request details: %w", err)
	}
	defer rows.Close()

	details := make([]map[string]any, 0, f.PageSize)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan request detail: %w", err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			continue
		}
		details = append(details, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &RequestDetailPage{
		Details: details,
		Pagination: RequestDetailPagination{
			Page:       f.Page,
			PageSize:   f.PageSize,
			TotalItems: totalItems,
			TotalPages: totalPages,
			HasNext:    f.Page < totalPages,
			HasPrev:    f.Page > 1 && totalItems > 0,
		},
	}, nil
}

// GetRequestDetailByID returns a single stored detail payload, or nil.
func (r *Repo) GetRequestDetailByID(id string) (map[string]any, error) {
	var data string
	err := r.db.QueryRow(`SELECT data FROM requestDetails WHERE id = ?`, id).Scan(&data)
	if err != nil {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return nil, nil
	}
	return m, nil
}

// DistinctRequestDetailProviders lists providers that have recorded details,
// for the filter dropdown.
func (r *Repo) DistinctRequestDetailProviders() []string {
	out := []string{}
	rows, err := r.db.Query(
		`SELECT DISTINCT provider FROM requestDetails WHERE provider IS NOT NULL AND provider != '' ORDER BY provider ASC`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// DistinctRequestDetailModels lists models that have recorded details.
func (r *Repo) DistinctRequestDetailModels() []string {
	out := []string{}
	rows, err := r.db.Query(
		`SELECT DISTINCT model FROM requestDetails WHERE model IS NOT NULL AND model != '' ORDER BY model ASC`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

// ParsePageParam parses a positive integer query parameter with a fallback.
func ParsePageParam(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
