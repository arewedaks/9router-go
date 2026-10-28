package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
)

const (
	freebuffSessionPath   = "/api/v1/freebuff/session"
	freebuffAdmissionPath = "/api/v1/freebuff/session/admission"
	freebuffRunPath       = "/api/v1/agent-runs"
	freebuffSystemMarker  = "You are Buffy, the strategic coding assistant."
	freebuffCLIUserAgent  = "codebuff-cli/0.0.138"
	freebuffChatUserAgent = "ai-sdk/openai-compatible/1.0/codebuff"
	freebuffSessionTTL    = 60 * time.Minute

	// FreebuffCLIVersion is the client version the gateway's update gate reads
	// from `x-freebuff-client-version`. It is load-bearing: a version below the
	// running release is answered HTTP 410 "This version of Freebuff is out of
	// date", which is what every request from this proxy used to get once the
	// header was missing entirely. Pinned to npm `freebuff` dist-tag latest
	// (0.2.34, measured 2026-10-28); overridable so a bump needs no rebuild.
	//
	// Note the package name: the free client ships as `freebuff`, not `codebuff`.
	// codebuff@1.0.688 is the paid Codebuff CLI and does not carry this gate.
	freebuffClientName = "cli"

	// freebuffAdmissionUnsupported is the 404/405 errorCode the CLI treats as
	// "this gateway predates the admission endpoint" and falls back from.
	freebuffAdmissionUnsupported = "session_admission_unsupported"
)

// FreebuffCLIVersion is a var, not a const, so GITHUB_COPILOT_CLI_VERSION-style
// overrides and tests can pin it.
var FreebuffCLIVersion = "0.2.34"

// freebuffClientVersion returns the version string to send, honouring the
// FREEBUFF_CLI_VERSION override the same way the Copilot profile does.
func freebuffClientVersion() string {
	if v := strings.TrimSpace(os.Getenv("FREEBUFF_CLI_VERSION")); v != "" {
		return v
	}
	return FreebuffCLIVersion
}

var freebuffRootAgentByModel = map[string]string{
	"deepseek/deepseek-v4-flash":      "base3-free-deepseek-flash",
	"z-ai/glm-5.2":                    "base3-free-glm",
	"z-ai/glm-5.3-flash":              "base3-free-glm-5-3-flash",
	"mimo/mimo-v2.5":                  "base3-free-mimo",
	"openai/gpt-5.6-luna":             "base3-free-luna",
	"upstage/solar-pro4":              "base3-free-solar-pro4",
	"meta/muse-spark-1.2-contributor": "base3-free-muse-spark",
	"anthropic/claude-fable-5":        "base3-free-fable",
}

var freebuffRootOpenings = []string{
	"You are Buffy, the strategic coding assistant.",
	"You are Buffy, the Freebuff Cloud project planner.",
	"You are Buffy, a strategic assistant that orchestrates complex coding tasks through specialized sub-agents.",
}

type freebuffSession struct {
	InstanceID string
	ExpiresAt  time.Time
}

var (
	freebuffSessionMu    sync.RWMutex
	freebuffSessionCache = make(map[string]*freebuffSession) // key: token::model
)

// FreebuffSupportedModels returns the model ids this executor can route, sorted.
//
// Exported so the dashboard's Import catalogue can be validated against it: the
// two lists describe the same capability, and a model offered in Import but
// missing from the root-agent map fails on the first request. Keeping them in
// sync is enforced by a test rather than by review.
func FreebuffSupportedModels() []string {
	out := make([]string, 0, len(freebuffRootAgentByModel))
	for model := range freebuffRootAgentByModel {
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func rootAgentIdForModel(model string) string {
	if root, ok := freebuffRootAgentByModel[model]; ok {
		return root
	}
	return "base2-free"
}

func freebuffOrigin(baseURL string) string {
	if baseURL != "" {
		if u, err := url.Parse(baseURL); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	return "https://www.codebuff.com"
}

func ensureFreebuffEndTurnTool(body map[string]any) {
	rawTools, ok := body["tools"].([]any)
	if !ok || len(rawTools) == 0 {
		return
	}

	for _, t := range rawTools {
		if tm, ok := t.(map[string]any); ok {
			if fn, ok := tm["function"].(map[string]any); ok {
				if fn["name"] == "end_turn" {
					return
				}
			}
		}
	}

	endTurnTool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "end_turn",
			"description": "Signal the end of the current task.",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
	body["tools"] = append(rawTools, endTurnTool)
}

func getFreebuffSession(token, model string) (*freebuffSession, bool) {
	key := token + "::" + model
	freebuffSessionMu.RLock()
	defer freebuffSessionMu.RUnlock()
	sess, ok := freebuffSessionCache[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, false
	}
	return sess, true
}

func setFreebuffSession(token, model string, sess *freebuffSession) {
	key := token + "::" + model
	freebuffSessionMu.Lock()
	defer freebuffSessionMu.Unlock()
	freebuffSessionCache[key] = sess
}

func clearFreebuffSession(token, model string) {
	key := token + "::" + model
	freebuffSessionMu.Lock()
	defer freebuffSessionMu.Unlock()
	delete(freebuffSessionCache, key)
}

// freebuffClientHeaders are the identity headers the released freebuff CLI sends
// on every freebuff call. The version header is the one the gateway's update gate
// reads: absent or stale it answers HTTP 410 "This version of Freebuff is out of
// date", which is a hard stop for the whole account.
//
// Values read from freebuff@0.2.34 (binary): `x-freebuff-client: "cli"`,
// `x-freebuff-client-version: <CODEBUFF_CLI_VERSION>`, and
// `x-freebuff-sse-gzip-recovery: "1"`.
func freebuffClientHeaders() map[string]string {
	h := map[string]string{
		"x-freebuff-client":            freebuffClientName,
		"x-freebuff-client-version":    freebuffClientVersion(),
		"x-freebuff-sse-gzip-recovery": "1",
	}
	return h
}

// freebuffSetClientHeaders copies the CLI identity headers onto a request.
func freebuffSetClientHeaders(req *http.Request) {
	for k, v := range freebuffClientHeaders() {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
}

func requestFreebuffSession(ctx context.Context, client *http.Client, baseURL, token, model string) (*freebuffSession, error) {
	origin := freebuffOrigin(baseURL)
	// The free CLI creates sessions on /session/admission, not /session; POST to
	// the old path is what a pre-admission build did. A gateway that answers
	// 404/405 with errorCode session_admission_unsupported means it predates the
	// endpoint, so fall back rather than failing the request.
	reqURL := origin + freebuffAdmissionPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("create freebuff session request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", freebuffCLIUserAgent)
	req.Header.Set("x-freebuff-model", model)
	// The CLI marks the create as a wallet-spend attempt; without it the gateway
	// cannot attribute the Freebucks charge.
	req.Header.Set("x-freebuff-wallet-spend-limit", "session")
	freebuffSetClientHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("freebuff session request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read freebuff session response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		var probe struct {
			Error     string `json:"error"`
			ErrorCode string `json:"errorCode"`
		}
		_ = json.Unmarshal(respBytes, &probe)
		if probe.ErrorCode == freebuffAdmissionUnsupported || probe.Error == freebuffAdmissionUnsupported || len(respBytes) == 0 {
			// Retry once against the pre-admission path.
			retryReq, rerr := http.NewRequestWithContext(ctx, http.MethodPost,
				origin+freebuffSessionPath, bytes.NewReader([]byte("{}")))
			if rerr != nil {
				return nil, rerr
			}
			retryReq.Header.Set("Content-Type", "application/json")
			retryReq.Header.Set("Authorization", "Bearer "+token)
			retryReq.Header.Set("User-Agent", freebuffCLIUserAgent)
			retryReq.Header.Set("x-freebuff-model", model)
			freebuffSetClientHeaders(retryReq)
			if retryResp, rerr2 := client.Do(retryReq); rerr2 == nil {
				defer retryResp.Body.Close()
				respBytes, _ = io.ReadAll(io.LimitReader(retryResp.Body, 1024*1024))
				resp = retryResp
			} else {
				return nil, fmt.Errorf("freebuff session fallback: %w", rerr2)
			}
		}
	}

	var data struct {
		Status     string `json:"status"`
		InstanceID string `json:"instanceId"`
		ExpiresAt  string `json:"expiresAt"`
		Message    string `json:"message"`
		Error      string `json:"error"`
		ErrorCode  string `json:"errorCode"`
	}
	_ = json.Unmarshal(respBytes, &data)

	if resp.StatusCode != http.StatusOK {
		return nil, &proxy.UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       respBytes,
		}
	}

	expiresAt := time.Now().Add(freebuffSessionTTL)
	if data.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, data.ExpiresAt); err == nil {
			expiresAt = t
		}
	}

	sess := &freebuffSession{
		InstanceID: data.InstanceID,
		ExpiresAt:  expiresAt,
	}
	setFreebuffSession(token, model, sess)
	return sess, nil
}

func startFreebuffRun(ctx context.Context, client *http.Client, baseURL, token, model string) (string, error) {
	origin := freebuffOrigin(baseURL)
	reqURL := origin + freebuffRunPath

	payload := map[string]any{
		"action":         "START",
		"agentId":        rootAgentIdForModel(model),
		"ancestorRunIds": []string{},
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create freebuff run request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", freebuffCLIUserAgent)
	freebuffSetClientHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("start freebuff run failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return "", fmt.Errorf("read freebuff run response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBytes}
	}

	var data struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(respBytes, &data); err != nil || data.RunID == "" {
		return "", fmt.Errorf("freebuff run start returned no runId: %s", string(respBytes))
	}
	return data.RunID, nil
}

func finishFreebuffRun(ctx context.Context, client *http.Client, baseURL, token, runID, status string) {
	if runID == "" {
		return
	}
	origin := freebuffOrigin(baseURL)
	reqURL := origin + freebuffRunPath
	payload := map[string]any{
		"action": "FINISH",
		"runId":  runID,
		"status": status,
	}
	bodyBytes, _ := json.Marshal(payload)

	// Best-effort finish with bounded 5s timeout
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(finishCtx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", freebuffCLIUserAgent)

	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// ForwardFreebuff handles chat requests for the Freebuff provider.
func ForwardFreebuff(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	token := req.APIKey
	if token == "" {
		return fmt.Errorf("freebuff: access token is required")
	}

	var parsedBody map[string]any
	if err := json.Unmarshal(req.Body, &parsedBody); err != nil {
		return fmt.Errorf("freebuff: unmarshal request body: %w", err)
	}

	model, _ := parsedBody["model"].(string)
	if model == "" {
		model = "deepseek/deepseek-v4-flash"
	}

	// 1. Session: ask the gateway before creating one. A create is billed
	// (Freebucks, per session per hour); a GET on an existing active session is
	// not. The CLI does exactly this — see freebuff_cli.go.
	sess, ok := getFreebuffSession(token, model)
	if !ok {
		state, status, serr := freebuffFetchSessionState(ctx, client, req.Config.BaseURL, token)
		if serr != nil && status != http.StatusNotFound {
			log.Warn("freebuff", "session GET failed", "model", model, "status", status, "error", serr)
			return serr
		}
		if live := freebuffSessionFromState(state); live != nil {
			// An active session for a different model holds the single-session
			// lock; reuse only when the model matches, otherwise the POST below
			// answers 409 model_locked.
			if state.Model == "" || state.Model == model {
				setFreebuffSession(token, model, live)
				sess = live
				ok = true
			}
		}
	}
	if !ok {
		// The free tier is ad-supported; the CLI pulls a proposal and reports an
		// impression before creating a session. Best-effort, never blocking.
		freebuffRequestAds(ctx, client, req.Config.BaseURL, token, "")
		var err error
		sess, err = requestFreebuffSession(ctx, client, req.Config.BaseURL, token, model)
		if err != nil {
			log.Warn("freebuff", "session request failed", "model", model, "error", err)
			return err
		}
	}

	// 2. Start Agent Run
	runID, err := startFreebuffRun(ctx, client, req.Config.BaseURL, token, model)
	if err != nil {
		log.Warn("freebuff", "run start failed", "model", model, "error", err)
		return err
	}
	runStatus := "failed"
	defer func() {
		finishFreebuffRun(context.Background(), client, req.Config.BaseURL, token, runID, runStatus)
	}()
	// 3. Transform body
	// The CLI sends reasoning_effort top-level AND echoes it inside
	// codebuff_metadata as freebuff_reasoning_effort (the server re-clamps from
	// that key). Dropping it silently removed the caller's choice; keeping it in
	// both places is what the released binary does.
	delete(parsedBody, "reasoning")
	reasoningEffort, _ := parsedBody["reasoning_effort"].(string)

	// Match the CLI's outbound shape: Buffy marker at position 0, no foreign
	// harness identity, always streaming with usage.
	freebuffNormalizeSystemPrompt(parsedBody)
	freebuffEnsureStreaming(parsedBody)
	ensureFreebuffEndTurnTool(parsedBody)

	parsedBody["provider"] = map[string]any{
		"allow_fallbacks": false,
	}

	codebuffMeta := map[string]any{
		"client_id":        "9router-" + uuid.New().String(),
		"cost_mode":        "free",
		"run_id":           runID,
		"trace_session_id": uuid.New().String(),
	}
	if sess != nil && sess.InstanceID != "" {
		codebuffMeta["freebuff_instance_id"] = sess.InstanceID
	}
	// The server re-clamps from this key, so an effort the caller chose is only
	// honoured when it is echoed here as well as sent top-level.
	if reasoningEffort != "" {
		codebuffMeta["freebuff_reasoning_effort"] = reasoningEffort
	}
	parsedBody["codebuff_metadata"] = codebuffMeta

	transformedBody, err := json.Marshal(parsedBody)
	if err != nil {
		return fmt.Errorf("freebuff: marshal transformed body: %w", err)
	}

	// 4. Execute Chat Request
	doChat := func(body []byte) (*http.Response, error) {
		headers := map[string]string{
			"Authorization": "Bearer " + token,
			"User-Agent":    freebuffChatUserAgent,
		}
		if req.IsStream {
			headers["Accept"] = "text/event-stream"
		}
		for k, v := range req.Config.StaticHeaders {
			headers[k] = v
		}
		// The chat call carries the same client identity as the session call:
		// the update gate applies to every freebuff request, not just session
		// creation.
		for k, v := range freebuffClientHeaders() {
			if v != "" {
				headers[k] = v
			}
		}
		return proxy.DoRequest(ctx, client, http.MethodPost, req.Config.BaseURL, headers, body)
	}

	resp, err := doChat(transformedBody)
	if err != nil {
		return fmt.Errorf("freebuff upstream: %w", err)
	}

	// Stale session check. The CLI distinguishes these by the `status` field, not
	// the code alone: 409 model_locked / model_unavailable, 410 model_unavailable,
	// 428 waiting_room_required. A model the gateway has retired is a global
	// failure — recreating a session cannot revive it, and retrying only burns
	// another attempt against the same dead id.
	if resp.StatusCode == 428 || resp.StatusCode == 409 || resp.StatusCode == 410 {
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()

		var errData struct {
			Status  string `json:"status"`
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBytes, &errData)

		modelGone := resp.StatusCode == 410 ||
			errData.Status == "model_unavailable" ||
			errData.Error == "model_unavailable"
		if modelGone {
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBytes}
		}

		// If model is locked or limited IP, fail immediately
		if errData.Error == "model_locked" || strings.Contains(strings.ToLower(errData.Message), "limited") {
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBytes}
		}

		// Otherwise force re-claim session once and retry
		clearFreebuffSession(token, model)
		newSess, sessErr := requestFreebuffSession(ctx, client, req.Config.BaseURL, token, model)
		if sessErr == nil && newSess != nil {
			sess = newSess
			codebuffMeta["freebuff_instance_id"] = sess.InstanceID
			parsedBody["codebuff_metadata"] = codebuffMeta
			if tb, err := json.Marshal(parsedBody); err == nil {
				transformedBody = tb
				resp, err = doChat(transformedBody)
				if err != nil {
					return fmt.Errorf("freebuff upstream retry: %w", err)
				}
			}
		} else {
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBytes}
		}
	}

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBytes}
	}

	runStatus = "completed"

	if req.IsStream {
		stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, "freebuff")
		defer stallReader.Close()
		return execSSEStream(w, stallReader, req)
	}

	// Upstream always streams (the CLI sends stream:true unconditionally), so a
	// non-streaming caller gets the SSE collapsed into one JSON body.
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("freebuff: read streamed response: %w", err)
	}
	if !freebuffLooksLikeSSE(raw) {
		return jsonResponse(ctx, w, bytes.NewReader(raw), req.TranslateResp, req.ResponseBuf)
	}
	collapsed, err := freebuffAggregateSSE(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("freebuff: aggregate streamed response: %w", err)
	}
	return jsonResponse(ctx, w, bytes.NewReader(collapsed), req.TranslateResp, req.ResponseBuf)
}
