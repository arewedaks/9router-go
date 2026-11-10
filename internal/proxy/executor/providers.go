package executor

import (
	"9router/proxy/internal/log"
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// readPeekedBody reads the first chunk so ParseBareErrorBody can check it; a
// replayable reader then serves the same bytes to the stream. The buffered
// reader holds them so the executor can swap resp.Body for the peeked one and
// lose nothing.
func readPeekedBody(body io.ReadCloser) ([]byte, io.ReadCloser) {
	buf := make([]byte, 8192)
	n, err := body.Read(buf)
	if n == 0 && err != nil {
		return nil, body
	}
	return buf[:n], &peekedReadCloser{Reader: bufio.NewReaderSize(&peekedSrc{data: buf[:n], src: body}, 8192), Closer: body}
}

type peekedSrc struct {
	data []byte
	src  io.Reader
	off  int
}

func (p *peekedSrc) Read(b []byte) (int, error) {
	if p.off < len(p.data) {
		n := copy(b, p.data[p.off:])
		p.off += n
		if p.off < len(p.data) {
			return n, nil
		}
		m, err := p.src.Read(b[n:])
		return n + m, err
	}
	return p.src.Read(b)
}

type peekedReadCloser struct {
	io.Reader
	io.Closer
}

func readPeekedBare(body io.ReadCloser) io.ReadCloser {
	return &peekedReadCloser{Reader: body, Closer: body}
}

// ---- Provider-specific executors ----

// ForwardGrokCLI forwards to grok-cli using Responses API format.
// Transforms Chat Completions body → Responses API body before forwarding.
func ForwardGrokCLI(w http.ResponseWriter, req *Request) error {
	transformedBody, _, err := buildResponsesBody(req.Body)
	if err != nil {
		return fmt.Errorf("transform body: %w", err)
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardGrokCLI(ctx, req.Client, req.Config, req.APIKey, transformedBody, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardGrokCLI: %w", err)
	}
	defer resp.Body.Close()
	return handleCodexStream(w, req, resp.Body)
}

// ForwardCodex forwards to codex using Responses API format.
// Transforms Chat Completions body → Responses API body before forwarding.
//
// When compact is set the request is addressed to the provider's compaction
// endpoint (base URL + "/compact"), which is how a client asks the model to
// summarise a conversation that no longer fits. Mirrors the reference
// implementation, where the Codex executor appends "/compact" per request
// rather than carrying a second base URL.
func ForwardCodex(w http.ResponseWriter, req *Request) error {
	transformedBody, _, err := buildResponsesBody(req.Body)
	if err != nil {
		return fmt.Errorf("transform body: %w", err)
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := req.Config
	if req.IsCompact {
		// Copy the config so the shared provider entry is not mutated: the same
		// *ProviderConfig is handed to every request for this provider.
		c := *cfg
		c.BaseURL = codexCompactURL(c.BaseURL)
		cfg = &c
	}
	resp, err := proxy.ForwardCodex(ctx, req.Client, cfg, req.APIKey, transformedBody, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardCodex: %w", err)
	}
	defer resp.Body.Close()
	return handleCodexStream(w, req, resp.Body)
}

// codexCompactURL appends the compaction path segment to a Codex base URL.
// Idempotent, so a config that already points at the compact endpoint (or a
// retry that runs this twice) does not grow a second suffix.
func codexCompactURL(base string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/compact") {
		return b
	}
	return b + "/compact"
}

// ForwardIflow forwards to iflow with HMAC-SHA256 signature.
// Injects stream_options and generates HMAC headers before forwarding.
func ForwardIflow(w http.ResponseWriter, req *Request) error {
	var reqMap map[string]interface{}
	if err := json.Unmarshal(req.Body, &reqMap); err != nil {
		return fmt.Errorf("parse body: %w", err)
	}
	if req.IsStream {
		reqMap["stream"] = true
		if _, ok := reqMap["stream_options"]; !ok {
			reqMap["stream_options"] = map[string]interface{}{"include_usage": true}
		}
	}
	reqBody, err := json.Marshal(reqMap)
	if err != nil {
		return fmt.Errorf("marshal iflow body: %w", err)
	}

	// HMAC-SHA256 signature
	sessionID := "session-" + uuid.New().String()
	timestamp := time.Now().UnixMilli()
	userAgent := "iFlow-Cli"
	payload := userAgent + ":" + sessionID + ":" + strconv.FormatInt(timestamp, 10)

	mac := hmac.New(sha256.New, []byte(req.APIKey))
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	extraHeaders := map[string]string{
		"User-Agent":        userAgent,
		"session-id":        sessionID,
		"x-iflow-timestamp": strconv.FormatInt(timestamp, 10),
		"x-iflow-signature": signature,
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardIflow(ctx, req.Client, req.Config, req.APIKey, reqBody, req.IsStream, extraHeaders)
	if err != nil {
		return fmt.Errorf("ForwardIflow upstream: %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

// ForwardKimchi forwards to kimchi with Anthropic field stripping.
// Cleans Anthropic-specific fields from body before forwarding.
func ForwardKimchi(w http.ResponseWriter, req *Request) error {
	var reqBody map[string]any
	if err := json.Unmarshal(req.Body, &reqBody); err != nil {
		return fmt.Errorf("parse request body: %w", err)
	}

	CleanKimchiBody(reqBody)

	cleanedBody, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal cleaned body: %w", err)
	}

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardKimchi(ctx, req.Client, req.Config, req.APIKey, cleanedBody, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardKimchi: %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

// ForwardKiro forwards to kiro with AWS EventStream response handling.
// Uses EventStream binary parsing instead of standard SSE.
func ForwardKiro(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardKiro(ctx, req.Client, req.Config, req.APIKey, req.Body, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardKiro: %w", err)
	}
	defer resp.Body.Close()
	return handleKiroStream(w, req, resp.Body)
}

// ForwardAzure forwards to Azure OpenAI with dynamic URL from env vars.
func ForwardAzure(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(req.Body, &oreq); err != nil {
		log.Warn("executor", "azure unmarshal body", "error", err)
	}
	modelName := oreq.Model
	if modelName == "" {
		modelName = "gpt-4"
	}

	endpoint := os.Getenv("AZURE_ENDPOINT")
	apiVersion := os.Getenv("AZURE_API_VERSION")
	if apiVersion == "" {
		apiVersion = "2024-10-01-preview"
	}
	deployment := os.Getenv("AZURE_DEPLOYMENT")
	if deployment == "" {
		deployment = modelName
	}
	if endpoint == "" {
		endpoint = "https://api.openai.com"
	}

	baseURL := strings.TrimRight(endpoint, "/")
	url := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		baseURL, deployment, apiVersion)

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	r, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(req.Body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("api-key", req.APIKey)
	if req.IsStream {
		r.Header.Set("Accept", "text/event-stream")
	}

	resp, err := req.Client.Do(r)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		if readErr != nil {
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: []byte("failed to read error body")}
		}
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

func parseDataURIMime(uri string) string {
	if strings.HasPrefix(uri, "data:") {
		if idx := strings.Index(uri, ";"); idx > 5 {
			return uri[5:idx]
		}
	}
	return "image/png"
}

func toCommandcodeImageBlock(part map[string]any) map[string]any {
	pType, _ := part["type"].(string)
	if pType == "image_url" {
		var urlStr string
		switch u := part["image_url"].(type) {
		case string:
			urlStr = u
		case map[string]any:
			urlStr, _ = u["url"].(string)
		}
		if urlStr != "" && strings.HasPrefix(urlStr, "data:") {
			mime := parseDataURIMime(urlStr)
			return map[string]any{
				"type":      "image",
				"image":     urlStr,
				"mimeType":  mime,
				"mediaType": mime,
			}
		}
	}
	if pType == "image" {
		if imgStr, ok := part["image"].(string); ok && strings.HasPrefix(imgStr, "data:") {
			mime, _ := part["mimeType"].(string)
			if mime == "" {
				mime = parseDataURIMime(imgStr)
			}
			return map[string]any{
				"type":      "image",
				"image":     imgStr,
				"mimeType":  mime,
				"mediaType": mime,
			}
		}
		if src, ok := part["source"].(map[string]any); ok {
			mediaType, _ := src["media_type"].(string)
			if mediaType == "" {
				mediaType = "image/png"
			}
			data, _ := src["data"].(string)
			if data != "" {
				dataURI := fmt.Sprintf("data:%s;base64,%s", mediaType, data)
				return map[string]any{
					"type":      "image",
					"image":     dataURI,
					"mimeType":  mediaType,
					"mediaType": mediaType,
				}
			}
		}
	}
	return nil
}

func jsonErrorBody(message, errType string, code int) []byte {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	})
	if err != nil {
		return []byte(`{"error":{"message":"upstream error","type":"server_error","code":500}}`)
	}
	return body
}

// buildCommandcodeBody transforms OpenAI request payload into CommandCode schema
// {threadId, memory, config, params} matching upstream openaiToCommandCodeRequest.
func buildCommandcodeBody(body []byte, model string) ([]byte, error) {
	return buildCommandcodeBodyForSession(body, model, "")
}

// buildCommandcodeBodyForSession is buildCommandcodeBody with a
// conversation-stable threadId derived from the client session, so the
// upstream prefix cache hits across turns of one conversation.
func buildCommandcodeBodyForSession(body []byte, model, sessionID string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, err
	}

	// Already wrapped in params: normalise, don't rebuild (lets callers
	// send the exact upstream schema).
	if rawParams, hasParams := m["params"]; hasParams {
		p, ok := rawParams.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("buildCommandcodeBody: params must be an object")
		}
		params := map[string]any{}
		for _, k := range []string{"messages", "stream", "temperature", "top_p", "tools", "reasoning_effort", "system"} {
			if v, ok := p[k]; ok && v != nil {
				params[k] = v
			}
		}
		if _, ok := params["stream"]; !ok {
			params["stream"] = true
		}
		if _, ok := params["messages"]; !ok {
			params["messages"] = []any{}
		}
		params["model"] = model
		params["max_tokens"] = commandcodeMaxTokens(p)
		m["params"] = params
		if _, hasThread := m["threadId"]; !hasThread {
			m["threadId"] = commandcodeSessionUUID(sessionID)
		}
		if _, hasMem := m["memory"]; !hasMem {
			m["memory"] = ""
		}
		if _, hasCfg := m["config"]; !hasCfg {
			m["config"] = map[string]any{
				"workingDir":    commandcodeWorkingDir,
				"date":          time.Now().UTC().Format("2006-01-02"),
				"environment":   runtime.GOOS,
				"structure":     []any{},
				"isGitRepo":     false,
				"currentBranch": "",
				"mainBranch":    "",
				"gitStatus":     "",
				"recentCommits": []any{},
			}
		}
		return marshalStable(m)
	}

	params := map[string]any{
		"model":      model,
		"messages":   []any{},
		"stream":     true,
		"max_tokens": commandcodeMaxTokens(m),
	}
	if temp, ok := m["temperature"]; ok && temp != nil {
		params["temperature"] = temp
	} else {
		params["temperature"] = 0.3
	}
	if top, ok := m["top_p"]; ok && top != nil {
		params["top_p"] = top
	}
	if tools, ok := m["tools"].([]any); ok && len(tools) > 0 {
		params["tools"] = commandcodeTools(convertOpenAIToolsToClaude(tools))
	}
	if effort, ok := m["reasoning_effort"]; ok && effort != nil {
		params["reasoning_effort"] = effort
	}

	if rawMsgs, ok := m["messages"].([]any); ok {
		messages, system := commandcodeConvertMessages(rawMsgs)
		params["messages"] = messages
		if system != "" {
			params["system"] = system
		}
	}

	payload := map[string]any{
		"threadId": commandcodeSessionUUID(sessionID),
		"memory":   "",
		"config": map[string]any{
			"workingDir":    commandcodeWorkingDir,
			"date":          time.Now().UTC().Format("2006-01-02"),
			"environment":   runtime.GOOS,
			"structure":     []any{},
			"isGitRepo":     false,
			"currentBranch": "",
			"mainBranch":    "",
			"gitStatus":     "",
			"recentCommits": []any{},
		},
		"params": params,
	}

	// marshalStable, not json.Marshal: CommandCode is a prefix-caching
	// upstream, so the same logical request must serialise byte-identically
	// or every turn after the first misses the prompt cache. (json/v2
	// randomises map member order without json.Deterministic.)
	return marshalStable(payload)
}

// ForwardCommandcode forwards to CommandCode with NDJSON→SSE translation.
// 502/503/504 (HTTP status or in-stream error before any byte is written)
// are retried twice with a linear backoff, as upstream's executor does.
func ForwardCommandcode(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(req.Body, &oreq); err != nil {
		log.Warn("executor", "commandcode unmarshal body", "error", err)
	}

	body := InjectReasoningContent(req.Body, "commandcode")
	reqBody, err := buildCommandcodeBodyForSession(body, oreq.Model,
		commandcodeSessionKey(body, req.SessionID, req.ConnectionID))
	if err != nil {
		return fmt.Errorf("marshal commandcode body: %w", err)
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var envelope struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(reqBody, &envelope)
	for attempt := 0; ; attempt++ {
		err = forwardCommandcodeOnce(ctx, w, req, reqBody, envelope.ThreadID, oreq.Model)
		if attempt >= commandcodeMaxRetries || !isCommandcodeRetryable(err) {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func isCommandcodeRetryable(err error) bool {
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		return false
	}
	return ue.StatusCode == http.StatusBadGateway ||
		ue.StatusCode == http.StatusServiceUnavailable ||
		ue.StatusCode == http.StatusGatewayTimeout
}

func forwardCommandcodeOnce(ctx context.Context, w http.ResponseWriter, req *Request, reqBody []byte, threadID, model string) error {
	r, err := http.NewRequestWithContext(ctx, "POST", req.Config.BaseURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+req.APIKey)
	r.Header.Set("x-session-id", threadID)
	r.Header.Set("x-project-slug", commandcodeProjectSlug)
	// Defaults mirror the registry StaticHeaders (command-code@1.88.0);
	// the loop below lets a configured value win.
	r.Header.Set("User-Agent", "cli")
	r.Header.Set("x-command-code-version", "1.88.0")
	r.Header.Set("x-cli-environment", "production")
	r.Header.Set("x-cli-surface", "cli")
	r.Header.Set("x-taste-learning", "false")
	r.Header.Set("Accept", "text/event-stream")
	if req.Config != nil && req.Config.StaticHeaders != nil {
		for k, v := range req.Config.StaticHeaders {
			r.Header.Set(k, v)
		}
	}
	resp, err := req.Client.Do(r)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		if readErr != nil {
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: []byte("failed to read error body")}
		}
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	if !req.IsStream {
		return handleCommandcodeNonStream(w, req, resp.Body, model)
	}
	return handleCommandcodeStream(w, req, resp.Body, model)
}

// ForwardOpencode handles requests for opencode (free tier).
func ForwardOpencode(w http.ResponseWriter, req *Request) error {
	apiKey := req.APIKey
	if apiKey == "" {
		apiKey = "public"
	}

	var reqObj struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(req.Body, &reqObj)
	cleanModel := strings.TrimPrefix(reqObj.Model, "oc/")
	cleanModel = strings.TrimPrefix(cleanModel, "opencode/")
	cleanModel = strings.TrimPrefix(cleanModel, "antigravity/")
	cleanModel = strings.TrimPrefix(cleanModel, "ag/")
	if parenIdx := strings.IndexByte(cleanModel, '('); parenIdx != -1 {
		cleanModel = cleanModel[:parenIdx]
	}

	if isOpencodeResponsesModel(cleanModel) {
		// Route through Responses API format: https://opencode.ai/zen/v1/responses
		transformedBody, _, err := buildResponsesBody(req.Body)
		if err != nil {
			return fmt.Errorf("transform body for muse-spark: %w", err)
		}

		transformedBody, err = normalizeMuseSparkResponsesBody(transformedBody, cleanModel)
		if err != nil {
			return fmt.Errorf("normalize muse-spark body: %w", err)
		}

		// The free-tier gate fingerprints the lowercase tool quartet; capitalised
		// variants from Claude Code CLI are renamed here and restored on the
		// response (translator.ConcealFingerprintTools). The `<env>` system
		// marker must ride along with the declarations — see
		// translator.ApplyEnvMarker.
		transformedBody = translator.ApplyEnvMarker(transformedBody, true)
		transformedBody, toolNameMap := translator.ConcealFingerprintTools(transformedBody, true)
		req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
		w = NewToolNameRestoringWriter(w, toolNameMap)

		cfg := *req.Config
		if !strings.HasSuffix(cfg.BaseURL, "/responses") {
			baseURL := strings.TrimRight(cfg.BaseURL, "/")
			if strings.HasSuffix(baseURL, "/chat/completions") {
				baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
			}
			cfg.BaseURL = baseURL + "/responses"
		}
		hint := opencodeSessionHint(req, true)
		cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(cfg.StaticHeaders, hint, true)

		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		baseHeaders := cfg.StaticHeaders
		resp, err := forwardOpenCodeRotating(ctx, func() (*http.Response, error) {
			return forwardOpencodeRequest(ctx, req.Client, &cfg, apiKey, transformedBody, req.IsStream)
		}, func() (*http.Response, error) {
			return forwardOpencodeRequestDirect(ctx, &cfg, apiKey, transformedBody, req.IsStream)
		}, rotateOpencodeStableSession(opencodeStableSessionKey(req, true), hint, func(session string) {
			cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(baseHeaders, session, req.IsStream)
		}))
		if err != nil {
			return fmt.Errorf("ForwardOpencode (muse-spark responses): %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
		}

		if req.IsStream {
			stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, "opencode-responses")
			defer stallReader.Close()
			return handleCodexStream(w, req, stallReader)
		}
		return handleCodexStream(w, req, resp.Body)
	}
	if cleanModel == "union-alpha" {
		// Route through Messages API format: https://opencode.ai/zen/v1/messages (PR #4099)
		messagesURL := "https://opencode.ai/zen/v1/messages"
		if req.Config != nil && req.Config.BaseURL != "" && !strings.Contains(req.Config.BaseURL, "opencode.ai") {
			base := strings.TrimRight(req.Config.BaseURL, "/")
			if strings.HasSuffix(base, "/chat/completions") {
				base = strings.TrimSuffix(base, "/chat/completions")
			}
			messagesURL = base + "/messages"
		}
		hint := opencodeSessionHint(req, true)
		headers := proxy.BuildOpenCodeHeaders(nil, hint, req.IsStream)
		headers["anthropic-version"] = "2023-06-01"
		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		body := ensureMessagesMaxTokens(req.Body, cleanModel)
		resp, err := proxy.DoRequest(ctx, req.Client, "POST", messagesURL, headers, body)
		if err != nil {
			return fmt.Errorf("ForwardOpencode (union-alpha messages route): %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
		}

		if req.IsStream {
			return handleClaudeMessagesStream(w, req, resp.Body)
		}
		return handleClaudeMessagesNonStream(w, req, resp.Body)
	}

	body := InjectReasoningContent(req.Body, "opencode")
	// opencode Chat Completions path: same conceal + restore of tool names, and
	// the `<env>` marker the free gate pairs with the tool quartet.
	body = translator.ApplyEnvMarker(body, false)
	body, toolNameMap := translator.ConcealFingerprintTools(body, false)
	req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
	w = NewToolNameRestoringWriter(w, toolNameMap)

	cfg := *req.Config
	hint := opencodeSessionHint(req, true)
	cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(cfg.StaticHeaders, hint, true)

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// The Zen free-tier gate rejects a non-streaming request, so the body always
	// carries stream:true by the time it gets here (see ConcealFingerprintTools).
	// The upstream call must therefore ask for SSE regardless of what the client
	// wanted, and a non-streaming caller gets the SSE collapsed back into one
	// JSON body below.
	baseHeaders := cfg.StaticHeaders
	resp, err := forwardOpenCodeRotating(ctx, func() (*http.Response, error) {
		return forwardOpencodeRequest(ctx, req.Client, &cfg, apiKey, body, true)
	}, func() (*http.Response, error) {
		return forwardOpencodeRequestDirect(ctx, &cfg, apiKey, body, true)
	}, rotateOpencodeStableSession(opencodeStableSessionKey(req, true), hint, func(session string) {
		cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(baseHeaders, session, true)
	}))
	if err != nil {
		return fmt.Errorf("ForwardOpencode: %w", err)
	}
	defer resp.Body.Close()

	// A 200 whose body is one bare error object (OpenCode's free-tier gate
	// answering FreeUsageLimitError at generation time, not at the HTTP
	// layer) must surface as an upstream error, not be streamed verbatim or
	// collapsed into a completion. Otherwise a strict OpenAI streaming
	// client (the Pi harness) sees a stream with no finish_reason and fails
	// with "Stream ended without finish_reason" instead of the real cause,
	// and fallback never gets to retry. Peek before writing anything.
	if resp.StatusCode == http.StatusOK {
		chunk, replay := readPeekedBody(resp.Body)
		if err := proxy.ParseBareErrorBody(chunk); err != nil {
			if ue, ok := err.(*proxy.UpstreamError); ok {
				return ue
			}
			return err
		}
		resp.Body = replay
	}

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	// We always force stream=true upstream for the free-tier gate; collapse SSE
	// to one JSON body when the client asked for non-stream.
	return jsonFromSSE(ctx, w, resp.Body, req)
}

// jsonFromSSE collapses an upstream SSE stream into a single Chat Completions
// JSON body.
//
// The Zen free-tier gate only serves streaming requests, so a client that asked
// for one JSON object still causes an SSE upstream call. Rather than leak the
// stream to that client, the deltas are merged back into the shape it expects:
// content and reasoning_content are concatenated in order, tool_call fragments
// are reassembled by index, and the finish_reason from the last chunk wins.
func jsonFromSSE(ctx context.Context, w http.ResponseWriter, upstream io.Reader, req *Request) error {
	if ctx == nil {
		ctx = context.Background()
	}

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var (
		id      string
		model   string
		created int64
		content strings.Builder
		reason  strings.Builder
		finish  string
		tools   = map[int]*streamToolCall{}
		order   []int
		sawSSE  bool
	)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		sawSSE = sawSSE || strings.HasPrefix(line, "data:")
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Created int64  `json:"created"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if id == "" {
			id = chunk.ID
		}
		if model == "" {
			model = chunk.Model
		}
		if created == 0 {
			created = chunk.Created
		}
		for _, ch := range chunk.Choices {
			content.WriteString(ch.Delta.Content)
			reason.WriteString(ch.Delta.ReasoningContent)
			reason.WriteString(ch.Delta.Reasoning)
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
			for _, tc := range ch.Delta.ToolCalls {
				cur, ok := tools[tc.Index]
				if !ok {
					cur = &streamToolCall{Index: tc.Index}
					tools[tc.Index] = cur
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Type != "" {
					cur.Type = tc.Type
				}
				if tc.Function.Name != "" {
					cur.Name = tc.Function.Name
				}
				cur.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read upstream stream: %w", err)
	}

	message := map[string]any{"role": "assistant", "content": content.String()}
	if reason.Len() > 0 {
		message["reasoning_content"] = reason.String()
	}
	if len(order) > 0 {
		sort.Ints(order)
		calls := make([]any, 0, len(order))
		for _, i := range order {
			tc := tools[i]
			if tc == nil {
				continue
			}
			calls = append(calls, map[string]any{
				"id": tc.ID, "type": "function", "index": tc.Index,
				"function": map[string]any{"name": tc.Name, "arguments": tc.Arguments},
			})
		}
		message["tool_calls"] = calls
	}

	// A 200 whose body collapses to zero content is a failed generation,
	// not a successful answer: upstream answered, but no delta ever
	// arrived. This is the class that produced the empty stream the Pi
	// harness reports as "Stream ended without finish_reason" — an
	// honest upstream error, so fallback can retry another account.
	if sawSSE && content.Len() == 0 && len(order) == 0 {
		return &proxy.UpstreamError{StatusCode: 502, Body: []byte(`{"error":{"type":"empty_stream","message":"upstream returned an empty stream"}}`)}
	}

	if finish == "" {
		finish = "stop"
	}
	body := map[string]any{
		"id":      firstNonEmpty(id, "chatcmpl-9router"),
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finish,
		}},
	}
	out, err := json.Marshal(body)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(out)
	return err
}

// streamToolCall accumulates a tool call whose arguments arrive in fragments.
type streamToolCall struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments string
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// bodyRequestsStream reports whether a marshalled request body asks the upstream
// to stream. It is how the executor learns that the fingerprint step forced
// stream:true on behalf of a non-streaming client.
func bodyRequestsStream(body []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Stream
}

var opencodeGoMessagesModels = map[string]bool{
	"minimax-m3":    true,
	"minimax-m2.7":  true,
	"minimax-m2.5":  true,
	"qwen3.8-max":   true,
	"qwen3.8-flash": true,
	"qwen3.7-max":   true,
	"qwen3.7-plus":  true,
	"qwen3.6-plus":  true,
	"union-alpha":   true,
}

// EnsureClaudeMessages exposes the OpenAI→Claude Messages request conversion
// (see ensureMessagesMaxTokens) for the fallback path, which forwards raw
// OpenAI-format bodies to Anthropic-native upstreams.
func EnsureClaudeMessages(body []byte, model string) []byte {
	return ensureMessagesMaxTokens(body, model)
}

// ensureMessagesMaxTokens converts an incoming request (OpenAI or Claude) into a spec-compliant
// Claude Messages API request payload:
// - Guarantees positive integer max_tokens (fallback from max_completion_tokens or default 4096)
// - Converts OpenAI tools [{type: "function", function: {name, description, parameters}}] to Claude [{name, description, input_schema}]
// - Converts OpenAI tool_choice to Claude format
// - Extracts role: "system" from messages into top-level system prompt
// - Converts assistant tool_calls to tool_use blocks and role: "tool" to user tool_result blocks
// - Merges consecutive same-role messages to uphold Claude alternating role invariant
// - Strips OpenAI-only fields like stream_options, store, max_completion_tokens, reasoning_effort
func ensureMessagesMaxTokens(body []byte, model string) []byte {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return body
	}
	if model != "" {
		reqMap["model"] = model
	}

	// 1. max_tokens
	maxTokensVal := 0
	if mt, ok := reqMap["max_tokens"]; ok && mt != nil {
		switch v := mt.(type) {
		case float64:
			maxTokensVal = int(v)
		case int:
			maxTokensVal = v
		case int64:
			maxTokensVal = int(v)
		}
	}
	if maxTokensVal <= 0 {
		if mct, ok := reqMap["max_completion_tokens"]; ok && mct != nil {
			switch v := mct.(type) {
			case float64:
				maxTokensVal = int(v)
			case int:
				maxTokensVal = v
			case int64:
				maxTokensVal = int(v)
			}
		}
	}
	if maxTokensVal <= 0 {
		maxTokensVal = 4096
	}
	reqMap["max_tokens"] = maxTokensVal
	delete(reqMap, "max_completion_tokens")

	// 2. tools: convert OpenAI tools to Claude {name, description, input_schema}
	if tools, ok := reqMap["tools"].([]any); ok && len(tools) > 0 {
		reqMap["tools"] = convertOpenAIToolsToClaude(tools)
	}

	// 3. tool_choice: convert OpenAI tool_choice to Claude format
	if tc, ok := reqMap["tool_choice"]; ok && tc != nil {
		if convertedTC := convertToolChoiceToClaude(tc); convertedTC != nil {
			reqMap["tool_choice"] = convertedTC
		} else {
			delete(reqMap, "tool_choice")
		}
	}

	// 4. messages & system
	if msgs, ok := reqMap["messages"].([]any); ok && len(msgs) > 0 {
		extractedSys, claudeMsgs := convertOpenAIMessagesToClaude(msgs)
		reqMap["messages"] = claudeMsgs
		if extractedSys != "" {
			if existingSys, ok := reqMap["system"].(string); ok && existingSys != "" {
				reqMap["system"] = existingSys + "\n\n" + extractedSys
			} else if reqMap["system"] == nil {
				reqMap["system"] = extractedSys
			}
		}
	}

	// 5. Clean OpenAI-specific fields that strict Claude API rejects
	delete(reqMap, "stream_options")
	delete(reqMap, "store")
	delete(reqMap, "reasoning_effort")

	updated, err := json.Marshal(reqMap)
	if err != nil {
		return body
	}
	return updated
}

func convertOpenAIToolsToClaude(tools []any) []any {
	if len(tools) == 0 {
		return tools
	}
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			out = append(out, t)
			continue
		}
		if _, hasName := m["name"]; hasName {
			if _, hasSchema := m["input_schema"]; hasSchema {
				out = append(out, t)
				continue
			}
		}
		if fn, ok := m["function"].(map[string]any); ok {
			cTool := make(map[string]any)
			if name, ok := fn["name"].(string); ok {
				cTool["name"] = name
			}
			if desc, ok := fn["description"].(string); ok && desc != "" {
				cTool["description"] = desc
			}
			if params, ok := fn["parameters"]; ok && params != nil {
				cTool["input_schema"] = params
			} else {
				cTool["input_schema"] = map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				}
			}
			if cc, ok := m["cache_control"]; ok {
				cTool["cache_control"] = cc
			}
			out = append(out, cTool)
			continue
		}
		if params, ok := m["parameters"]; ok {
			cTool := make(map[string]any, len(m))
			for k, v := range m {
				if k != "type" && k != "parameters" {
					cTool[k] = v
				}
			}
			cTool["input_schema"] = params
			out = append(out, cTool)
			continue
		}
		out = append(out, t)
	}
	return out
}

func convertToolChoiceToClaude(tc any) any {
	if tc == nil {
		return nil
	}
	switch v := tc.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required":
			return map[string]any{"type": "any"}
		case "none":
			return nil
		default:
			return map[string]any{"type": "auto"}
		}
	case map[string]any:
		if tType, _ := v["type"].(string); tType == "function" {
			if fn, ok := v["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					return map[string]any{"type": "tool", "name": name}
				}
			}
		}
		return v
	default:
		return tc
	}
}

// sanitizeToolUseID returns a tool id valid for the Anthropic Messages API
// (must match ^[a-zA-Z0-9_-]+$). Ids from other upstreams (e.g. Gemini
// function-call history translated to OpenAI format) may contain other
// characters; those are rewritten deterministically as "toolu_<sha256>" so
// the same id always maps to the same replacement, keeping tool_use and
// tool_result blocks paired. mapping must be shared across the whole request.
//
// An EMPTY id is replaced too, not passed through: the Anthropic Messages API
// requires tool_use.id, so forwarding "" makes the upstream reject the whole
// request with 400 "messages.N.content.N.tool_use.id: Field required". History
// can lose ids when another upstream trims a tool_calls entry or the client
// omits the field. Because both tool_use and tool_result look up the same ""
// key, they receive the same synthetic id and stay paired.
func sanitizeToolUseID(id string, mapping map[string]string) string {
	if id == "" {
		const missing = "toolu_missing"
		mapping[id] = missing
		return missing
	}
	if mapped, ok := mapping[id]; ok {
		return mapped
	}
	valid := true
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		valid = false
		break
	}
	if valid {
		mapping[id] = id
		return id
	}
	sum := sha256.Sum256([]byte(id))
	newID := "toolu_" + hex.EncodeToString(sum[:])[:24]
	mapping[id] = newID
	return newID
}

func convertOpenAIMessagesToClaude(messages []any) (systemText string, claudeMessages []any) {
	// Anthropic requires tool_use.id / tool_use_id to match ^[a-zA-Z0-9_-]+$.
	// History from other upstreams (e.g. Gemini) can contain ids with other
	// characters; rewrite them deterministically, with a single mapping shared
	// across the whole conversation so tool_use and tool_result stay paired.
	toolIDMap := map[string]string{}
	var systemParts []string
	intermediate := make([]map[string]any, 0, len(messages))

	for _, m := range messages {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)

		if role == "system" {
			switch c := msgMap["content"].(type) {
			case string:
				if strings.TrimSpace(c) != "" {
					systemParts = append(systemParts, c)
				}
			case []any:
				for _, block := range c {
					if bMap, ok := block.(map[string]any); ok {
						if text, ok := bMap["text"].(string); ok && strings.TrimSpace(text) != "" {
							systemParts = append(systemParts, text)
						}
					}
				}
			}
			continue
		}

		if role == "tool" {
			toolCallID, _ := msgMap["tool_call_id"].(string)
			toolCallID = sanitizeToolUseID(toolCallID, toolIDMap)
			intermediate = append(intermediate, map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": toolCallID,
						"content":     convertToolResultContent(msgMap["content"]),
					},
				},
			})
			continue
		}

		if role == "assistant" {
			if toolCalls, hasTC := msgMap["tool_calls"].([]any); hasTC && len(toolCalls) > 0 {
				var contentBlocks []any
				if cStr, ok := msgMap["content"].(string); ok && cStr != "" {
					contentBlocks = append(contentBlocks, map[string]any{
						"type": "text",
						"text": cStr,
					})
				} else if cArr, ok := msgMap["content"].([]any); ok && len(cArr) > 0 {
					contentBlocks = append(contentBlocks, cArr...)
				}
				for _, tc := range toolCalls {
					tcMap, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					id, _ := tcMap["id"].(string)
					id = sanitizeToolUseID(id, toolIDMap)
					fn, _ := tcMap["function"].(map[string]any)
					name := ""
					var inputMap any = map[string]any{}
					if fn != nil {
						name, _ = fn["name"].(string)
						if argsStr, ok := fn["arguments"].(string); ok && strings.TrimSpace(argsStr) != "" {
							var parsed any
							if err := json.Unmarshal([]byte(argsStr), &parsed); err == nil && parsed != nil {
								inputMap = parsed
							}
						}
					}
					contentBlocks = append(contentBlocks, map[string]any{
						"type":  "tool_use",
						"id":    id,
						"name":  name,
						"input": inputMap,
					})
				}
				newMsg := make(map[string]any)
				for k, v := range msgMap {
					if k != "tool_calls" && k != "content" {
						newMsg[k] = v
					}
				}
				newMsg["role"] = "assistant"
				newMsg["content"] = contentBlocks
				intermediate = append(intermediate, newMsg)
				continue
			}
		}

		newMsg := make(map[string]any, len(msgMap))
		for k, v := range msgMap {
			newMsg[k] = v
		}
		intermediate = append(intermediate, newMsg)
	}

	var merged []any
	for _, m := range intermediate {
		if len(merged) == 0 {
			merged = append(merged, m)
			continue
		}
		prev := merged[len(merged)-1].(map[string]any)
		if prev["role"] == m["role"] {
			prev["content"] = combineClaudeContent(prev["content"], m["content"])
		} else {
			merged = append(merged, m)
		}
	}

	systemText = strings.Join(systemParts, "\n\n")
	return systemText, merged
}

func combineClaudeContent(c1, c2 any) any {
	return append(normalizeClaudeBlocks(c1), normalizeClaudeBlocks(c2)...)
}

func normalizeClaudeBlocks(c any) []any {
	if c == nil {
		return []any{}
	}
	switch v := c.(type) {
	case string:
		if v == "" {
			return []any{}
		}
		return []any{map[string]any{"type": "text", "text": v}}
	case []any:
		return v
	case map[string]any:
		return []any{v}
	default:
		return []any{map[string]any{"type": "text", "text": fmt.Sprint(v)}}
	}
}

func isOpencodeResponsesModel(model string) bool {
	base := model
	if parenIdx := strings.IndexByte(base, '('); parenIdx != -1 {
		base = base[:parenIdx]
	}
	return strings.Contains(base, "muse-spark") || base == "grok-4.6" || base == "gpt-5.6-luna"
}

func deriveOpencodeSession(rawSession, clientTool, connID string) string {
	raw := strings.TrimSpace(rawSession)
	if raw == "" {
		raw = strings.TrimSpace(connID)
	}
	if raw == "" {
		raw = "default"
	}
	return proxy.TranslateOpenCodeSessionID(raw, clientTool)
}

// minMuseSparkOutputTokens is the floor muse-spark enforces on
// max_output_tokens; requests below it are rejected with a 400.
const minMuseSparkOutputTokens = 16

// numericTokenLimit reads a token count that may arrive as a JSON number or as
// a string, since clients are inconsistent about it.
func numericTokenLimit(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		return i, err == nil
	}
	return 0, false
}

func normalizeMuseSparkResponsesBody(body []byte, cleanModel string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, nil
	}
	if rEffort, ok := m["reasoning_effort"].(string); ok {
		if rEffort == "max" {
			rEffort = "xhigh"
		}
		m["reasoning"] = map[string]any{
			"effort":  rEffort,
			"summary": "auto",
		}
		delete(m, "reasoning_effort")
	} else if rMap, ok := m["reasoning"].(map[string]any); ok {
		if eff, ok := rMap["effort"].(string); ok && eff == "max" {
			rMap["effort"] = "xhigh"
		}
		rMap["summary"] = "auto"
	}
	// PR #4061: Strip prior reasoning items & continuity properties
	if inList, ok := m["input"].([]any); ok {
		cleanInput := make([]any, 0, len(inList))
		for _, item := range inList {
			if itemMap, ok := item.(map[string]any); ok {
				if itemMap["type"] == "reasoning" {
					continue
				}
				delete(itemMap, "encrypted_content")
				delete(itemMap, "reasoning_encrypted_content")
				cleanInput = append(cleanInput, itemMap)
			} else {
				cleanInput = append(cleanInput, item)
			}
		}
		m["input"] = cleanInput
	}
	// muse-spark rejects max_output_tokens below 16 with a 400:
	//   "`max_output_tokens` The number must be `>= 16`."
	// A caller asking for a very short answer (a smoke test, a one-word reply)
	// therefore gets a hard failure for a request the model would have served.
	// Clamp up rather than forward the rejection — the caller's intent is "a
	// short answer", not "exactly this cap".
	if v, ok := m["max_output_tokens"]; ok {
		if n, ok := numericTokenLimit(v); ok && n < minMuseSparkOutputTokens {
			m["max_output_tokens"] = minMuseSparkOutputTokens
		}
	}
	// PR #4062: Normalize explicit non-auto tool_choice to "auto" on muse-spark-1.3
	if strings.Contains(cleanModel, "muse-spark-1.3") {
		if tc, ok := m["tool_choice"]; ok && tc != nil {
			if tcStr, ok := tc.(string); !ok || tcStr != "auto" {
				m["tool_choice"] = "auto"
			}
		}
	}
	return json.Marshal(m)
}

// ForwardOpencodeGo handles requests for opencode-go (paid tier).
func ForwardOpencodeGo(w http.ResponseWriter, req *Request) error {
	body := InjectReasoningContent(req.Body, "opencode-go")

	var reqObj struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqObj); err != nil {
		log.Warn("executor", "opencode unmarshal body", "error", err)
	}

	sessionHeader := deriveOpencodeSession(req.SessionID, "", req.ConnectionID)

	cleanModel := strings.TrimPrefix(reqObj.Model, "oc/")
	cleanModel = strings.TrimPrefix(cleanModel, "opencode-go/")
	cleanModel = strings.TrimPrefix(cleanModel, "opencode/")
	cleanModel = strings.TrimPrefix(cleanModel, "antigravity/")
	cleanModel = strings.TrimPrefix(cleanModel, "ag/")
	if parenIdx := strings.IndexByte(cleanModel, '('); parenIdx != -1 {
		cleanModel = cleanModel[:parenIdx]
	}

	if isOpencodeResponsesModel(cleanModel) {
		// Route through Responses API format: https://opencode.ai/zen/go/v1/responses (#3819, #3820, v0.5.75)
		transformedBody, _, err := buildResponsesBody(req.Body)
		if err != nil {
			return fmt.Errorf("transform body for opencode-go muse-spark: %w", err)
		}

		transformedBody, err = normalizeMuseSparkResponsesBody(transformedBody, cleanModel)
		if err != nil {
			return fmt.Errorf("normalize opencode-go muse-spark body: %w", err)
		}

		cfg := *req.Config
		if !strings.HasSuffix(cfg.BaseURL, "/responses") {
			baseURL := strings.TrimRight(cfg.BaseURL, "/")
			if strings.HasSuffix(baseURL, "/chat/completions") {
				baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
			}
			cfg.BaseURL = baseURL + "/responses"
		}
		headers := make(map[string]string)
		for k, v := range cfg.StaticHeaders {
			headers[k] = v
		}
		if headers["User-Agent"] == "" || !proxy.HasValidOpenCodeVersion(headers["User-Agent"]) {
			headers["User-Agent"] = proxy.DefaultOpenCodeUA
		}
		if headers["x-opencode-client"] == "" {
			headers["x-opencode-client"] = "desktop"
		}
		if headers["x-opencode-request"] == "" {
			headers["x-opencode-request"] = proxy.GenerateOpenCodeRequestID()
		}
		headers["x-opencode-session"] = sessionHeader
		cfg.StaticHeaders = headers

		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		resp, err := proxy.ForwardOpenAI(ctx, req.Client, &cfg, req.APIKey, transformedBody, req.IsStream)
		if err != nil {
			return fmt.Errorf("ForwardOpencodeGo (muse-spark responses): %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
		}

		if req.IsStream {
			stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, "opencode-go-responses")
			defer stallReader.Close()
			return handleCodexStream(w, req, stallReader)
		}
		return handleCodexStream(w, req, resp.Body)
	}

	if opencodeGoMessagesModels[reqObj.Model] || opencodeGoMessagesModels[cleanModel] || cleanModel == "union-alpha" {
		// Route to /zen/go/v1/messages (Anthropic/Claude format)
		messagesURL := "https://opencode.ai/zen/go/v1/messages"
		if req.Config != nil && req.Config.BaseURL != "" && !strings.Contains(req.Config.BaseURL, "opencode.ai") {
			base := strings.TrimRight(req.Config.BaseURL, "/")
			if strings.HasSuffix(base, "/chat/completions") {
				base = strings.TrimSuffix(base, "/chat/completions")
			}
			messagesURL = base + "/messages"
		}
		headers := map[string]string{
			"Content-Type":       "application/json",
			"x-api-key":          req.APIKey,
			"anthropic-version":  "2023-06-01",
			"x-opencode-session": sessionHeader,
		}
		if req.IsStream {
			headers["Accept"] = "text/event-stream"
		}
		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		messagesBody := ensureMessagesMaxTokens(body, cleanModel)
		resp, err := proxy.DoRequest(ctx, req.Client, "POST", messagesURL, headers, messagesBody)
		if err != nil {
			return fmt.Errorf("ForwardOpencodeGo (messages route): %w", err)
		}
		defer resp.Body.Close()

		if req.IsStream {
			return handleClaudeMessagesStream(w, req, resp.Body)
		}
		return handleClaudeMessagesNonStream(w, req, resp.Body)
	}

	// Default OpenAI format endpoint: https://opencode.ai/zen/go/v1/chat/completions
	body, toolNameMap := translator.ConcealFingerprintTools(body, false)
	req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
	w = NewToolNameRestoringWriter(w, toolNameMap)

	cfg := *req.Config
	headers := make(map[string]string)
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	if headers["User-Agent"] == "" || !proxy.HasValidOpenCodeVersion(headers["User-Agent"]) {
		headers["User-Agent"] = proxy.DefaultOpenCodeUA
	}
	if headers["x-opencode-client"] == "" {
		headers["x-opencode-client"] = "desktop"
	}
	if headers["x-opencode-request"] == "" {
		headers["x-opencode-request"] = proxy.GenerateOpenCodeRequestID()
	}
	headers["x-opencode-session"] = sessionHeader
	cfg.StaticHeaders = headers

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardOpenAI(ctx, req.Client, &cfg, req.APIKey, body, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardOpencodeGo (default route): %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}
