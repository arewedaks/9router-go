package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/tokensaver"
	"9router/proxy/internal/tracing"
	"9router/proxy/internal/translator"
	"9router/proxy/internal/usagetracker"
)

// StatusClientClosedRequest is the canonical HTTP status for client connection aborts (nginx 499).
const StatusClientClosedRequest = 499

// handleAccountFallback attempts to forward a request with automatic account fallback.
func (h *ChatHandler) handleAccountFallback(
	ctx context.Context,
	w http.ResponseWriter,
	provider string,
	model string,
	pinnedConnectionID string,
	body []byte,
	isStream bool,
	translateResponse bool,
	endpoint string,
) error {
	body = repairToolCallIDsInJSON(body)

	// Provider-level breaker first, before any path that dials upstream — the
	// pinned-connection path below returns early, and a check placed after it
	// would let a pinned request bypass an open circuit entirely.
	if !providerBreakerAllows(provider) {
		return fmt.Errorf("provider %s is temporarily unavailable (circuit open)", provider)
	}

	if pinnedConnectionID != "" {
		connObj, connData, err := h.getBestConnection(provider, pinnedConnectionID, nil, model)
		if err != nil {
			return fmt.Errorf("pinned connection %s: %w", pinnedConnectionID, err)
		}
		log.Debug("fallback", "pinned", "pinnedConn", pinnedConnectionID, "connObj", connObj.ID)
		return h.tryForwardWithConnection(ctx, w, provider, model, connObj.ID, connData, body, isStream, translateResponse, endpoint)
	}

	if !h.Repo.IsProviderAvailable(provider, model) {
		log.Warn("fallback", "skip unhealthy", "provider", provider, "model", model)
		return fmt.Errorf("provider %s/%s is unhealthy", provider, model)
	}

	allConns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil || len(allConns) == 0 {
		if cfg, ok := providers.KnownProviders[provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			apiKey := cfg.DefaultAPIKey
			if apiKey == "" {
				apiKey = "public"
			}
			// Proxy configuration for a keyless provider lives only here, so it
			// has to be resolved with the connection rather than left empty.
			return h.tryForwardWithConnection(ctx, w, provider, model, "default", h.NewNoAuthConnectionData(provider, apiKey), body, isStream, translateResponse, endpoint)
		}
		return fmt.Errorf("no active connections for provider: %s", provider)
	}

	// Apply the provider's account routing strategy. Two settings feed this:
	// fallbackStrategy spreads requests across the provider's own ACCOUNTS (the
	// Round Robin toggle on the provider page), rotateStrategy across PROXY
	// POOLS. Either one being on is a reason to reorder, and they are
	// independent — checking only rotateStrategy is why the toggle did nothing.
	if len(allConns) > 1 && h.Repo != nil {
		if settings, sErr := h.Repo.GetSettings(); sErr == nil && settings != nil && settings.ProviderStrategies != nil {
			if strat, ok := settings.ProviderStrategies[provider]; ok {
				poolRotation := strat.RotateStrategy != "" && strat.RotateStrategy != "none"
				if poolRotation || strat.WantsAccountRoundRobin() {
					allConns = h.applyConnectionStrategy(provider, allConns, strat)
				}
			}
		}
	}

	var excludeIDs []string
	var lastErr error
	for _, c := range allConns {
		if slices.Contains(excludeIDs, c.ID) {
			continue
		}
		connObj, connData, err := h.getBestConnection(provider, c.ID, nil, model)
		if err != nil || connObj == nil {
			continue
		}
		apiKey := extractAPIKey(connData)
		if apiKey == "" {
			providerCfg, pErr := h.getProviderConfig(provider, connData)
			if pErr == nil && providerCfg.DefaultAPIKey != "" {
				apiKey = providerCfg.DefaultAPIKey
			} else {
				continue
			}
		}
		log.Debug("fallback", "connection", "conn", c.ID, "connObj", connObj.ID)
		if err := h.tryForwardWithConnection(ctx, w, provider, model, c.ID, connData, body, isStream, translateResponse, endpoint); err == nil {
			return nil
		} else {
			lastErr = err
		}
		var ue *upstreamError
		if errors.As(lastErr, &ue) && providers.RetryableStatusCodes[ue.StatusCode] {
			// Extract error text from upstream body for classification
			errorText := extractErrorText(ue.Body)
			// Get current backoff level from this connection
			currentBackoffLevel := h.Repo.GetConnectionBackoffLevel(connObj.ID)
			// Classify error to get dynamic cooldown
			classification := providers.ClassifyError(ue.StatusCode, errorText, currentBackoffLevel)
			cooldownSec := int((classification.CooldownMs + 999) / 1000) // ceil to seconds
			if dur, ok := extractResetDuration(ue.Body); ok {
				cooldownSec = int(dur.Seconds())
			}
			errMsg := errorText
			if errMsg == "" {
				errMsg = fmt.Sprintf("%d upstream error", ue.StatusCode)
			}
			lockKey := canonicalLockModel(provider, model)
			h.Repo.LockConnectionModel(connObj.ID, lockKey, cooldownSec, classification.NewBackoffLevel)
			if lockKey != model {
				_ = h.Repo.LockConnectionModel(connObj.ID, model, cooldownSec, classification.NewBackoffLevel)
			}
			// Hold this account's concurrency gate for the cooldown so queued
			// requests wait instead of adding to the throttle that caused it.
			if ue.StatusCode == http.StatusTooManyRequests {
				blockAccountAfter429(provider, connObj.ID, connData, time.Duration(cooldownSec)*time.Second)
			}
			log.Warn("fallback", "connection locked", "conn", connObj.ID, "provider", provider, "model", model, "lockKey", lockKey, "status", ue.StatusCode, "cooldown_s", cooldownSec)
			excludeIDs = append(excludeIDs, c.ID)
			continue
		}
		return lastErr
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("no available connections for provider: %s", provider)
}

// tryForwardWithConnection attempts a single upstream request using the given connection data.
// isAnthropicUpstream reports whether the request is headed to Anthropic's
// native Messages API (as opposed to an anthropic-compatible custom node).
func isAnthropicUpstream(provider string, cfg *providers.ProviderConfig) bool {
	if provider != "claude" && provider != "anthropic" {
		return false
	}
	if cfg == nil {
		return false
	}
	targetURL := cfg.BaseURL
	if cfg.StaticHeaders != nil {
		if relayTarget, ok := cfg.StaticHeaders["x-relay-target"]; ok && relayTarget != "" {
			targetURL = relayTarget + cfg.StaticHeaders["x-relay-path"]
		}
	}
	return targetURL == "https://api.anthropic.com/v1/messages" ||
		strings.HasPrefix(targetURL, "https://api.anthropic.com/v1/messages?")
}

// upstreamClaudeWanted reports whether the upstream returns Claude Messages
// format, so the response must be translated back.
//
// True for Anthropic's own host (unless the client already speaks Messages) and
// for any provider flagged FormatClaude — a third-party gateway fronting Claude
// models, whose body conversion EnsureClaudeMessages performs. The credential
// handling for Anthropic OAuth is deliberately NOT extended to those providers:
// they are not Anthropic, so Claude-Code cloaking would only send headers they
// do not expect.
func upstreamClaudeWanted(isAnthropic bool, cfg *providers.ProviderConfig, claudeNative bool) bool {
	if claudeNative {
		return false
	}
	return isAnthropic || (cfg != nil && cfg.IsClaudeFormat())
}

func appendBetaQuery(u string) string {
	if strings.Contains(u, "beta=true") {
		return u
	}
	if strings.Contains(u, "?") {
		return u + "&beta=true"
	}
	return u + "?beta=true"
}

func (h *ChatHandler) tryForwardWithConnection(
	ctx context.Context,
	w http.ResponseWriter,
	provider string,
	model string,
	connectionID string,
	connData *ConnectionData,
	body []byte,
	isStream bool,
	translateResponse bool,
	endpoint string,
) error {
	ctx = translator.WithUsageCapture(ctx)

	// Cap concurrent requests to this account before doing any work. Released
	// by the deferred call below, which runs on every return path.
	releaseSlot := acquireAccountSlot(ctx, provider, connectionID, connData)
	defer releaseSlot()

	providerCfg, err := h.getProviderConfig(provider, connData)
	if err != nil {
		return fmt.Errorf("get config for %s/%s: %w", provider, model, err)
	}
	// anthropic-compatible nodes fronting a Claude model need Anthropic-Beta flags (parity #3797)
	if (provider == "claude" || strings.HasPrefix(provider, "anthropic-compatible-") || strings.HasPrefix(provider, "anthropic")) && strings.HasPrefix(model, "claude-") {
		if providerCfg.StaticHeaders == nil {
			providerCfg.StaticHeaders = make(map[string]string)
		}
		if _, ok := providerCfg.StaticHeaders["Anthropic-Beta"]; !ok {
			providerCfg.StaticHeaders["Anthropic-Beta"] = "prompt-caching-scope-2026-01-05, context-management-2025-06-27"
		}
	}

	// OAuth connections (e.g. Claude subscription logins) must authenticate
	// with "Authorization: Bearer" against the beta endpoint, matching the
	// Next.js dashboard (open-sse/executors/default.js + registry/claude.js).
	// Sending the OAuth access token via x-api-key makes api.anthropic.com
	// return 401 "API key is invalid" even after a successful token refresh.
	// A connection is OAuth when it only carries an accessToken (no apiKey).
	isAnthropic := isAnthropicUpstream(provider, providerCfg)
	// OAuth = connection carries only an accessToken, OR the token itself is
	// an Anthropic OAuth token (some connections store sk-ant-oat in APIKey).
	isOAuth := isAnthropic && connData != nil &&
		((connData.APIKey == "" && connData.AccessToken != "") ||
			strings.Contains(connData.APIKey, "sk-ant-oat"))
	if isOAuth {
		providerCfg.AuthHeader = "Authorization"
		providerCfg.AuthScheme = "bearer"
		if providerCfg.StaticHeaders != nil && providerCfg.StaticHeaders["x-relay-path"] != "" {
			providerCfg.StaticHeaders["x-relay-path"] = appendBetaQuery(providerCfg.StaticHeaders["x-relay-path"])
		} else {
			providerCfg.BaseURL = appendBetaQuery(providerCfg.BaseURL)
		}
	}

	apiKey := extractAPIKey(connData)
	if apiKey == "" {
		if providerCfg.DefaultAPIKey != "" {
			apiKey = providerCfg.DefaultAPIKey
		} else {
			return &upstreamError{StatusCode: http.StatusUnauthorized, Body: []byte(`{"error":{"message":"no API key found","type":"auth_error","code":401}}`)}
		}
	}

	if connectionID != "" {
		rekey, _, err := h.refreshOAuthTokenIfExpired(connectionID, apiKey)
		if err == nil {
			apiKey = rekey
		} else {
			log.Warn("fallback", "OAuth token refresh error", "conn", connectionID, "error", err)
		}
	}
	apiKey = NormalizeProviderToken(provider, apiKey)

	// Token savers + provider-format normalization:
	// - claudeNative: the CLIENT body is already Claude Messages format, so it
	//   stays as-is and savers inject into the top-level "system".
	// - an OpenAI-format body headed to a Messages upstream: convert to a
	//   spec-compliant Claude payload (top-level system, tools, merged roles).
	//
	// claudeNative needs a Messages-speaking upstream as well as a Messages
	// endpoint: chat.go converts /v1/messages requests for OpenAI-compatible
	// providers to OpenAI format before reaching here, so the endpoint alone
	// would wrongly inject a top-level "system" that upstream ignores.
	claudeNative := (isAnthropic || providerCfg.IsClaudeFormat()) && (endpoint == "/v1/v1/messages" || endpoint == "/v1/messages")
	pipedBody := h.applyTokenSavers(ctx, body, claudeNative, model)
	var claudeToolMap map[string]string
	// A Claude-format provider needs the same Messages-shaped body as Anthropic's
	// own host, but none of the Claude-Code credential handling below (it is not
	// Anthropic, so cloaking would only add headers it does not expect).
	if providerCfg.IsClaudeFormat() && !claudeNative {
		pipedBody = executor.EnsureClaudeMessages(pipedBody, model)
	}
	if isAnthropic {
		if !claudeNative {
			// Raw OpenAI-format body would be invalid at the Messages API:
			// convert to a spec-compliant Claude payload (top-level system,
			// tools, merged roles) — what the dashboard does server-side.
			pipedBody = executor.EnsureClaudeMessages(pipedBody, model)
		}
		// OAuth connections (or sk-ant-oat tokens) require Claude-Code-shaped requests:
		// billing-header system block + metadata.user_id + cloaked tools,
		// or the API 429s (anti-abuse fingerprinting).
		if isOAuth || strings.Contains(apiKey, "sk-ant-oat") {
			pipedBody = applyClaudeCloaking(pipedBody, apiKey, handlerutil.GetSessionID(ctx))
			var reqMap map[string]any
			if err := json.Unmarshal(pipedBody, &reqMap); err == nil {
				claudeToolMap = cloakClaudeTools(reqMap)
				if out, err := json.Marshal(reqMap); err == nil {
					pipedBody = out
				}
			}
		}
	}
	// Sanitize tool schemas for all OpenAI-compatible providers (opencode, gemini-openai, etc.)
	// Fixes misplaced `required` inside `properties` and missing `items` for arrays.
	if sanitized, err := translator.SanitizeOpenAITools(pipedBody); err == nil && sanitized != nil && string(sanitized) != string(pipedBody) {
		log.Debug("fallback", "sanitized tools", "provider", provider, "model", model, "conn", connectionID[:min(8, len(connectionID))], "beforeBytes", len(pipedBody), "afterBytes", len(sanitized))
		pipedBody = sanitized
	} else if err != nil {
		log.Warn("fallback", "sanitize failed", "provider", provider, "model", model, "error", err)
	}
	start := time.Now()
	metrics := &streamMetrics{}
	var fwdErr error

	usagetracker.GetTracker().TrackPending(model, provider, connectionID, true, false)
	defer func() {
		hasErr := fwdErr != nil
		usagetracker.GetTracker().TrackPending(model, provider, connectionID, false, hasErr)
	}()

	httpClient := h.getClientForConnection(connData)
	sessionID := handlerutil.GetSessionID(ctx)

	if exec := executor.Get(provider); exec != nil {
		fwdErr = exec(w, &executor.Request{
			Ctx:            ctx,
			Client:         httpClient,
			Config:         providerCfg,
			APIKey:         apiKey,
			Body:           pipedBody,
			IsStream:       isStream,
			TranslateResp:  translateResponse,
			ConnectionID:   connectionID,
			SessionID:      sessionID,
			ToolNameMap:    claudeToolMap,
			UpstreamClaude: upstreamClaudeWanted(isAnthropic, providerCfg, claudeNative),
			ResponseBuf:    &metrics.ResponseBuf,
			StartTime:      start,
			TTFT:           &metrics.TTFT,
		})
	} else if providerCfg.IsGeminiNative() {
		fwdErr = h.forwardGeminiNativeRequest(ctx, w, provider, providerCfg, apiKey, connectionID, pipedBody, isStream, translateResponse, metrics)
	} else {
		fwdErr = h.forwardRequest(ctx, w, providerCfg, apiKey, pipedBody, isStream, translateResponse, metrics)
	}

	var ue *upstreamError
	if errors.As(fwdErr, &ue) && ue.StatusCode == http.StatusUnauthorized && connectionID != "" {
		refreshedKey, _, rErr := h.forceRefreshOAuthToken(connectionID)
		if rErr == nil && refreshedKey != "" && refreshedKey != apiKey {
			log.Info("fallback", "reactive 401 token refresh success, retrying request", "conn", connectionID)
			apiKey = NormalizeProviderToken(provider, refreshedKey)
			if exec := executor.Get(provider); exec != nil {
				fwdErr = exec(w, &executor.Request{
					Ctx:            ctx,
					Client:         httpClient,
					Config:         providerCfg,
					APIKey:         apiKey,
					Body:           pipedBody,
					IsStream:       isStream,
					TranslateResp:  translateResponse,
					ConnectionID:   connectionID,
					SessionID:      sessionID,
					ToolNameMap:    claudeToolMap,
					UpstreamClaude: upstreamClaudeWanted(isAnthropic, providerCfg, claudeNative),
					ResponseBuf:    &metrics.ResponseBuf,
					StartTime:      start,
					TTFT:           &metrics.TTFT,
				})
			} else if providerCfg.IsGeminiNative() {
				fwdErr = h.forwardGeminiNativeRequest(ctx, w, provider, providerCfg, apiKey, connectionID, pipedBody, isStream, translateResponse, metrics)
			} else {
				fwdErr = h.forwardRequest(ctx, w, providerCfg, apiKey, pipedBody, isStream, translateResponse, metrics)
			}
		}
	}

	latencyMs := time.Since(start).Milliseconds()

	// Lightweight request trace for /debug/traces (provider/model latency).
	completed := fwdErr == nil
	if !completed && isClientCanceled(ctx, fwdErr) && metrics != nil && metrics.ResponseBuf.Len() > 0 {
		completed = true
	}

	status := "error"
	if completed {
		status = strconv.Itoa(http.StatusOK)
	} else if isClientCanceled(ctx, fwdErr) {
		status = strconv.Itoa(StatusClientClosedRequest)
	} else if ue, ok := fwdErr.(*upstreamError); ok && ue.StatusCode > 0 {
		status = strconv.Itoa(ue.StatusCode)
	}
	tracing.Record(tracing.Span{
		Provider:   provider,
		Model:      model,
		Status:     status,
		DurationMs: latencyMs,
		TTFTMs:     metrics.TTFT,
	})

	if completed {
		// Clear any existing model lock on success (matching Next.js clearAccountError)
		lockKey := canonicalLockModel(provider, model)
		if unlockErr := h.Repo.UnlockConnectionModel(connectionID, lockKey); unlockErr != nil {
			log.Warn("fallback", "unlock failed", "provider", provider, "model", lockKey, "error", unlockErr)
		}
		if lockKey != model {
			_ = h.Repo.UnlockConnectionModel(connectionID, model)
		}
		usage := translator.GetAndClearUsage(ctx)
		if usage == nil {
			usage = &translator.OpenAIUsage{}
		}
		logInfo := &UsageLogInfo{
			Provider:     provider,
			Model:        model,
			ConnectionID: connectionID,
			APIKey:       apiKey,
			Endpoint:     endpoint,
		}
		h.logUsage(logInfo, usage, latencyMs, body, metrics)
		fwdErr = nil
		recordProviderOutcome(provider, 200, false)
	} else {
		var ue *upstreamError
		statusCode := 0
		if errors.As(fwdErr, &ue) {
			statusCode = ue.StatusCode
		}
		if isClientCanceled(ctx, fwdErr) {
			// The client hung up, so the provider never got to answer. Counting
			// this would let an impatient client open the breaker.
			log.Info("fallback", "client canceled request", "provider", provider, "model", model, "conn", connectionID)
		} else if projectProbeCached(connectionID) {
			log.Debug("fallback", "upstream skipped (cached no-project)", "provider", provider, "model", model, "conn", connectionID, "error", fwdErr)
			recordProviderOutcome(provider, statusCode, true)
		} else {
			log.Warn("fallback", "upstream failed", "provider", provider, "model", model, "conn", connectionID, "status", statusCode, "error", fwdErr)
			recordProviderOutcome(provider, statusCode, statusCode == 0)
		}
	}
	return fwdErr
}
func isClientCanceled(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	errStr := err.Error()
	return strings.Contains(errStr, "context canceled") || strings.Contains(errStr, "client closed")
}

// applyTokenSavers runs the token-saving pipeline on the request body:
// prompt-injection scan, RTK compression, Headroom compression, then the
// Caveman and Ponytail system-prompt injections.
//
// claudeNative indicates the body is in Claude Messages format (endpoint
// /v1/messages): system prompts must go to the top-level "system" field —
// a role:"system" message is rejected by the Anthropic API.
//
// A per-request X-9Router-Token-Saver: off (carried on ctx) skips everything.
// false from compress/inject means nothing changed (or unparseable) — keep original, not a failure.
func (h *ChatHandler) applyTokenSavers(ctx context.Context, body []byte, claudeNative bool, model string) []byte {
	if tokensaver.BypassRequested(ctx) {
		return body
	}

	// Prompt-injection guard: tag (never block) flagged user content. Early
	// detection here means operators can see abuse before it reaches upstream.
	// Toggle via settings.injectionGuardEnabled (off bypasses the scan).
	if h.TokenSaver.InjectionGuardEnabled() {
		if inj := tokensaver.DetectInjection(body); inj.Flagged {
			log.Warn("guard", "prompt injection flagged", "reasons", inj.Reasons, "messageId", inj.MessageID)
		}
	}
	out := body
	if h.TokenSaver.RTKEnabled() {
		if next, did := tokensaver.CompressMessages(out); did {
			out = next
		}
	}
	// Headroom runs after RTK and before the prompt injections, matching the
	// reference order: it rewrites the conversation, while the injections append
	// to it. Compressing after injection would let the proxy shorten the very
	// style prompt the operator asked for.
	if h.TokenSaver.HeadroomEnabled() {
		out = h.compressWithHeadroom(ctx, out, claudeNative, model)
	}
	inject := tokensaver.InjectSystemPrompt
	if claudeNative {
		inject = tokensaver.InjectSystemPromptClaude
	}
	if h.TokenSaver.CavemanEnabled() {
		prompt := tokensaver.GetCavemanPrompt(h.TokenSaver.CavemanLevel())
		if next, did := inject(out, prompt); did {
			out = next
		}
	}
	if h.TokenSaver.PonytailEnabled() {
		prompt := tokensaver.GetPonytailPrompt(h.TokenSaver.PonytailLevel())
		if next, did := inject(out, prompt); did {
			out = next
		}
	}
	return out
}

// compressWithHeadroom sends the conversation to the external Headroom proxy
// and returns the compressed body, or the original when the proxy is absent,
// slow, or answers something unexpected.
//
// Fail-open on every error path: Headroom is an optional sidecar, so an
// unreachable proxy must cost a request its compression, not its success.
//
// Only the OpenAI `messages` shape is sent directly. A Claude-format body is
// translated to that shape and back, which is what the reference does too —
// /v1/compress only understands chat messages, and handing it a Messages
// payload would have it rewrite tool_use blocks it cannot see.
func (h *ChatHandler) compressWithHeadroom(ctx context.Context, body []byte, claudeNative bool, model string) []byte {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body
	}

	// Responses-API bodies hold items (reasoning, function_call) that are not
	// chat messages. Rewriting them without a round-trip translator corrupts the
	// payload, so this shape is left alone rather than half-converted.
	if _, isResponses := parsed["input"].([]any); isResponses && parsed["messages"] == nil {
		log.Debug("headroom", "skipped: responses-API input is not chat messages")
		return body
	}

	working := body
	if claudeNative {
		converted, err := translator.TranslateClaudeToOpenAI(body)
		if err != nil {
			log.Warn("headroom", "skipped: Claude body did not translate", "error", err)
			return body
		}
		working = converted
	}

	var msgs map[string]any
	if err := json.Unmarshal(working, &msgs); err != nil {
		return body
	}
	key, ok := tokensaver.MessageKey(msgs)
	if !ok {
		log.Debug("headroom", "skipped: no messages[] to compress")
		return body
	}
	original, _ := msgs[key].([]any)

	compressed, stats, err := tokensaver.CompressWithHeadroom(ctx, h.Client, original, tokensaver.HeadroomOptions{
		BaseURL:              h.TokenSaver.HeadroomURL(),
		Model:                model,
		TimeoutMs:            h.TokenSaver.HeadroomTimeoutMs(),
		CompressUserMessages: h.TokenSaver.HeadroomCompressUserMessages(),
	})
	if err != nil {
		log.Warn("headroom", "skipped", "reason", err, "url", h.TokenSaver.HeadroomURL())
		return body
	}
	log.Info("headroom", tokensaver.FormatHeadroomLog(stats))

	msgs[key] = compressed
	rewritten, err := json.Marshal(msgs)
	if err != nil {
		return body
	}
	if !claudeNative {
		return rewritten
	}
	// Back to Messages format, so the Claude-native upstream still receives the
	// shape it expects.
	return executor.EnsureClaudeMessages(rewritten, model)
}

// extractErrorText attempts to extract a human-readable error message from an upstream error JSON body.
// Returns "" when the body isn't parseable or has no message field.
func extractErrorText(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
		Message string `json:"message"`
		// CodeBuddy answers with {"code":N,"msg":"..."} rather than the OpenAI
		// {"error":{"message":"..."}} shape. Without this field every CodeBuddy
		// failure classified as empty text, so the quota-exhaustion rules could
		// never match and a spent budget was treated as a 2-second throttle.
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		var parts []string
		if parsed.Error.Message != "" {
			parts = append(parts, parsed.Error.Message)
		} else if parsed.Message != "" {
			parts = append(parts, parsed.Message)
		} else if parsed.Msg != "" {
			parts = append(parts, parsed.Msg)
		}
		if parsed.Error.Status != "" {
			parts = append(parts, parsed.Error.Status)
		}
		for _, d := range parsed.Error.Details {
			if d.Reason != "" {
				parts = append(parts, d.Reason)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	trimmed := bytes.TrimSpace(body)
	if bytes.HasPrefix(trimmed, []byte("<!DOCTYPE html")) || bytes.HasPrefix(trimmed, []byte("<html")) {
		lower := strings.ToLower(string(trimmed))
		if strings.Contains(lower, "cloudflare") || strings.Contains(lower, "attention required") {
			return "Cloudflare WAF challenge (Attention Required!): check User-Agent or network proxy"
		}
		if titleStart := strings.Index(lower, "<title>"); titleStart != -1 {
			titleEnd := strings.Index(lower[titleStart:], "</title>")
			if titleEnd != -1 {
				return "upstream returned HTML: " + strings.TrimSpace(string(trimmed[titleStart+7:titleStart+titleEnd]))
			}
		}
		return "upstream returned HTML error page"
	}
	return ""
}

var resetsInRegex = regexp.MustCompile(`(?i)resets?\s+in\s+([0-9hms\.]+)`)

// resetAtRegex matches an ABSOLUTE reset stamp, which is how CodeBuddy reports
// a rate limit: "your usage will reset at 2026-09-23 10:37:27 UTC+8". The
// offset may be "UTC+8", "UTC+08:00" or absent, in which case the time is
// treated as UTC.
var resetAtRegex = regexp.MustCompile(`(?i)reset\s+at\s+(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}:\d{2})(?:\s*UTC\s*([+-]\d{1,2}(?::\d{2})?))?`)

const (
	minResetCooldown = 5 * time.Second
	maxResetCooldown = 2 * time.Hour
)

// parseResetTimestamp converts a matched absolute stamp into a duration from
// now, clamped like every other reset source so a clock skew cannot produce a
// negative lock.
func parseResetTimestamp(matches []string) (time.Duration, bool) {
	if len(matches) < 3 {
		return 0, false
	}
	stamp := matches[1] + " " + matches[2]

	loc := time.UTC
	if len(matches) > 3 && matches[3] != "" {
		if offsetSec, ok := parseUTCOffset(matches[3]); ok {
			loc = time.FixedZone("", offsetSec)
		}
	}

	resetAt, err := time.ParseInLocation("2006-01-02 15:04:05", stamp, loc)
	if err != nil {
		return 0, false
	}
	return clampResetDuration(time.Until(resetAt)), true
}

// parseUTCOffset converts the offset out of a "UTC+8" / "UTC+08:00" suffix into
// seconds. The hour is deliberately not zero-padded: Tencent writes "+8", and
// handing that to time.Parse as "-07:00" fails, which silently left the stamp
// read as UTC and the cooldown at the 2-hour ceiling instead of the real reset.
func parseUTCOffset(offset string) (int, bool) {
	if len(offset) < 2 {
		return 0, false
	}
	sign := 1
	switch offset[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, false
	}

	hourStr, minStr, hasMin := strings.Cut(offset[1:], ":")
	hours, err := strconv.Atoi(hourStr)
	if err != nil {
		return 0, false
	}
	minutes := 0
	if hasMin {
		if minutes, err = strconv.Atoi(minStr); err != nil {
			return 0, false
		}
	}
	return sign * (hours*3600 + minutes*60), true
}

// extractResetDuration attempts to extract a structured reset duration from an error payload.
// It parses:
// 1. Google RPC ErrorInfo metadata: quotaResetDelay ("1h12m28.109534319s")
// 2. Text message patterns: "Resets in 1h12m28s."
// 3. Clamps duration between 5 seconds and 2 hours to prevent deadlock / indefinite lockout.
func extractResetDuration(body []byte) (time.Duration, bool) {
	if len(body) == 0 {
		return 0, false
	}

	// 1. Google RPC error details
	var rpcErr struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcErr); err == nil {
		for _, d := range rpcErr.Error.Details {
			if delayStr, ok := d.Metadata["quotaResetDelay"]; ok && delayStr != "" {
				delayStr = strings.TrimRight(delayStr, ".")
				if dur, err := time.ParseDuration(delayStr); err == nil && dur > 0 {
					return clampResetDuration(dur), true
				}
			}
		}
		if rpcErr.Error.Message != "" {
			if matches := resetsInRegex.FindStringSubmatch(rpcErr.Error.Message); len(matches) > 1 {
				raw := strings.TrimRight(matches[1], ".")
				if dur, err := time.ParseDuration(raw); err == nil && dur > 0 {
					return clampResetDuration(dur), true
				}
			}
		}
	}

	// 2. Absolute reset stamp ("reset at 2026-09-23 10:37:27 UTC+8"), the form
	//    CodeBuddy uses. Checked before the relative pattern because a body may
	//    carry both and the absolute one is authoritative.
	if matches := resetAtRegex.FindStringSubmatch(string(body)); matches != nil {
		if dur, ok := parseResetTimestamp(matches); ok {
			return dur, true
		}
	}

	// 3. Fallback regex on raw body string
	if matches := resetsInRegex.FindSubmatch(body); len(matches) > 1 {
		raw := strings.TrimRight(string(matches[1]), ".")
		if dur, err := time.ParseDuration(raw); err == nil && dur > 0 {
			return clampResetDuration(dur), true
		}
	}

	return 0, false
}

func clampResetDuration(dur time.Duration) time.Duration {
	if dur < minResetCooldown {
		return minResetCooldown
	}
	if dur > maxResetCooldown {
		return maxResetCooldown
	}
	return dur
}

// extractRetryAfter extracts a retryAfter ISO timestamp from an upstream error JSON body.
// Checks quotaResetDelay, "Resets in X", or common field names: retryAfter, retry_after, resetsAt, resets_at.
// Returns "" when not found or not parseable.
func extractRetryAfter(body []byte) string {
	if dur, ok := extractResetDuration(body); ok {
		return time.Now().UTC().Add(dur).Format(time.RFC3339)
	}
	var parsed struct {
		RetryAfter string `json:"retryAfter"`
		RetryAlt   string `json:"retry_after"`
		ResetsAt   string `json:"resetsAt"`
		ResetsAlt  string `json:"resets_at"`
		Error      struct {
			RetryAfter string `json:"retryAfter"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if parsed.RetryAfter != "" {
		return parsed.RetryAfter
	}
	if parsed.RetryAlt != "" {
		return parsed.RetryAlt
	}
	if parsed.ResetsAt != "" {
		return parsed.ResetsAt
	}
	if parsed.ResetsAlt != "" {
		return parsed.ResetsAlt
	}
	if parsed.Error.RetryAfter != "" {
		return parsed.Error.RetryAfter
	}
	return ""
}

// formatRetryAfter formats an ISO timestamp into a human-readable "reset after Xm Ys" string.
// Returns "" when the timestamp is empty, unparseable, or in the past.
func formatRetryAfter(isoTimestamp string) string {
	if isoTimestamp == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, isoTimestamp)
	if err != nil {
		return ""
	}
	diffMs := time.Until(parsed)
	if diffMs <= 0 {
		return "reset after 0s"
	}
	totalSec := int((diffMs + 999) / 1000) // ceil
	h := totalSec / 3600
	m := (totalSec % 3600) / 60
	s := totalSec % 60
	var parts []string
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%dh", h))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("%dm", m))
	}
	if s > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", s))
	}
	return "reset after " + strings.Join(parts, " ")
}
