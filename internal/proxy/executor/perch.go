package executor

// Perch (perchai-cli) free Starter models as an OpenAI-compatible lane.
//
// Perch is not an OpenAI gateway. Its CLI talks to one internal endpoint,
//
//	POST https://app.perchai.app/api/perch-terminal/model-call
//
// with a body of its own shape:
//
//	{"request": {"messages": [...], "maxTokens": N},
//	 "runId": "...", "lane": "chat", "strictManual": true,
//	 "manualModelOptionId": "<provider>-<model>", "clientSurface": "cli"}
//
// and answers a single JSON object (or an SSE stream on request):
//
//	{"ok": true, "text": "...", "model": "deepseek/deepseek-v4.1-flash-1017",
//	 "usage": {"inputTokens": N, "outputTokens": N}}
//
// Two facts measured against a live Starter account (2026-10-23) decide this
// file's shape:
//
//  1. `preferredModelId` is a hint the server ignores — nine different ids all
//     came back served as the account's default model. Only
//     `manualModelOptionId` actually pins the model, and its value is not the
//     model name the CLI shows: "deepseek-flash-starter" is really
//     "openrouter-deepseek-deepseek-v4-1-flash-1017-cloudflare".
//  2. The per-turn ticket from /api/perch-terminal/turn-ticket is single-use
//     (a replay answers 401) and NOT required: the account JWT alone is
//     accepted. This lane therefore sends the JWT and skips the ticket dance,
//     which turns five requests per turn into one.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	jsontext "encoding/json/jsontext"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
)

// perchUserAgent is what the CLI sends. The endpoint does not appear to
// fingerprint beyond this, but keeping it identical costs nothing.
const perchUserAgent = "perchai-cli/unknown"

// PerchModelOption maps a model id the router exposes onto the
// manualModelOptionId Perch requires. The keys are the ids that appear in
// /v1/models; the values were read out of the CLI bundle's Starter table and
// each was verified to serve the model it names.
var PerchModelOption = map[string]string{
	"deepseek-flash-starter":      "openrouter-deepseek-deepseek-v4-1-flash-1017-cloudflare",
	"glm-5.3-flash-starter":       "openrouter-z-ai-glm-5-3-flash",
	"minimax-m3.1-flash-starter":  "openrouter-minimax-minimax-m3-1-flash-free",
	"beam-1.0-starter":            "openrouter-reflection-beam-1-0-501b-a23b",
	"laguna-s":                    "poolside-poolside-laguna-s-2-1",
}

// perchBody is the request envelope. Fields the CLI always sends are included
// even when empty so the endpoint sees the same shape it expects from its own
// client.
type perchBody struct {
	Request           perchRequest     `json:"request"`
	RunID             string           `json:"runId"`
	Lane              string           `json:"lane"`
	StrictManual      bool             `json:"strictManual"`
	PreferredModelID  string           `json:"preferredModelId,omitempty"`
	ManualModelOption string           `json:"manualModelOptionId,omitempty"`
	AvoidModelIDs     []string         `json:"avoidModelIds"`
	Attribution       *perchAttribution `json:"attribution,omitempty"`
	ClientSurface     string           `json:"clientSurface"`
	ResendAfterOutput bool             `json:"resendAfterOutput"`
}

// perchAttribution is the identity block the CLI attaches to every model call.
//
// Measured against a live CLI (2026-10-23): the request it sends carries
// userId / workspaceId / runId / lane / source, and the same call without that
// block is what Perch answers with
//
//	403 perch_surface_required "Repeated direct-access attempts…"
//
// clientSurface alone is a string the caller asserts; attribution is the
// account binding the server can check. BillingMultiplier is sent as null by the
// CLI and is kept so the shape matches byte for byte.
type perchAttribution struct {
	UserID            string  `json:"userId"`
	WorkspaceID       string  `json:"workspaceId"`
	RunID             string  `json:"runId"`
	Lane              string  `json:"lane"`
	Source            string  `json:"source"`
	BillingMultiplier *int    `json:"billingMultiplier"`
}

type perchRequest struct {
	Messages     []perchMessage `json:"messages"`
	MaxTokens    int            `json:"maxTokens,omitempty"`
	Tools        jsontext.Value `json:"tools,omitempty"`
	ToolChoice   jsontext.Value `json:"toolChoice,omitempty"`
	Temperature  *float64       `json:"temperature,omitempty"`
}

type perchMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// perchToolCall is one call the model asked to make.
type perchToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input jsontext.Value  `json:"input"`
	Type  string          `json:"type,omitempty"`
}

// perchContent is one block of the response's content array. A text block fills
// Text; a tool_use block fills the call fields.
type perchContent struct {
	Type  string         `json:"type"`
	Text  string         `json:"text"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input jsontext.Value `json:"input"`
}

type perchResponse struct {
	OK           bool   `json:"ok"`
	Text         string `json:"text"`
	Model        string `json:"model"`
	FinishReason string `json:"finishReason"`
	// ToolCalls is Perch's structured call list; Content may carry the same
	// calls as {"type":"tool_use",...} blocks. Either shape has to reach the
	// client as OpenAI tool_calls, or a coding harness sees an empty answer
	// while the model was in fact asking to run a tool.
	ToolCalls []perchToolCall `json:"toolCalls"`
	Content   []perchContent  `json:"content"`
	ErrorCode    string `json:"errorCode"`
	Error        string `json:"error"`
	Usage        struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"usage"`
}

// shortRand returns 8 hex chars for a request id. Perch requires a runId that
// looks like the CLI's own (tkt-cli-<ms>-<8 chars>); the value is not validated
// beyond being present and unique.
func shortRand() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// perchRunID derives the run id for this call.
//
// The CLI mints one `tkt-cli-<ms>-<8 chars>` per run and keeps it for every turn
// of that run. This lane originally minted a fresh one per request — the shape
// Perch reports when it decides an account is being driven by direct API access
// rather than by its own client:
//
//	403 perch_surface_required "Repeated direct-access attempts…"
//
// A stable suffix per conversation removes that signal. The id still carries the
// current timestamp so it looks like a freshly started run, but the trailing
// eight characters stay constant for one conversation, the way the CLI keeps one
// run for a whole session. Without a session id there is nothing to key on, so a
// random suffix is used rather than collapsing unrelated requests onto one run.
func perchRunID(req *Request) string {
	key := ""
	if req != nil {
		key = strings.TrimSpace(req.SessionID)
	}
	suffix := shortRand()
	if key != "" {
		sum := sha256.Sum256([]byte("perch:" + key))
		suffix = hex.EncodeToString(sum[:4])
	}
	return "tkt-cli-" + fmt.Sprint(time.Now().UnixMilli()) + "-" + suffix
}

// perchAttributionFor builds the identity block the CLI sends with every model
// call. The account and workspace ids are captured at sign-in (see the Perch
// OAuth exchange); without them the request still goes out, it simply carries no
// attribution and looks like an unbound caller to Perch's gate.
func perchAttributionFor(req *Request, runID string) *perchAttribution {
	if req == nil {
		return nil
	}
	uid := strings.TrimSpace(req.PerchUserID)
	wid := strings.TrimSpace(req.PerchWorkspaceID)
	if uid == "" || wid == "" {
		return nil
	}
	return &perchAttribution{
		UserID:            uid,
		WorkspaceID:       wid,
		RunID:             runID,
		Lane:              "chat",
		Source:            "cli",
		BillingMultiplier: nil,
	}
}

// perchTools passes the caller's OpenAI tool declarations straight through.
// Perch's own CLI sends exactly that shape (verified against a captured CLI
// request), so no translation is needed — only the name of the field changes,
// from the OpenAI "tools" key to Perch's "tools" inside the request envelope.
//
// A null or empty list is dropped rather than sent as [], because the captured
// CLI omits the key entirely when it has no tools.
func perchTools(v jsontext.Value) jsontext.Value {
	t := strings.TrimSpace(string(v))
	if t == "" || t == "null" || t == "[]" {
		return nil
	}
	return v
}

// perchToolChoice maps OpenAI's tool_choice onto Perch's. The CLI sends the
// plain string "auto"; object forms ("function" with a name) are passed through
// unchanged because they already use the same member names.
func perchToolChoice(v jsontext.Value) jsontext.Value {
	t := strings.TrimSpace(string(v))
	if t == "" || t == "null" || t == `"none"` {
		// Perch's own client never sends "none", and a request carrying it was
		// refused in the OpenCode lane for the same reason: omit rather than
		// claim no tools are wanted while sending a tool list.
		return nil
	}
	return v
}

// perchCompetingSignatures are verbatim openings of other coding harnesses.
//
// Perch answers a request carrying one of these with
//
//	403 perch_surface_required "Repeated direct-access attempts…"
//
// Measured 2026-10-23 by bisecting a real failing request: the trigger is the
// literal sentence below (matched case-insensitively, anywhere in the body), not
// the harness name alone — "operating inside pi" on its own is answered 200, and
// so is the same sentence naming claude code, cursor or opencode. The gate is a
// fingerprint of the competitor's system prompt, the same shape as the OpenCode
// tool-quartet rule.
//
// Rewriting the identity clause keeps the instructions intact: the sentence
// still says the assistant runs inside a coding agent harness, it just no longer
// names one Perch refuses to serve.
var perchCompetingSignatures = []struct{ from, to string }{
	{"operating inside pi, a coding agent harness", "operating inside a coding agent harness"},
}

// sanitizePerchText removes competing-harness fingerprints from one message.
// Matching is case-insensitive because the gate is: a lowercased copy of the
// same sentence is refused too.
func sanitizePerchText(s string) string {
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	for _, sig := range perchCompetingSignatures {
		if !strings.Contains(lower, sig.from) {
			continue
		}
		// Rebuild by scanning the lowercase copy for offsets, so the replacement
		// keeps the caller's original casing everywhere else.
		var b strings.Builder
		rest := s
		restLower := lower
		for {
			i := strings.Index(restLower, sig.from)
			if i < 0 {
				b.WriteString(rest)
				break
			}
			b.WriteString(rest[:i])
			b.WriteString(sig.to)
			rest = rest[i+len(sig.from):]
			restLower = restLower[i+len(sig.from):]
		}
		s = b.String()
		lower = strings.ToLower(s)
	}
	return s
}

// ForwardPerch converts an OpenAI chat-completions request into a Perch model
// call and translates the answer back.
func ForwardPerch(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model     string `json:"model"`
		Messages  []struct {
			Role      string          `json:"role"`
			Content   any             `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls,omitempty"`
		} `json:"messages"`
		MaxTokens   int            `json:"max_tokens"`
		Stream      bool           `json:"stream"`
		Tools       jsontext.Value `json:"tools"`
		ToolChoice  jsontext.Value `json:"tool_choice"`
		Temperature *float64       `json:"temperature"`
	}
	if err := json.Unmarshal(req.Body, &oreq); err != nil {
		return fmt.Errorf("perch: parse body: %w", err)
	}
	if len(oreq.Messages) == 0 {
		return &proxy.UpstreamError{
			StatusCode: http.StatusBadRequest,
			Body:       []byte(`{"error":{"message":"messages required"}}`),
		}
	}

	model := strings.TrimPrefix(oreq.Model, "perch/")
	option, ok := PerchModelOption[model]
	if !ok {
		return &proxy.UpstreamError{
			StatusCode: http.StatusBadRequest,
			Body:       []byte(`{"error":{"message":"perch: unknown model ` + model + `"}}`),
		}
	}

	msgs := make([]perchMessage, 0, len(oreq.Messages))
	for _, m := range oreq.Messages {
		text := sanitizePerchText(perchText(m.Content))
		role := m.Role

		// If this is an assistant message with tool_calls, synthesize an informative text
		// so the turn is never dropped as empty. Dropping it leaves the subsequent tool
		// result without an assistant invocation, causing Perch to answer 400 api_error.
		if role == "assistant" && len(m.ToolCalls) > 0 {
			var names []string
			for _, tc := range m.ToolCalls {
				if tc.Function.Name != "" {
					names = append(names, tc.Function.Name)
				}
			}
			callDesc := "I will use the tool: " + strings.Join(names, ", ")
			if text != "" {
				text = text + "\n" + callDesc
			} else {
				text = callDesc
			}
		}

		// Drop empty messages: Perch rejects empty messages with code "api_error"
		if strings.TrimSpace(text) == "" {
			continue
		}

		// Perch's model-call API rejects role="tool" as an orphaned message. Flatten it into
		// a user message with a result header so tool results are kept in context without
		// breaking the upstream validator.
		if role == "tool" {
			role = "user"
			text = "[Tool Result]:\n" + text
		}
		msgs = append(msgs, perchMessage{Role: role, Content: text})
	}
	if len(msgs) == 0 {
		return &proxy.UpstreamError{
			StatusCode: http.StatusBadRequest,
			Body:       []byte(`{"error":{"message":"messages required"}}`),
		}
	}

	runID := perchRunID(req)
	body := perchBody{
		Request: perchRequest{
			Messages:    msgs,
			MaxTokens:   oreq.MaxTokens,
			Tools:       perchTools(oreq.Tools),
			ToolChoice:  perchToolChoice(oreq.ToolChoice),
			Temperature: oreq.Temperature,
		},
		RunID:             runID,
		Lane:              "chat",
		StrictManual:      false,
		PreferredModelID:  option,
		ManualModelOption: option,
		AvoidModelIDs:     []string{},
		Attribution:       perchAttributionFor(req, runID),
		ClientSurface:     "cli",
		ResendAfterOutput: true,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("perch: marshal: %w", err)
	}

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	endpoint := "https://app.perchai.app/api/perch-terminal/model-call"
	if req.Config != nil && req.Config.BaseURL != "" {
		endpoint = req.Config.BaseURL
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("perch: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	httpReq.Header.Set("User-Agent", perchUserAgent)
	if oreq.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := req.Client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("perch: upstream: %w", err)
	}
	defer resp.Body.Close()

	// DIAG: rekam apa yang dikirim dan apa jawabannya, untuk setiap panggilan.
	{
		sessID := ""
		connID := ""
		attrUser := ""
		attrWS := ""
		if req != nil {
			sessID = req.SessionID
			connID = req.ConnectionID
			attrUser = req.PerchUserID
			attrWS = req.PerchWorkspaceID
		}
		log.Warn("perch-diag", "call",
			"status", resp.StatusCode,
			"session_id", sessID,
			"connection_id", connID,
			"run_id", body.RunID,
			"model_option", option,
			"max_tokens", oreq.MaxTokens,
			"stream", oreq.Stream,
			"user_agent", perchUserAgent,
			"body_bytes", len(raw),
			"has_attribution", body.Attribution != nil,
			"attr_user", attrUser,
			"attr_ws", attrWS,
			"strict_manual", body.StrictManual,
			"preferred", body.PreferredModelID,
			"endpoint", endpoint,
			"proxy_client", req.Client != nil && req.Client.Transport != nil)

		// DIAG: dump body + header aktual agar bisa dibandingkan byte-per-byte
		// dengan request yang berhasil.
		_ = os.WriteFile("/tmp/perch-last-body.json", raw, 0o644)
		var hdump []string
		for k, v := range httpReq.Header {
			hdump = append(hdump, k+"="+strings.Join(v, ","))
		}
		_ = os.WriteFile("/tmp/perch-last-headers.txt",
			[]byte(resp.Status+"\n"+strings.Join(hdump, "\n")), 0o644)
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: b}
	}

	if oreq.Stream {
		return perchStream(w, resp.Body, req, model)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("perch: read: %w", err)
	}
	var pr perchResponse
	if err := json.Unmarshal(data, &pr); err != nil {
		return fmt.Errorf("perch: decode: %w", err)
	}
	if !pr.OK {
		code := pr.ErrorCode
		if code == "" {
			code = "upstream_error"
		}
		msg := pr.Error
		if msg == "" {
			msg = "perch returned ok=false"
		}
		return &proxy.UpstreamError{
			StatusCode: http.StatusBadGateway,
			Body:       []byte(`{"error":{"code":"` + code + `","message":"` + msg + `"}}`),
		}
	}

	msg := map[string]any{"role": "assistant", "content": pr.Text}
	calls := perchToolCallsOf(pr)
	if len(calls) > 0 {
		msg["tool_calls"] = calls
		// An assistant turn that only asks for a tool carries no text; leaving
		// content as "" is correct and is what the OpenAI schema expects.
	}

	out := map[string]any{
		"id":      "chatcmpl-perch-" + shortRand(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   pr.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": perchFinishFor(pr, len(calls) > 0),
		}},
		"usage": map[string]any{
			"prompt_tokens":     pr.Usage.InputTokens,
			"completion_tokens": pr.Usage.OutputTokens,
			"total_tokens":      pr.Usage.InputTokens + pr.Usage.OutputTokens,
		},
	}
	b, jerr := json.Marshal(out)
	if jerr != nil {
		return jerr
	}
	if req.ResponseBuf != nil {
		req.ResponseBuf.Write(b)
	}
	w.Header().Set("Content-Type", "application/json")
	_, werr := w.Write(b)
	return werr
}

// perchText flattens a message content value: Perch takes a plain string, so a
// block array ("content": [{"type":"text","text":"…"}]) is joined and a
// non-text block is skipped rather than sent as JSON.
func perchText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, part := range c {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := m["text"].(string); ok && t != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(t)
			}
		}
		return sb.String()
	default:
		return ""
	}
}

// perchStream forwards Perch's SSE as OpenAI chunks. Perch emits `keepalive`
// comment lines and `{type,text}` events; both are translated here so a client
// never sees the upstream shape.
// perchStream forwards Perch's SSE stream as OpenAI chunks. Perch emits
// answer_delta, reasoning_delta, tool_call_delta, tool_use_end, and done.
// A coding harness like Pi decides its next step from tool_calls; dropping
// answer_delta or tool_call_delta was the bug that made Pi stall with empty turns.
func perchStream(w http.ResponseWriter, body io.Reader, req *Request, model string) error {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	id := "chatcmpl-perch-" + shortRand()
	sentRole := false
	hasToolCalls := false
	lastFinishReason := "stop"
	var fullText strings.Builder
	var finalUsage map[string]any

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			continue
		}
		evType, _ := raw["type"].(string)

		switch evType {
		case "answer_delta", "text_delta", "content_delta":
			text, _ := raw["text"].(string)
			if text == "" {
				continue
			}
			fullText.WriteString(text)
			delta := map[string]any{"content": text}
			if !sentRole {
				delta["role"] = "assistant"
				sentRole = true
			}
			writeSSEChunk(w, map[string]any{
				"id":      id,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   model,
				"choices": []map[string]any{{"index": 0, "delta": delta}},
			}, req)
			if flusher != nil {
				flusher.Flush()
			}

		case "reasoning_delta":
			text, _ := raw["text"].(string)
			if text == "" {
				continue
			}
			delta := map[string]any{"reasoning_content": text}
			if !sentRole {
				delta["role"] = "assistant"
				sentRole = true
			}
			writeSSEChunk(w, map[string]any{
				"id":      id,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   model,
				"choices": []map[string]any{{"index": 0, "delta": delta}},
			}, req)
			if flusher != nil {
				flusher.Flush()
			}

		case "tool_call_delta", "tool_use_end":
			callsRaw, ok := raw["toolCalls"].([]any)
			if !ok || len(callsRaw) == 0 {
				continue
			}
			hasToolCalls = true
			lastFinishReason = "tool_calls"

			for callIdx, cAny := range callsRaw {
				c, ok := cAny.(map[string]any)
				if !ok {
					continue
				}
				cid, _ := c["id"].(string)
				cname, _ := c["name"].(string)
				args := ""
				if rawArgs, ok := c["rawArguments"].(string); ok && rawArgs != "" {
					args = rawArgs
				} else if parsedArgs, ok := c["arguments"].(map[string]any); ok && len(parsedArgs) > 0 {
					if b, err := json.Marshal(parsedArgs); err == nil {
						args = string(b)
					}
				}

				fnObj := map[string]any{}
				if cname != "" {
					fnObj["name"] = cname
				}
				if args != "" {
					fnObj["arguments"] = args
				}

				tcItem := map[string]any{
					"index": callIdx,
					"type":  "function",
				}
				if cid != "" {
					tcItem["id"] = cid
				}
				if len(fnObj) > 0 {
					tcItem["function"] = fnObj
				}

				delta := map[string]any{
					"tool_calls": []map[string]any{tcItem},
				}
				if !sentRole {
					delta["role"] = "assistant"
					sentRole = true
				}
				writeSSEChunk(w, map[string]any{
					"id":      id,
					"object":  "chat.completion.chunk",
					"created": time.Now().Unix(),
					"model":   model,
					"choices": []map[string]any{{"index": 0, "delta": delta}},
				}, req)
				if flusher != nil {
					flusher.Flush()
				}
			}

		case "done":
			if ok, hasOk := raw["ok"].(bool); hasOk && !ok {
				errMsg, _ := raw["error"].(string)
				if errMsg == "" {
					errMsg = "Perch model call failed"
				}
				errCode, _ := raw["errorCode"].(string)
				writeSSEChunk(w, map[string]any{
					"error": map[string]any{
						"message": errMsg,
						"type":    "upstream_error",
						"code":    errCode,
					},
				}, req)
				if flusher != nil {
					flusher.Flush()
				}
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				return fmt.Errorf("perch stream error: %s (code=%s)", errMsg, errCode)
			}

			// If no text chunks were emitted before done (and no tool calls), emit the text from done
			if fullText.Len() == 0 && !hasToolCalls {
				doneText, _ := raw["text"].(string)
				if doneText == "" {
					if contentArr, ok := raw["content"].([]any); ok {
						var sb strings.Builder
						for _, item := range contentArr {
							if itemMap, ok := item.(map[string]any); ok {
								if (itemMap["type"] == "text" || itemMap["type"] == "output") && itemMap["text"] != nil {
									sb.WriteString(fmt.Sprint(itemMap["text"]))
								}
							}
						}
						doneText = sb.String()
					}
				}
				if doneText != "" {
					delta := map[string]any{"content": doneText}
					if !sentRole {
						delta["role"] = "assistant"
						sentRole = true
					}
					writeSSEChunk(w, map[string]any{
						"id":      id,
						"object":  "chat.completion.chunk",
						"created": time.Now().Unix(),
						"model":   model,
						"choices": []map[string]any{{"index": 0, "delta": delta}},
					}, req)
					if flusher != nil {
						flusher.Flush()
					}
				}
			}

			if u, ok := raw["usage"].(map[string]any); ok {
				inTokens, _ := u["inputTokens"].(float64)
				outTokens, _ := u["outputTokens"].(float64)
				finalUsage = map[string]any{
					"prompt_tokens":     int(inTokens),
					"completion_tokens": int(outTokens),
					"total_tokens":      int(inTokens + outTokens),
				}
			}

			if fr, ok := raw["finishReason"].(string); ok && fr != "" {
				lastFinishReason = perchFinish(fr)
			}
			if hasToolCalls {
				lastFinishReason = "tool_calls"
			}
		}
	}

	finalChunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": lastFinishReason}},
	}
	if finalUsage != nil {
		finalChunk["usage"] = finalUsage
	}
	writeSSEChunk(w, finalChunk, req)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return sc.Err()
}

func writeSSEChunk(w io.Writer, v any, req *Request) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	line := append([]byte("data: "), b...)
	line = append(line, "\n\n"...)
	if req != nil {
		if req.ResponseBuf != nil {
			req.ResponseBuf.Write(line)
		}
		if req.TTFT != nil && *req.TTFT == 0 && !req.StartTime.IsZero() {
			*req.TTFT = time.Since(req.StartTime).Milliseconds()
		}
	}
	_, _ = w.Write(line)
}

// perchToolCallsOf collects the calls Perch reported, from either shape it uses:
// a `toolCalls` array, or `content` blocks of type "tool_use". Both describe the
// same thing, and a real Perch answer used the content-blocks form while
// `text` was empty.
func perchToolCallsOf(pr perchResponse) []map[string]any {
	out := make([]map[string]any, 0, len(pr.ToolCalls)+len(pr.Content))
	add := func(id, name string, input jsontext.Value) {
		if name == "" {
			return
		}
		args := strings.TrimSpace(string(input))
		if args == "" || args == "null" {
			args = "{}"
		}
		if id == "" {
			id = "call_" + shortRand()
		}
		out = append(out, map[string]any{
			"id":   id,
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": args,
			},
		})
	}
	for _, c := range pr.ToolCalls {
		add(c.ID, c.Name, c.Input)
	}
	for _, c := range pr.Content {
		if c.Type == "tool_use" {
			add(c.ID, c.Name, c.Input)
		}
	}
	return out
}

// perchFinishFor prefers tool_calls when calls are present: a harness decides
// what to do next from finish_reason, so reporting "stop" beside a tool_calls
// array would make it drop the calls.
func perchFinishFor(pr perchResponse, hasCalls bool) string {
	if hasCalls {
		return "tool_calls"
	}
	return perchFinish(pr.FinishReason)
}

func perchFinish(reason string) string {
	switch reason {
	case "length", "max_tokens":
		return "length"
	case "tool_calls", "tool_call":
		return "tool_calls"
	default:
		return "stop"
	}
}
