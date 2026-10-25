package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	internalproxy "9router/proxy/internal/proxy"
	"9router/proxy/internal/shutdown"
	"9router/proxy/internal/translator"
)

// forwardRequest sends the request to the upstream provider and streams/pipes the response.
func (h *ChatHandler) forwardRequest(
	ctx context.Context,
	w http.ResponseWriter,
	provider string,
	cfg *providers.ProviderConfig,
	apiKey string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
) error {
	// OpenAI-compat Gemini endpoints validate tool schemas as strictly as the
	// native one — sanitize tools so no unsupported JSON-Schema keyword reaches
	// them ("Invalid tool parameters" fix).
	if cfg.IsGeminiOpenAICompat() {
		if sanitized, serr := translator.SanitizeOpenAITools(body); serr == nil && sanitized != nil {
			body = sanitized
		}
	}
	// OpenRouter strict pattern validation (PR #3665): strip malformed regex patterns
	if strings.Contains(cfg.BaseURL, "openrouter.ai") {
		var bodyMap map[string]any
		if err := json.Unmarshal(body, &bodyMap); err == nil {
			if tools, ok := bodyMap["tools"].([]any); ok && len(tools) > 0 {
				if normalized, removed := translator.NormalizeToolSchemasForProvider("openrouter", tools); removed > 0 {
					bodyMap["tools"] = normalized
					log.Debug("tool_schema", "openrouter stripped invalid patterns", "removed", removed)
					if newBody, err := json.Marshal(bodyMap); err == nil {
						body = newBody
					}
				}
			}
		}
	}
	resp, err := h.forwardWithParamRepair(ctx, cfg, provider, apiKey, body, isStream)
	if err != nil {
		return fmt.Errorf("forward to upstream: %w", err)
	}

	var bodyCloser io.Closer = resp.Body
	defer func() {
		if bodyCloser != nil {
			bodyCloser.Close()
		}
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		if err != nil {
			return fmt.Errorf("read upstream error body: %w", err)
		}
		return &upstreamError{StatusCode: resp.StatusCode, Body: respBody}
	}

	start := time.Now()
	if metrics == nil {
		metrics = &streamMetrics{}
	}
	if isStream {
		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
			// Upstream returned non-streaming response (e.g. JSON error with 200 OK)
			log.Warn("stream", "non-stream response", "contentType", contentType)
			return h.handleJSONResponse(ctx, w, resp.Body, translateResponse, metrics)
		}
		// Wrap with SSE stall detection
		stallReader := internalproxy.NewStallReaderWithContext(ctx, resp.Body, 0, "upstream")
		bodyCloser = stallReader
		return h.handleStreamResponse(ctx, w, stallReader, translateResponse, start, metrics)
	}
	return h.handleJSONResponse(ctx, w, resp.Body, translateResponse, metrics)
}

// upstreamErrorFromBody promotes a 200 response whose body is a bare error
// object into a retryable upstream error. Some OpenAI-compatible relays
// (TokenHarbor among them) return the throttle as {"error":{...}} with status
// 200 instead of a 429; without this the caller sees "success" carrying the
// error text and no fallback happens.
//
// Only a top-level "error" with no "choices" counts: a normal completion can
// carry an "error" field inside a choice or tool payload and must pass through.
func upstreamErrorFromBody(body []byte) *upstreamError {
	var probe struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
		Choices []json.RawMessage `json:"choices"`
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil
	}
	if probe.Error == nil || len(probe.Choices) > 0 {
		return nil
	}
	text := strings.ToLower(probe.Error.Type + " " + probe.Error.Message + " " + fmt.Sprint(probe.Error.Code))
	status := http.StatusBadGateway
	switch {
	case strings.Contains(text, "rate") && (strings.Contains(text, "limit") || strings.Contains(text, "exceed")):
		status = http.StatusTooManyRequests
	case strings.Contains(text, "quota"), strings.Contains(text, "credit"), strings.Contains(text, "balance"):
		status = http.StatusTooManyRequests
	case strings.Contains(text, "auth"), strings.Contains(text, "invalid api key"), strings.Contains(text, "unauthorized"):
		status = http.StatusUnauthorized
	}
	return &upstreamError{StatusCode: status, Body: body}
}

// errorInsideContent catches an upstream that fabricates a successful
// completion whose assistant message is really its own error text — seen on
// TokenHarbor free models, which return the throttle as a normal completion
// with content {"error":{"type":"rate_limit_exceeded",...}} after a 1-2 minute
// queue. Only a single-choice response whose content is a bare error object
// with a throttle/quota/auth signal is promoted; ordinary JSON answers pass.
func errorInsideContent(body []byte) *upstreamError {
	var probe struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &probe); err != nil {
		return nil
	}
	if len(probe.Choices) != 1 {
		return nil
	}
	ue := upstreamErrorFromBody([]byte(probe.Choices[0].Message.Content))
	// Content yang kebetulan berbentuk {"error":...} tanpa sinyal throttle/kuota/
	// auth bisa jadi jawaban model sungguhan; hanya yang sinyalnya jelas dirotasi.
	if ue == nil || ue.StatusCode == http.StatusBadGateway {
		return nil
	}
	return ue
}

// handleStreamResponse pipes SSE chunks from upstream to the client.
func (h *ChatHandler) handleStreamResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translate bool, startTime time.Time, metrics *streamMetrics) error {
	hw := internalproxy.NewHeartbeatWriter(ctx, w, 0)
	defer hw.Close()
	flusher := internalproxy.WriteSSEHeaders(hw)

	if !translate {
		return internalproxy.SSECopy(hw, upstream, flusher, func(chunk []byte) {
			if metrics.TTFT == 0 {
				metrics.TTFT = time.Since(startTime).Milliseconds()
			}
			metrics.ResponseBuf.Write(chunk)
		})
	}

	sessionKey := fmt.Sprintf("stream-%d", time.Now().UnixNano())
	// Seed with requested model so message_start echoes client's model (decolua/9router#3693)
	if reqModel := translator.RequestedModelFromContext(ctx); reqModel != "" {
		translator.SeedStreamState(sessionKey, reqModel)
	}
	defer func() {
		if endChunk := translator.EnsureStreamClosed(sessionKey); len(endChunk) > 0 {
			hw.Write(endChunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
		translator.ClearStreamState(sessionKey)
	}()
	finished := false
	err := internalproxy.ScanStream(upstream, func(chunk []byte) {
		translated, err := translator.TranslateOpenAIToClaudeStreamSession(sessionKey, chunk)
		if err != nil {
			log.Error("stream", "translate error", "error", err)
			return
		}
		if translated == nil {
			return
		}
		if bytes.Contains(translated, []byte("[DONE]")) {
			finished = true
		}
		if metrics.TTFT == 0 {
			metrics.TTFT = time.Since(startTime).Milliseconds()
		}
		metrics.ResponseBuf.Write(translated)
		hw.Write(translated)
		if flusher != nil {
			flusher.Flush()
		}
	})
	// A shutdown abort cuts the stream mid-way. End it with the same terminator
	// a natural finish emits, so the client sees a clean [DONE] instead of a
	// truncated stream (the stall reader already closed the upstream body).
	if shutdown.Fired() && !finished {
		hw.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	// Pull actual accumulated usage (incl. cached tokens) out of the session so
	// the log sees real numbers instead of the fallback estimate.
	if usage := translator.GetStreamUsage(sessionKey); usage != nil {
		translator.SetUsage(ctx, usage)
	}
	return err
}

// isSSEBody reports whether body looks like SSE (forced-stream provider returned event stream for non-stream client).
func isSSEBody(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:")) || bytes.Contains(trimmed, []byte("\ndata:"))
}

// sseToClaudeJSON aggregates OpenAI SSE chunks into a single chat.completion JSON for forced-SSE handling.
// Port of executor/codebuddy.go sseToOpenAIJSON for chatCore forced-SSE fix (decolua/9router#3683).
func sseToClaudeJSON(raw []byte) ([]byte, bool) {
	var chunks []map[string]any
	var streamErr map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(trimmed[5:])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if e, ok := chunk["error"]; ok {
			streamErr, _ = e.(map[string]any)
			continue
		}
		chunks = append(chunks, chunk)
	}
	if streamErr != nil {
		b, _ := json.Marshal(map[string]any{"error": streamErr})
		return b, true
	}
	if len(chunks) == 0 {
		return nil, false
	}
	var contentParts, reasoningParts []string
	toolCallIdx := map[int]map[string]any{}
	var toolCalls []map[string]any
	finishReason := "stop"
	var usage any
	var first map[string]any
	for _, chunk := range chunks {
		if first == nil {
			first = chunk
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if c, ok := delta["content"].(string); ok && c != "" {
			contentParts = append(contentParts, c)
		}
		if r, ok := delta["reasoning_content"].(string); ok && r != "" {
			reasoningParts = append(reasoningParts, r)
		}
		if fr, ok := choice["finish_reason"].(string); ok {
			finishReason = fr
		}
		if u, ok := chunk["usage"]; ok {
			usage = u
		}
		if tcs, ok := delta["tool_calls"].([]any); ok {
			for _, tcAny := range tcs {
				tc, _ := tcAny.(map[string]any)
				idx, _ := tc["index"].(float64)
				entry, ok := toolCallIdx[int(idx)]
				if !ok {
					entry = map[string]any{
						"id":       "",
						"type":     "function",
						"function": map[string]any{"name": "", "arguments": ""},
					}
					toolCallIdx[int(idx)] = entry
					toolCalls = append(toolCalls, entry)
				}
				if id, ok := tc["id"].(string); ok && id != "" {
					entry["id"] = id
				}
				if fn, ok := tc["function"].(map[string]any); ok {
					fEntry, _ := entry["function"].(map[string]any)
					if n, ok := fn["name"].(string); ok && n != "" {
						if existing, _ := fEntry["name"].(string); existing == "" {
							fEntry["name"] = n
						} else if existing != n && !strings.Contains(existing, n) {
							fEntry["name"] = existing + n
						}
					}
					if a, ok := fn["arguments"].(string); ok && a != "" {
						existing, _ := fEntry["arguments"].(string)
						if existing == "" {
							fEntry["arguments"] = a
						} else if existing != a && !strings.Contains(existing, a) {
							if len(a) > 0 && !strings.HasSuffix(existing, a) {
								fEntry["arguments"] = existing + a
							}
						}
					}
				}
			}
		}
	}
	msg := map[string]any{"role": "assistant"}
	content := strings.Join(contentParts, "")
	if content == "" {
		if len(toolCalls) > 0 {
			msg["content"] = nil
		} else {
			msg["content"] = ""
		}
	} else {
		msg["content"] = content
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	if len(reasoningParts) > 0 {
		msg["reasoning_content"] = strings.Join(reasoningParts, "")
	}
	if usage == nil {
		usage = map[string]any{"prompt_tokens": 0, "completion_tokens": len(content) / 4}
	}
	id := "chatcmpl-0"
	if first != nil {
		if v, ok := first["id"].(string); ok && v != "" {
			id = v
		}
	}
	model := "gpt-4o"
	if first != nil {
		if v, ok := first["model"].(string); ok && v != "" {
			model = v
		}
	}
	created := int64(0)
	if first != nil {
		if v, ok := first["created"].(float64); ok {
			created = int64(v)
		}
	}
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
		"usage": usage,
	}
	b, _ := json.Marshal(resp)
	return b, true
}

// handleJSONResponse forwards a non-streaming JSON response.
func (h *ChatHandler) handleJSONResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translate bool, metrics *streamMetrics) error {
	body, err := io.ReadAll(io.LimitReader(upstream, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read upstream response body: %w", err)
	}

	body = translator.UnwrapClineEnvelope(body)

	// A 200 whose body is a bare error object is a failure wearing a success
	// status. Relaying it wrote the upstream's error text out as assistant
	// content and returned nil, which skipped account fallback: TokenHarbor
	// answers a rate-limited account exactly this way (after a long queue), so
	// every request "succeeded" with garbage and never rotated to a healthy key.
	if ue := upstreamErrorFromBody(body); ue != nil {
		return ue
	}
	// TokenHarbor juga membungkus throttle-nya jadi completion normal:
	// choices[0].message.content berisi JSON error. Tanpa ini klien menerima
	// "berhasil" yang isinya pesan rate limit, dan rotasi akun tidak jalan.
	if ue := errorInsideContent(body); ue != nil {
		return ue
	}

	if metrics != nil {
		metrics.ResponseBuf.Write(body)
	}

	if !translate {
		// The !translate path serves both OpenAI bodies (/v1/chat/completions,
		// translateResponse hardcoded false) and Claude bodies (claude/anthropic
		// providers). ParseResponseUsage handles both formats.
		if usage := translator.ParseResponseUsage(body); usage != nil {
			translator.SetUsage(ctx, usage)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}

	// Forced-SSE handling for Claude clients (PR #3683): upstream forced to stream (e.g. Responses-API)
	// but client did stream:false retry. Body is SSE, not JSON. Aggregate SSE chunks into
	// a single OpenAI JSON then translate to Anthropic Message, so Claude Code can parse it.
	if isSSEBody(body) {
		if aggregated, ok := sseToClaudeJSON(body); ok {
			translated, usage, err := translator.TranslateOpenAIToClaude(aggregated)
			if err == nil && usage != nil {
				translator.SetUsage(ctx, usage)
			}
			if err == nil && translated != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write(translated)
				return nil
			}
			// Fall through to normal handling if aggregation/translation fails
		}
	}

	translated, usage, err := translator.TranslateOpenAIToClaude(body)
	if err == nil && usage != nil {
		translator.SetUsage(ctx, usage)
	}
	if err != nil || translated == nil {
		errMsg := "failed to translate upstream response to Claude format"
		if err != nil {
			errMsg = errMsg + ": " + err.Error()
		}
		log.Error("json", "translate error", "msg", errMsg)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(translated)
	return nil
}
