package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"strconv"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/resilience"
	"9router/proxy/internal/sysmetrics"
	"9router/proxy/internal/tracing"
)

// consolePingInterval matches the Next dashboard's 25s keepalive so proxies
// that idle-out connections keep the SSE stream alive.
const consolePingInterval = 25 * time.Second

// HandleConsoleLogsGet returns the buffered console logs (translator
// console-logs GET). Matches Next's { success, logs } shape.
func HandleConsoleLogsGet(w http.ResponseWriter, r *http.Request) {
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"logs":    log.ConsoleLogs(),
	})
}

// HandleDebugTraces returns recent request spans + p50/p95/p99 latency per
// provider+model. Optional ?n= query caps the span list.
func HandleDebugTraces(w http.ResponseWriter, r *http.Request) {
	n := 0
	if q := r.URL.Query().Get("n"); q != "" {
		if v, err := strconv.Atoi(q); err == nil && v > 0 {
			n = v
		}
	}
	body, err := tracing.JSON(n)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// HandleResilienceStatus reports the circuit-breaker and semaphore state, so an
// operator can see which provider is being skipped and which accounts are
// saturated. POST /debug/resilience/reset clears the breakers.
func HandleResilienceStatus(w http.ResponseWriter, r *http.Request) {
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"breakers":  resilience.AllBreakerStatuses(),
		"semaphores": resilience.SemaphoreStats(),
	})
}

// HandleResilienceReset closes every breaker. Used after fixing an upstream so
// the gateway stops waiting out the backoff.
func HandleResilienceReset(w http.ResponseWriter, r *http.Request) {
	resilience.ResetAllBreakers()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleConsoleLogsDelete clears the buffered console logs.
func HandleConsoleLogsDelete(w http.ResponseWriter, r *http.Request) {
	log.ClearConsoleLogs()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleConsoleLogsStream streams live console output over SSE. On connect it
// sends the buffered logs as an "init" event, then "line" events as they
// arrive and a "clear" event on buffer clear, with a keepalive ping every 25s.
func HandleConsoleLogsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(v map[string]any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		if _, err := w.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Buffered logs first.
	if buffered := log.ConsoleLogs(); len(buffered) > 0 {
		if !send(map[string]any{"type": "init", "logs": buffered}) {
			return
		}
	}

	ch, cancel := log.SubscribeConsole()
	defer cancel()

	ping := time.NewTicker(consolePingInterval)
	defer ping.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			switch ev.Kind() {
			case "clear":
				if !send(map[string]any{"type": "clear"}) {
					return
				}
			default:
				if !send(map[string]any{"type": "line", "line": ev.Line()}) {
					return
				}
			}
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// HandleSystemMetrics reports this process's CPU and memory use plus the host's
// memory, for the resource card on the Console Log page.
//
// The first call of a process returns cpuPercent 0 because a percentage needs
// two samples to difference; the client polls, so the second call onward is
// meaningful. That is stated in the payload via Supported rather than hidden.
func HandleSystemMetrics(w http.ResponseWriter, r *http.Request) {
	sample := sysmetrics.Read()
	payload := map[string]any{
		"success": true,
		"metrics": sample,
	}
	if load, ok := sysmetrics.HostLoadAverage(); ok {
		payload["hostLoad1"] = load
	}
	handlerutil.WriteJSON(w, http.StatusOK, payload)
}
