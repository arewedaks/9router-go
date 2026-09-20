package dashboard

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// Model test ("ping") — port of the upstream Next.js
// POST /api/models/test and POST /api/providers/[id]/test-models actions.
//
// Strategy: the dashboard calls the proxy's own HTTP endpoints
// (/v1/chat/completions, /v1/embeddings, /v1/images/generations,
// /v1/audio/transcriptions) exactly like the Next.js build does. That way a
// test exercises the full real path — auth, OAuth refresh, combo resolution,
// fallback, translators — instead of a shortcut that could report a false
// green.

// modelTestTimeout is the per-model deadline. Mirrors upstream
// MODEL_TEST_TIMEOUT_MS (default 30s) so slow proxy pools / relays / cold
// upstreams still have a chance to answer.
func modelTestTimeout() time.Duration {
	if v := os.Getenv("MODEL_TEST_TIMEOUT_MS"); v != "" {
		if n, err := time.ParseDuration(v + "ms"); err == nil && n > 0 {
			return n
		}
	}
	return 30 * time.Second
}

// modelTestResult is the per-model outcome surfaced in the UI.
type modelTestResult struct {
	ModelID   string `json:"modelId"`
	Name      string `json:"name,omitempty"`
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
	Note      string `json:"note,omitempty"`
	Status    int    `json:"status,omitempty"`
	// TimedOut distinguishes "the provider did not answer in time" from "the
	// provider answered with a failure". Auto-disable must only act on the
	// second: a slow-but-healthy model is still a usable model, and deleting it
	// loses an entry the operator imported deliberately. With 110 models on one
	// node and a 30s budget, a batch run would otherwise prune healthy models
	// whose provider merely took longer than the timeout.
	TimedOut bool `json:"timedOut,omitempty"`
}

// activeApiKey returns any active API key so the dashboard can call the
// proxy's own protected endpoints. Mirrors upstream getInternalHeaders().
func (h *Handler) activeApiKey() string {
	keys, err := h.repo.GetAllApiKeys()
	if err != nil {
		return ""
	}
	for _, k := range keys {
		if k.IsActive == 1 && k.Key != "" {
			return k.Key
		}
	}
	// Fall back to the first key even if marked inactive — a local self-call
	// should not be blocked by a UI flag.
	if len(keys) > 0 {
		return keys[0].Key
	}
	return ""
}

// selfBaseURL builds the loopback base URL for internal calls.
func selfBaseURL() string {
	port := os.Getenv("PORT")
	if port == "" {
		port = "20128"
	}
	return "http://127.0.0.1:" + port
}

// pingModel sends one test request for the given model and kind, returning a
// normalised result. kind is one of: llm (default), embedding, image, stt.
func (h *Handler) pingModel(ctx context.Context, model, kind string) modelTestResult {
	if kind == "" {
		kind = "llm"
	}
	start := time.Now()

	res, err := h.pingModelImpl(ctx, model, kind)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		// Normalise aborts/timeouts into an actionable message, like upstream.
		if ctx.Err() != nil {
			secs := int(modelTestTimeout().Seconds())
			return modelTestResult{
				ModelID: model, OK: false, LatencyMs: latency, TimedOut: true,
				Error: fmt.Sprintf("Test timed out after %ds — provider too slow or unreachable. "+
					"The model may not exist on this provider, or the route (proxy pool / relay) is slow. "+
					"Increase MODEL_TEST_TIMEOUT_MS to allow more time.", secs),
			}
		}
		return modelTestResult{ModelID: model, OK: false, LatencyMs: latency, Error: err.Error()}
	}
	res.ModelID = model
	res.LatencyMs = latency
	return res
}

// pingModelImpl performs the actual HTTP call for a given kind.
func (h *Handler) pingModelImpl(ctx context.Context, model, kind string) (modelTestResult, error) {
	apiKey := h.activeApiKey()
	base := selfBaseURL()
	client := &http.Client{Timeout: modelTestTimeout()}

	switch kind {
	case "embedding":
		body, _ := json.Marshal(map[string]any{"model": model, "input": "test"})
		return h.doJSONTest(ctx, client, base+"/v1/embeddings", body, apiKey, "embedding")

	case "image":
		body, _ := json.Marshal(map[string]any{"model": model, "prompt": "test"})
		return h.doJSONTest(ctx, client, base+"/v1/images/generations", body, apiKey, "image")

	default: // llm
		body, _ := json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": 1024,
			"stream":     false,
			"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		})
		return h.doJSONTest(ctx, client, base+"/v1/chat/completions", body, apiKey, "llm")
	}
}

// doJSONTest POSTs a JSON body to the proxy's own endpoint and interprets the
// response for the given kind.
func (h *Handler) doJSONTest(ctx context.Context, client *http.Client, url string, body []byte, apiKey, kind string) (modelTestResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return modelTestResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return modelTestResult{}, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := extractErrorMessage(parsed, raw)
		return modelTestResult{
			OK:     false,
			Status: resp.StatusCode,
			Error:  fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(detail, 240)),
		}, nil
	}

	// Provider-level error smuggled inside a 200 (common for compat shims).
	if e, ok := parsed["error"].(map[string]any); ok && e != nil {
		msg := truncate(anyString(e["message"]), 240)
		if msg == "" {
			msg = truncate(anyString(parsed["error"]), 240)
		}
		return modelTestResult{OK: false, Status: resp.StatusCode, Error: msg}, nil
	}
	if st, ok := parsed["status"]; ok {
		s := fmt.Sprint(st)
		if s != "" && s != "200" && s != "0" {
			msg := truncate(firstNonEmpty(anyString(parsed["msg"]), anyString(parsed["message"])), 240)
			if msg != "" {
				return modelTestResult{OK: false, Status: resp.StatusCode,
					Error: fmt.Sprintf("Provider status %s: %s", s, msg)}, nil
			}
		}
	}

	switch kind {
	case "embedding":
		data, _ := parsed["data"].([]any)
		if len(data) == 0 {
			return modelTestResult{OK: false, Status: resp.StatusCode,
				Error: "Provider returned no embedding data"}, nil
		}
		if first, ok := data[0].(map[string]any); ok {
			if emb, ok := first["embedding"].([]any); !ok || len(emb) == 0 {
				return modelTestResult{OK: false, Status: resp.StatusCode,
					Error: "Provider returned no embedding data"}, nil
			}
		}
		return modelTestResult{OK: true, Status: resp.StatusCode}, nil

	case "image":
		data, _ := parsed["data"].([]any)
		if len(data) == 0 {
			return modelTestResult{OK: false, Status: resp.StatusCode,
				Error: "Provider returned no image data for this model"}, nil
		}
		return modelTestResult{OK: true, Status: resp.StatusCode}, nil

	default: // llm
		choices, _ := parsed["choices"].([]any)
		if len(choices) == 0 {
			return modelTestResult{OK: false, Status: resp.StatusCode,
				Error: "Provider returned no completion choices for this model"}, nil
		}
		// Reasoning-only response with finish_reason=length still counts as OK.
		if first, ok := choices[0].(map[string]any); ok {
			if fr, _ := first["finish_reason"].(string); fr == "length" {
				msg, _ := first["message"].(map[string]any)
				content := anyString(msg["content"])
				hasReasoning := msg["reasoning"] != nil || msg["reasoning_content"] != nil ||
					msg["thinking"] != nil || msg["thinking_content"] != nil
				if strings.TrimSpace(content) == "" && hasReasoning {
					return modelTestResult{OK: true, Status: resp.StatusCode,
						Note: "reasoning-only response (length-limited)"}, nil
				}
			}
		}
		return modelTestResult{OK: true, Status: resp.StatusCode}, nil
	}
}

// HandleModelTest pings a single model.
//
//	POST /api/dashboard/models/test
//	body: {"model":"ag/claude-sonnet-4-6","kind":"llm"}
func (h *Handler) HandleModelTest(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var req struct {
		Model string `json:"model"`
		Kind  string `json:"kind"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "model is required")
		return
	}
	if req.Kind == "" {
		req.Kind = "llm"
	}

	ctx, cancel := context.WithTimeout(r.Context(), modelTestTimeout())
	defer cancel()

	res := h.pingModel(ctx, req.Model, req.Kind)
	handlerutil.WriteJSON(w, http.StatusOK, res)
}

// HandleTestProviderModels pings every model configured for a provider.
//
//	POST /api/dashboard/providers/{id}/test-models
//	body: {"parallel":true,"autoDisableFailed":false,"models":["..."]}
//
// If "models" is omitted, the provider's cached model list is used. The first
// model is warmed up sequentially (triggers OAuth refresh) before the rest run
// in parallel, matching upstream behaviour and avoiding a token-refresh race.
func (h *Handler) HandleTestProviderModels(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "id")
	canonical := providers.ResolveAlias(raw)

	body, _ := io.ReadAll(r.Body)
	var req struct {
		Parallel          bool     `json:"parallel"`
		AutoDisableFailed bool     `json:"autoDisableFailed"`
		Models            []string `json:"models"`
	}
	_ = json.Unmarshal(body, &req)

	// Resolve the provider alias used in full model IDs (e.g. "ag" for
	// antigravity), falling back to the canonical id / raw id.
	alias := providerAliasFor(canonical, raw)

	models := req.Models
	if len(models) == 0 {
		keys := []string{canonical, raw, alias}
		keys = append(keys, providers.AliasesFor(canonical)...)
		keys = append(keys, providers.AliasesFor(raw)...)
		// ListCachedModelsForDashboard, not ListCachedModels: a database restored
		// from the Next.js build keeps its catalogue in kv scope customModels, so
		// the narrow query returned nothing here and "Test All Models" silently
		// tested zero models — nothing was disabled even with auto-disable ticked.
		cached, err := h.repo.ListCachedModelsForDashboard(dedupeStrings(keys)...)
		if err == nil {
			for _, m := range cached {
				// Only LLM-kind models are supported by the built-in test path.
				if m.Kind == "" || m.Kind == "llm" {
					models = append(models, alias+"/"+m.ModelID)
				}
			}
		}
	}
	if len(models) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "No models configured for this provider")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), modelTestTimeout()*time.Duration(len(models)+1))
	defer cancel()

	results := make([]modelTestResult, 0, len(models))

	// Warm-up: run the first model alone so any OAuth refresh completes before
	// the parallel burst starts.
	first := models[0]
	firstStart := time.Now()
	firstRes := h.pingModel(ctx, first, "llm")
	results = append(results, modelTestResultFor(first, firstRes, time.Since(firstStart).Milliseconds()))
	h.maybeAutoDisable(first, firstRes, req.AutoDisableFailed)

	if rest := models[1:]; len(rest) > 0 {
		if req.Parallel {
			var mu sync.Mutex
			var wg sync.WaitGroup
			restRes := make([]modelTestResult, len(rest))
			for i, m := range rest {
				wg.Add(1)
				go func(idx int, model string) {
					defer wg.Done()
					started := time.Now()
					res := h.pingModel(ctx, model, "llm")
					restRes[idx] = modelTestResultFor(model, res, time.Since(started).Milliseconds())
					mu.Lock()
					h.maybeAutoDisable(model, res, req.AutoDisableFailed)
					mu.Unlock()
				}(i, m)
			}
			wg.Wait()
			results = append(results, restRes...)
		} else {
			for _, m := range rest {
				started := time.Now()
				res := h.pingModel(ctx, m, "llm")
				results = append(results, modelTestResultFor(m, res, time.Since(started).Milliseconds()))
				h.maybeAutoDisable(m, res, req.AutoDisableFailed)
			}
		}
	}

	passed := 0
	for _, res := range results {
		if res.OK {
			passed++
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":   canonical,
		"connection": raw,
		"results":    results,
		"summary": map[string]int{
			"total":  len(results),
			"passed": passed,
			"failed": len(results) - passed,
		},
		"testedAt": time.Now().UTC().Format(time.RFC3339),
	})
}

// isPermanentModelFailure reports whether an HTTP status is evidence that this
// model cannot be called, as opposed to a condition that may pass on a retry.
//
// Only the provider's own refusal counts:
//
//  400 bad request, 401 unauthorised, 403 forbidden, 404 not found,
//  410 gone, 422 unprocessable — the provider evaluated the request and
//  rejected it. A 404 from a provider that knows its models is the strongest
//  signal there is that the model does not exist on this account.
//
// Everything else is excluded deliberately:
//
//  408 request timeout, 429 rate limited, 5xx gateway/origin errors — these say
//  the provider is busy or unwell, nothing about the model. A parallel run over
//  a large node triggers 429 against its own upstream, and acting on it deletes
//  healthy models: measured live, three models a parallel run reported as
//  "failed" all answered 200 on a serial retry a minute later.
//
//  0 means no HTTP response was produced at all (connection refused, DNS
//  failure). Nothing was learned, so nothing may be removed.
func isPermanentModelFailure(status int) bool {
	switch status {
	case 400, 401, 403, 404, 410, 422:
		return true
	default:
		return false
	}
}

// maybeAutoDisable removes a model from the provider cache when it failed and
// the caller asked for auto-disable. Mirrors the upstream "Auto-disable
// failed" checkbox on the batch test toolbar.
func (h *Handler) maybeAutoDisable(fullModel string, res modelTestResult, autoDisable bool) {
	if res.OK || !autoDisable {
		return
	}
	// A timeout says the provider was slow, not that the model is gone. Pruning
	// on it deletes healthy models: 110 models on one node with a 30s budget
	// meant any provider answering slower than that lost its entries. Only a
	// real failure (4xx/5xx response) is evidence the model should go.
	if res.TimedOut {
		return
	}
	// Only a permanent rejection is evidence the model is gone. Everything else
	// means "ask again later" or "the provider is having a bad day", and acting
	// on it removes models that work.
	if !isPermanentModelFailure(res.Status) {
		return
	}
	// fullModel looks like "<alias>/<modelID>".
	alias, modelID, found := strings.Cut(fullModel, "/")
	if !found || modelID == "" {
		return
	}
	canonical := providers.ResolveAlias(alias)
	keys := append([]string{alias, canonical}, providers.AliasesFor(canonical)...)
	// The model is advertised under the node's prefix ("xkiro/<id>"), and the
	// list checks markers against that prefix. Recording only the generated
	// node id left the marker unreadable and the model stayed advertised.
	if p := h.nodePrefixKey(alias); p != "" {
		keys = append(keys, p)
	}
	if p := h.nodePrefixKey(canonical); p != "" {
		keys = append(keys, p)
	}
	keys = dedupeStrings(keys)
	for _, k := range keys {
		_ = h.repo.RemoveCachedModel(k, modelID)
	}
	// Removing from cachedProviderModels is not enough. A database restored from
	// the Next.js build keeps its catalogue in kv scope customModels, and
	// /v1/models reads that scope, so the failed model kept being advertised.
	// HandleRemoveModel records the same marker for the button path; without it
	// here, auto-disable appeared to do nothing.
	_ = h.repo.HideModel(keys, modelID)
}

// modelTestResultFor rebuilds a result echoing the exact model string that was
// requested, so the UI can match it back to the list it rendered.
func modelTestResultFor(requested string, res modelTestResult, latencyMs int64) modelTestResult {
	res.ModelID = requested
	res.LatencyMs = latencyMs
	return res
}

// providerAliasFor returns the short alias used when composing full model IDs
// for a provider, preferring any alias already present in the model cache.
func providerAliasFor(canonical, raw string) string {
	candidates := []string{canonical, raw}
	candidates = append(candidates, providers.AliasesFor(canonical)...)
	candidates = append(candidates, providers.AliasesFor(raw)...)
	for _, c := range candidates {
		if c != "" {
			return c
		}
	}
	return raw
}

// extractErrorMessage pulls a human-readable error out of an upstream body.
func extractErrorMessage(parsed map[string]any, raw []byte) string {
	if parsed != nil {
		if e, ok := parsed["error"].(map[string]any); ok {
			if m := anyString(e["message"]); m != "" {
				return m
			}
		}
		for _, k := range []string{"message", "msg", "error_detail", "detail", "error"} {
			if v, ok := parsed[k]; ok {
				if s := anyString(v); s != "" {
					return s
				}
			}
		}
	}
	return string(raw)
}

// anyString stringifies a JSON value that may already be a string or another
// scalar type.
func anyString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// dedupeStrings removes empty and duplicate entries while preserving order.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
