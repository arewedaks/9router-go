package dashboard

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/handlerutil"
)

// HandleTestConnection probes a single account and reports the verdict.
//
// POST /api/dashboard/connections/{id}/test
//
//	{} → {"connectionId":"...","valid":true,"status":200,"latencyMs":412,...}
//
// The probe is read-only: nothing about the connection is mutated. See
// testConnection for why upstream's stateful behaviour is deliberately not
// ported to this fork.
//
// A completed probe always returns HTTP 200, even when Valid is false — the
// request succeeded in producing a verdict, and the UI distinguishes the
// verdict from the transport. 404 is reserved for an unknown connection id,
// mirroring upstream.
func (h *Handler) HandleTestConnection(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "connection id is required")
		return
	}

	conn, err := h.repo.GetProviderConnectionByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to load connection: "+err.Error())
		return
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "Connection not found")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), connectionTestTimeout())
	defer cancel()

	result := h.testConnection(ctx, id)
	handlerutil.WriteJSON(w, http.StatusOK, result)
}

// connectionTestBatchRequest is the body for a provider-wide connection test.
type connectionTestBatchRequest struct {
	// Parallel opts into concurrent probing. Defaults to SEQUENTIAL: in this
	// fork the OAuth refresher has no per-connection mutex or in-flight dedup,
	// so fanning probes out concurrently is the only way to risk two refreshes
	// racing on the same refresh token. Sequential is the safe default; the
	// operator can still opt in.
	Parallel bool `json:"parallel"`
	// ConnectionIDs optionally restricts the test to specific accounts.
	ConnectionIDs []string `json:"connectionIds,omitempty"`
}

// HandleTestProviderConnections tests every (or a subset of) connection(s) for
// one provider. Mirrors upstream POST /api/providers/test-batch, adapted to the
// per-provider route shape used by this dashboard.
//
// POST /api/dashboard/providers/{id}/test-connections
//
//	{"parallel":false,"connectionIds":["..."]}
//
// Responses are bound by connection id so the UI can attach each verdict to the
// exact row that was probed, never to a row index.
func (h *Handler) HandleTestProviderConnections(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(chi.URLParam(r, "id"))
	if provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "provider id is required")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var req connectionTestBatchRequest
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
			return
		}
	}

	conns, err := h.repo.GetProviderConnections(provider, false)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to load connections: "+err.Error())
		return
	}

	// Optional subset filter. Empty means "all".
	wanted := map[string]bool{}
	for _, id := range req.ConnectionIDs {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}

	ids := make([]string, 0, len(conns))
	for _, c := range conns {
		if len(wanted) > 0 && !wanted[c.ID] {
			continue
		}
		ids = append(ids, c.ID)
	}

	if len(ids) == 0 {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"provider": provider,
			"results":  []connectionTestResult{},
			"total":    0,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), connectionTestBatchDeadline(len(ids), req.Parallel))
	defer cancel()

	results := make([]connectionTestResult, len(ids))

	if req.Parallel {
		var wg sync.WaitGroup
		wg.Add(len(ids))
		for i, id := range ids {
			go func(i int, id string) {
				defer wg.Done()
				results[i] = h.testConnection(ctx, id)
			}(i, id)
		}
		wg.Wait()
	} else {
		// Warm-up-first ordering: the first probe may trigger an upstream token
		// refresh. Running it alone before the rest keeps any such refresh
		// serialised rather than racing with the remaining probes.
		for i, id := range ids {
			if ctx.Err() != nil {
				results[i] = connectionTestResult{ConnectionID: id, Error: "Test cancelled."}
				continue
			}
			results[i] = h.testConnection(ctx, id)
		}
	}

	okCount := 0
	for _, res := range results {
		if res.Valid && !res.GeoBlocked && res.Warning == "" {
			okCount++
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider": provider,
		"results":  results,
		"total":    len(results),
		"ok":       okCount,
	})
}

// connectionTestBatchDeadline budgets a whole batch. Each probe may take up to
// connectionTestTimeout(); sequential runs add up, so the deadline scales with
// the number of accounts (capped) rather than reusing a single probe timeout —
// otherwise a batch of 10 sequential probes would be killed after the first.
func connectionTestBatchDeadline(n int, parallel bool) time.Duration {
	per := connectionTestTimeout()
	if parallel {
		return per + 10*time.Second
	}
	d := time.Duration(n)*per + 10*time.Second
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}
