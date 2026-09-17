package chat

import (
	"9router/proxy/internal/log"
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/oauth"
	"9router/proxy/internal/translator"
)

// forwardGeminiNativeRequest handles forwarding for gemini-native providers (antigravity).
func (h *ChatHandler) forwardGeminiNativeRequest(
	ctx context.Context,
	w http.ResponseWriter,
	provider string,
	cfg *providers.ProviderConfig,
	apiKey string,
	connectionID string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
) error {
	// Extract model
	var reqMeta struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqMeta); err != nil {
		log.Warn("gemini", "parse model failed", "error", err)
	}
	modelName := reqMeta.Model
	if modelName == "" {
		modelName = "gemini-3-flash"
	}

	// OAuth refresh for providers using Gemini-native format (antigravity, etc)
	var projectID string
	refreshedKey, pid, err := h.refreshOAuthTokenIfExpired(connectionID, apiKey)
	if err != nil {
		// A failed refresh must not discard what we already know: the caller
		// still has a usable token, and projectID was read from the connection
		// row before the refresh was attempted. Dropping it here turned every
		// refresh failure into "no project ID", which is a different bug.
		log.Warn("gemini", "token refresh error", "conn", connectionID, "error", err)
		if pid != "" {
			projectID = pid
		}
	} else {
		apiKey = refreshedKey
		projectID = pid
	}

	if (provider == "antigravity" || provider == "gemini-cli") && projectID == "" && !projectProbeCached(connectionID) {
		// Identify as a real Antigravity client on the discovery RPCs. The
		// generic API-client UA makes Google omit cloudaicompanionProject even
		// for provisioned accounts, which then looks like "no project".
		discoveryUA := ""
		if cfg != nil {
			discoveryUA = cfg.StaticHeaders["User-Agent"]
		}
		pid, authFailed, noProject := fetchAntigravityProjectID(ctx, h.Client, apiKey, discoveryUA)
		switch {
		case pid != "":
			projectID = pid
			h.storeAntigravityProjectID(connectionID, pid)
		case authFailed:
			// Upstream rejected the token (401/403) — refresh once and retry.
			// A missing/empty project is NOT an auth failure; refreshing there
			// only hammers the onboarding RPCs (the 429 source) without helping.
			log.Info("gemini", "force refresh OAuth token", "conn", connectionID)
			refreshedKey, pid2, err2 := h.forceRefreshOAuthToken(connectionID)
			if err2 == nil && refreshedKey != "" {
				apiKey = refreshedKey
				if pid2 != "" {
					projectID = pid2
					h.storeAntigravityProjectID(connectionID, pid2)
				} else if pid2, _, _ := fetchAntigravityProjectID(ctx, h.Client, apiKey, discoveryUA); pid2 != "" {
					projectID = pid2
					h.storeAntigravityProjectID(connectionID, pid2)
				}
			}
		case noProject:
			// Google definitively said this token has no project. Cache it so
			// client retries stop re-probing the onboarding RPCs.
			cacheProjectMissing(connectionID)
		}
	}

	if projectID == "" {
		// antigravity has no OpenAI-compatible endpoint without a project ID — a
		// bare POST to cloudcode-pa.googleapis.com is a guaranteed 404. Bail with
		// an error so the fallback chain moves on instead of burning a request on
		// a dead lane.
		if provider == "antigravity" {
			return fmt.Errorf("antigravity: no project ID — onboard the account in Antigravity (antigravity.google) then re-login")
		}
		// gemini-cli keeps its OpenAI-style fallback.
		log.Info("gemini", "no projectID, fallback to OpenAI", "provider", provider)
		return h.forwardRequest(ctx, w, cfg, apiKey, body, isStream, translateResponse, metrics)
	}

	resp, err := proxy.ForwardGemini(ctx, h.Client, cfg, apiKey, string(body), isStream, projectID, modelName)
	if err != nil {
		if uErr, ok := err.(*proxy.UpstreamError); ok {
			if uErr.StatusCode == http.StatusConflict || uErr.StatusCode == http.StatusTooManyRequests {
				if dur, ok := extractResetDuration(uErr.Body); ok {
					BlockAntigravityModelUntil(connectionID, modelName, time.Now().UTC().Add(dur))
				}
				HandleAntigravityQuotaError(ctx, h.Client, connectionID, uErr.StatusCode, modelName, apiKey, projectID)
			}
		}
		return fmt.Errorf("ForwardGemini (%s/%s): %w", provider, modelName, err)
	}

	var bodyCloser io.Closer = resp.Body
	defer func() {
		if bodyCloser != nil {
			bodyCloser.Close()
		}
	}()

	// Handle response — antigravity wraps non-streaming response in {"response": {...}}
	// For streaming (SSE), the events are NOT wrapped — pipe directly.
	if projectID != "" && !isStream {
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Error("gemini", "read response body failed", "error", err)
			return fmt.Errorf("read gemini response body: %w", err)
		}
		unwrapped := translator.UnwrapAntigravityResponse(raw)
		return h.handleGeminiNonStream(ctx, w, bytes.NewReader(unwrapped), translateResponse, metrics)
	}
	if isStream {
		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
			return h.handleGeminiNonStream(ctx, w, resp.Body, translateResponse, metrics)
		}
		stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, provider)
		bodyCloser = stallReader
		return h.handleGeminiStream(ctx, w, stallReader, translateResponse, metrics)
	}
	return h.handleGeminiNonStream(ctx, w, resp.Body, translateResponse, metrics)
}

// storeAntigravityProjectID persists a discovered Antigravity project ID on the
// connection so later requests skip the onboarding RPCs entirely.
func (h *ChatHandler) storeAntigravityProjectID(connectionID, pid string) {
	if connectionID == "" || pid == "" {
		return
	}
	go func() {
		if _, err := h.Repo.RawDB().Exec("UPDATE providerConnections SET data = json_set(data, '$.projectId', ?) WHERE id = ?", pid, connectionID); err != nil {
			log.Warn("gemini", "update projectId failed", "conn", connectionID, "error", err)
		}
	}()
}

// refreshOAuthTokenIfExpired refreshes an expired OAuth token for a stored
// connection and returns the (possibly unchanged) token plus its project ID.
//
// The refresh itself is delegated to oauth.RefreshStoredConnection, which holds
// a per-connection lock across the whole read→refresh→persist sequence. That
// serialisation is what stops a dashboard "Test Connection" click from racing a
// live chat request on the same single-use refresh token (which would surface as
// refresh_token_reused and invalidate a healthy account).
//
// A token that is still valid is returned untouched, without a network call.
func (h *ChatHandler) refreshOAuthTokenIfExpired(connectionID, currentToken string) (string, string, error) {
	if connectionID == "" {
		return currentToken, "", nil
	}

	token, projectID, refreshed, err := oauth.RefreshStoredConnection(
		context.Background(), h.Repo, h.Client, connectionID, false,
	)
	if err != nil {
		// A refresh failure must not sink the request: the caller still has the
		// current token, and the upstream round trip will produce the real error.
		log.Warn("oauth", "token refresh failed", "conn", connectionID, "error", err)
		return currentToken, projectID, err
	}
	if !refreshed {
		return currentToken, projectID, nil
	}
	log.Info("oauth", "token refreshed", "conn", connectionID, "project", projectID)
	return token, projectID, nil
}

// forceRefreshOAuthToken refreshes an OAuth token even when it is not locally
// expired. The chat path calls it after an upstream 401/403, where the stored
// expiry looked valid but Google rejected the credential — so the local expiry
// cannot be trusted as the trigger.
//
// Like refreshOAuthTokenIfExpired it goes through oauth.RefreshStoredConnection,
// so the forced refresh is serialised against every other refresh of the same
// connection.
func (h *ChatHandler) forceRefreshOAuthToken(connectionID string) (string, string, error) {
	if connectionID == "" {
		return "", "", nil
	}

	log.Info("oauth", "force refresh", "conn", connectionID)
	token, projectID, _, err := oauth.RefreshStoredConnection(
		context.Background(), h.Repo, h.Client, connectionID, true,
	)
	if err != nil {
		return "", "", err
	}
	return token, projectID, nil
}

// handleGeminiStream processes Gemini stream SSE chunks and translates to OpenAI format.
// The stream drops the first SSE line (model metadata), then translates each content block SSE.
func (h *ChatHandler) handleGeminiStream(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translateResponse bool, metrics *streamMetrics) error {
	hw := proxy.NewHeartbeatWriter(ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)
	geminiState := &translator.GeminiStreamState{}
	start := time.Now()
	// One session per stream so the OpenAI→Claude translation state cannot
	// collide across concurrent requests; always cleared on exit.
	sessionKey := fmt.Sprintf("gemini-stream-%d", time.Now().UnixNano())
	defer func() {
		if translateResponse {
			if endChunk := translator.EnsureStreamClosed(sessionKey); len(endChunk) > 0 {
				hw.Write(endChunk)
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
		translator.ClearStreamState(sessionKey)
	}()

	var totalChunks int
	var totalBytesWritten int
	err := proxy.ScanStream(upstream, func(chunk []byte) {
		chunkStr := strings.TrimSpace(string(chunk))
		if chunkStr == "" || chunkStr == "[DONE]" {
			return
		}

		// Unwrap antigravity envelope if present (SSE chunks may be wrapped as {"response": {...}})
		unwrapped := translator.UnwrapAntigravityResponse(chunk)

		// Translate each Gemini SSE chunk to OpenAI SSE chunk
		openaiChunk, err := translator.TranslateGeminiChunkToOpenAI(unwrapped, geminiState)
		if err != nil {
			log.Error("gemini", "chunk translate error", "error", err)
			return
		}
		if openaiChunk == nil {
			return
		}

		totalChunks++
		if metrics.TTFT == 0 {
			metrics.TTFT = time.Since(start).Milliseconds()
		}
		metrics.ResponseBuf.Write(openaiChunk)

		if translateResponse {
			// openaiChunk may have multiple SSE lines -- split and translate each
			for _, sse := range strings.Split(string(openaiChunk), "\n") {
				sse = strings.TrimSpace(sse)
				if !strings.HasPrefix(sse, "data: ") {
					continue
				}
				claudeChunk, tErr := translator.TranslateOpenAIToClaudeStreamSession(sessionKey, []byte(strings.TrimPrefix(sse, "data: ")))
				if tErr != nil {
					log.Error("gemini", "claude translate error", "error", tErr)
					continue
				}
				if claudeChunk == nil {
					continue
				}
				n, _ := hw.Write(claudeChunk)
				totalBytesWritten += n
			}
		} else {
			n, _ := hw.Write(openaiChunk)
			totalBytesWritten += n
		}
		if flusher != nil {
			flusher.Flush()
		}
	})
	if !translateResponse {
		n, _ := hw.Write([]byte("data: [DONE]\n\n"))
		totalBytesWritten += n
		if flusher != nil {
			flusher.Flush()
		}
	}
	log.Info("gemini", "stream ended", "chunks", totalChunks, "bytesWritten", totalBytesWritten, "duration_ms", time.Since(start).Milliseconds(), "err", err)
	// Pull actual accumulated usage (incl. cached tokens) out of the session so
	// the log sees real numbers instead of the fallback estimate.
	if translateResponse {
		if usage := translator.GetStreamUsage(sessionKey); usage != nil {
			translator.SetUsage(ctx, usage)
		}
	} else if geminiState.Usage != nil {
		translator.SetUsage(ctx, geminiState.Usage)
	}
	return err
}

// handleGeminiNonStream translates a Gemini non-stream response to OpenAI format,
// and then to Claude format if translateResponse is true.
func (h *ChatHandler) handleGeminiNonStream(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translateResp bool, metrics *streamMetrics) error {
	body, err := io.ReadAll(upstream)
	if err != nil {
		return fmt.Errorf("read gemini response body: %w", err)
	}

	if metrics != nil {
		metrics.ResponseBuf.Write(body)
	}

	// Use the full translator which handles tool calls, thinking, etc.
	openaiResp, usage, err := translator.TranslateGeminiResponseToOpenAI(body)
	if err == nil && usage != nil {
		translator.SetUsage(ctx, usage)
	}
	if err != nil {
		// Fallback: write raw body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}

	if translateResp {
		// OpenAI → Claude (Anthropic) format
		claudeResp, claudeUsage, tErr := translator.TranslateOpenAIToClaude(openaiResp)
		if tErr == nil && claudeUsage != nil {
			translator.SetUsage(ctx, claudeUsage)
		}
		if tErr != nil || claudeResp == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(openaiResp)
			return nil
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(claudeResp)
		return nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(openaiResp)
	return nil
}
