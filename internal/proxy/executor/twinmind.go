package executor

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	jsontext "encoding/json/jsontext"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/proxy"
)

// TwinMind wraps Google/OpenAI/Anthropic models behind one chat endpoint:
// POST /api/chat, which answers with a narrow SSE event stream
// (run_start/thinking_start/thinking_delta/text_start/text_delta/done) rather
// than the OpenAI chunk shape. The client speaks OpenAI; this executor
// flattens the messages into TwinMind's single `query` string, picks the
// model, then translates the event stream back into OpenAI chunks.
//
// TwinMind keeps its own cross-session memory of the account, so the request
// is sent without a session id and the reply still follows the conversation.

// twinmindChatPath is fixed on the app host; the registry BaseURL already
// points at it, but the executor treats BaseURL as the endpoint (no path join
// happens on this provider).
const twinmindChatPath = "https://app.twinmind.com/api/chat"

// twinmindUserAgent mirrors the web client's UA; the upstream rejects some
// non-browser default agents.
const twinmindUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36"

// ForwardTwinMind converts an OpenAI chat-completions request into a TwinMind
// chat turn and translates the streamed reply back.
func ForwardTwinMind(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model    string           `json:"model"`
		Messages []jsontext.Value `json:"messages"`
	}
	if err := json.Unmarshal(req.Body, &oreq); err != nil {
		return fmt.Errorf("twinmind: parse body: %w", err)
	}
	if len(oreq.Messages) == 0 {
		return &proxy.UpstreamError{StatusCode: http.StatusBadRequest, Body: []byte(`{"error":{"message":"messages required"}}`)}
	}

	query := twinmindFlattenMessages(oreq.Messages)
	if strings.TrimSpace(query) == "" {
		return &proxy.UpstreamError{StatusCode: http.StatusBadRequest, Body: []byte(`{"error":{"message":"no user content to send"}}`)}
	}

	payload := map[string]any{
		"type":             "app",
		"version":          1,
		"response_version": 1,
		"query":            query,
		"mode":             "default",
		"model":            twinmindModelPayload(oreq.Model),
		"context":          nil,
		"client": map[string]any{
			"platform":    "web",
			"timezone":    "Asia/Jakarta",
			"client_time": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			"locale":      "id-ID",
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("twinmind: encode payload: %w", err)
	}

	headers := map[string]string{
		"Authorization": "Bearer " + req.APIKey,
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"User-Agent":    twinmindUserAgent,
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// The registry BaseURL is the chat endpoint; a connection may override it
	// (tests do), so only fall back to the constant when it is unset.
	target := req.Config.BaseURL
	if target == "" {
		target = twinmindChatPath
	}
	resp, err := proxy.DoRequest(ctx, req.Client, "POST", target, headers, body)
	if err != nil {
		return fmt.Errorf("twinmind: upstream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: b}
	}

	// Upstream is always SSE regardless of the client's stream flag; the
	// non-stream path just accumulates the same events into one completion.
	return twinmindStreamResponse(w, req, resp.Body, oreq.Model)
}

// twinmindFlattenMessages renders the OpenAI message list as one TwinMind
// query. The chat engine takes a single string, so the transcript is serialised
// with role prefixes and the last user turn last, which is what a chat
// completion at the end of a conversation reads like.
func twinmindFlattenMessages(messages []jsontext.Value) string {
	type msg struct {
		Role    string          `json:"role"`
		Content jsontext.Value  `json:"content"`
	}
	var b strings.Builder
	for i, raw := range messages {
		var m msg
		if err := json.Unmarshal([]byte(raw), &m); err != nil || m.Content == nil {
			continue
		}
		text := twinmindContentText(m.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if i > 0 {
			b.WriteString("\n\n")
		}
		switch strings.ToLower(m.Role) {
		case "system", "developer":
			b.WriteString("[system]\n" + text)
		case "assistant":
			b.WriteString("[assistant]\n" + text)
		default:
			b.WriteString(text)
		}
	}
	return b.String()
}

// twinmindContentText extracts plain text from an OpenAI content field, which
// is either a string or an array of {type:"text", text:"..."} parts. Non-text
// parts (images) contribute nothing on this provider.
func twinmindContentText(content jsontext.Value) string {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	var out strings.Builder
	for _, p := range parts {
		if p.Text != "" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(p.Text)
		}
	}
	return out.String()
}

// twinmindModelPayload maps the requested model onto TwinMind's payload shape:
// the literal "auto", or {"model_name": <id>} for an explicit pick. "auto" is
// TwinMind's own free default and rides as a plain string.
//
// Imported ids carry the vendor prefix the catalogue adds to keep same-named
// models distinct (google/gemini-3.7-flash); TwinMind itself only knows the
// bare name, so the prefix is stripped before it is sent. A 422 here surfaced
// as an unreadable gzip body, which is why the prefix must not leak upstream.
func twinmindModelPayload(model string) any {
	m := strings.TrimSpace(model)
	if m == "" || m == "auto" {
		return "auto"
	}
	if i := strings.IndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	return map[string]any{"model_name": m}
}

// twinmindStreamResponse reads the upstream SSE and writes either OpenAI
// chat.completion.chunk lines (stream) or one chat.completion object (not).
func twinmindStreamResponse(w http.ResponseWriter, req *Request, upstream io.Reader, model string) error {
	responseID := fmt.Sprintf("chatcmpl-twinmind-%d", time.Now().UnixMilli())
	created := time.Now().Unix()

	var (
		thinking strings.Builder
		text     strings.Builder
		errSeen  *twinmindErr
	)

	base := func(choices []map[string]any) map[string]any {
		if choices == nil {
			choices = []map[string]any{}
		}
		return map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": choices,
		}
	}
	var emit func(chunk map[string]any) error
	emit = func(chunk map[string]any) error {
		if req.TTFT != nil && *req.TTFT == 0 {
			*req.TTFT = time.Since(req.StartTime).Milliseconds()
		}
		if req.ResponseBuf != nil {
			if c, ok := chunk["choices"].([]map[string]any); ok && len(c) > 0 {
				if d, ok := c[0]["delta"].(map[string]any); ok {
					if s, ok := d["content"].(string); ok {
						req.ResponseBuf.Write([]byte(s))
					}
				}
			}
		}
		b, jerr := json.Marshal(chunk)
		if jerr != nil {
			return jerr
		}
		if _, werr := w.Write([]byte("data: " + string(b) + "\n\n")); werr != nil {
			return werr
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return nil
	}

	// The stream flag decides whether text deltas are emitted as OpenAI chunks
	// or accumulated for one completion object; the upstream itself is always
	// SSE. emit is nil on the non-stream path.
	if !req.IsStream {
		emit = nil
	}

	onEvent := func(emit func(map[string]any) error) func(string, jsontext.Value) (bool, error) {
		return func(ev string, data jsontext.Value) (bool, error) {
			var e twinmindEvent
			if jerr := json.Unmarshal([]byte(data), &e); jerr != nil {
				return false, nil // unknown event shape: skip, keep reading
			}
			switch e.Type {
			case "error":
				errSeen = &twinmindErr{Message: e.Content}
				return true, nil
			// Upstream opens a text block with the first slice already in
			// text_start and only then streams text_delta. Reading just the
			// deltas dropped that opening slice, so a short answer ("PONG")
			// arrived empty while a long one merely lost its first sentence.
			case "text_start", "text_delta":
				if e.Content == "" {
					break // the closing empty delta only marks end-of-text
				}
				text.WriteString(e.Content)
				if emit != nil {
					if jerr := emit(base([]map[string]any{{"index": 0, "delta": map[string]any{"content": e.Content}, "finish_reason": nil}})); jerr != nil {
						return false, jerr
					}
				}
			// Same shape for the reasoning block of a thinking model.
			case "thinking_start", "thinking_delta":
				thinking.WriteString(e.Content)
			}
			return e.Type == "done" || e.Type == "error", nil
		}
	}

	if err := twinmindReadSSE(upstream, onEvent(emit)); err != nil {
		return &proxy.UpstreamError{StatusCode: http.StatusBadGateway, Body: twinmindJSONError("twinmind: " + err.Error())}
	}
	if errSeen != nil {
		return &proxy.UpstreamError{StatusCode: http.StatusBadGateway, Body: twinmindJSONError("twinmind: "+errSeen.Message)}
	}

	if req.IsStream {
		// Reasoning, when the model produced any, rides the standard
		// reasoning_content delta so OpenAI-compatible clients can show it.
		if thinking.Len() > 0 {
			if err := emit(base([]map[string]any{{"index": 0, "delta": map[string]any{"reasoning_content": thinking.String()}, "finish_reason": nil}})); err != nil {
				return err
			}
		}
		if err := emit(base([]map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}})); err != nil {
			return err
		}
		_, err := w.Write([]byte("data: [DONE]\n\n"))
		return err
	}

	// Non-streaming: assemble one chat.completion from the accumulated text.
	out := map[string]any{
		"id":      responseID,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":        0,
			"message":      map[string]any{"role": "assistant", "content": text.String()},
			"finish_reason": "stop",
		}},
	}
	if thinking.Len() > 0 {
		out["choices"].([]map[string]any)[0]["message"].(map[string]any)["reasoning_content"] = thinking.String()
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

// twinmindEvent is one upstream SSE data payload. The content field carries
// text for the *delta events and an error message for "error".
type twinmindEvent struct {
	Type        string `json:"type"`
	Content     string `json:"content"`
	SessionID   string `json:"session_id"`
	SequenceNum int    `json:"sequence_num"`
}

type twinmindErr struct {
	Message string
}

// twinmindReadSSE parses TwinMind's SSE framing. Events arrive as "data: {json}"
// lines with no "event:" name line; the type lives inside the JSON payload.
func twinmindReadSSE(upstream io.Reader, onEvent func(string, jsontext.Value) (bool, error)) error {
	reader := bufio.NewReader(upstream)
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if payload == "" {
				break
			}
			var data jsontext.Value
			if jerr := json.Unmarshal([]byte(payload), &data); jerr != nil {
				break
			}
			var ev struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(data), &ev)
			stop, eerr := onEvent(ev.Type, data)
			if eerr != nil {
				return eerr
			}
			if stop {
				return nil
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
	return nil
}

func twinmindJSONError(message string) []byte {
	b, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "api_error", "code": ""},
	})
	return b
}
