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
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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
	Request            perchRequest `json:"request"`
	RunID              string       `json:"runId"`
	Lane               string       `json:"lane"`
	StrictManual       bool         `json:"strictManual"`
	ManualModelOption  string       `json:"manualModelOptionId,omitempty"`
	AvoidModelIDs      []string     `json:"avoidModelIds"`
	ClientSurface      string       `json:"clientSurface"`
	ResendAfterOutput  bool         `json:"resendAfterOutput"`
}

type perchRequest struct {
	Messages  []perchMessage `json:"messages"`
	MaxTokens int            `json:"maxTokens,omitempty"`
}

type perchMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type perchResponse struct {
	OK           bool   `json:"ok"`
	Text         string `json:"text"`
	Model        string `json:"model"`
	FinishReason string `json:"finishReason"`
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

// ForwardPerch converts an OpenAI chat-completions request into a Perch model
// call and translates the answer back.
func ForwardPerch(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model     string `json:"model"`
		Messages  []struct {
			Role    string          `json:"role"`
			Content any             `json:"content"`
		} `json:"messages"`
		MaxTokens int  `json:"max_tokens"`
		Stream    bool `json:"stream"`
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
		msgs = append(msgs, perchMessage{Role: m.Role, Content: perchText(m.Content)})
	}

	body := perchBody{
		Request:           perchRequest{Messages: msgs, MaxTokens: oreq.MaxTokens},
		RunID:             "tkt-cli-" + fmt.Sprint(time.Now().UnixMilli()) + "-" + shortRand(),
		Lane:              "chat",
		StrictManual:      true,
		ManualModelOption: option,
		AvoidModelIDs:     []string{},
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

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: b}
	}

	if oreq.Stream {
		return perchStream(w, resp.Body, model)
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

	out := map[string]any{
		"id":      "chatcmpl-perch-" + shortRand(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   pr.Model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": pr.Text,
			},
			"finish_reason": perchFinish(pr.FinishReason),
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
func perchStream(w http.ResponseWriter, body io.Reader, model string) error {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	id := "chatcmpl-perch-" + shortRand()
	sentRole := false
	var full strings.Builder

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		if ev.Type != "text_delta" && ev.Type != "content_delta" || ev.Text == "" {
			continue
		}
		full.WriteString(ev.Text)
		delta := map[string]any{"content": ev.Text}
		if !sentRole {
			delta["role"] = "assistant"
			sentRole = true
		}
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta}},
		}
		writeSSEChunk(w, chunk)
		if flusher != nil {
			flusher.Flush()
		}
	}

	final := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
	}
	writeSSEChunk(w, final)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return sc.Err()
}

func writeSSEChunk(w io.Writer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, "data: ")
	_, _ = w.Write(b)
	_, _ = io.WriteString(w, "\n\n")
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
