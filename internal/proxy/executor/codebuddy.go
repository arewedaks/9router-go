package executor

import (
	"bytes"
	"crypto/sha1"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"

	"github.com/google/uuid"
)

// ForwardCodebuddyCN forwards to Tencent CodeBuddy with force-stream
// and reasoning_summary injection.
//
// CodeBuddy is OpenAI-compatible but rejects non-stream chat requests
// (HTTP 400, code 11101 "Non-stream chat request is currently not supported").
// This executor forces stream=true in the request body, mirroring the
// JS version's CodeBuddyExecutor.transformRequest behaviour.
//
// It also handles reasoning_effort:
//   - "none"/"off" → stripped (CodeBuddy gateway has no "none")
//   - any other value → injects reasoning_summary="auto" so CodeBuddy
//     surfaces the model's reasoning output.
func ForwardCodebuddyCN(w http.ResponseWriter, req *Request) error {
	// Same identity fingerprint as the intl lane: the CN gateway blocks the
	// account when the client headers do not match the official CLI either.
	return forwardCodebuddyWithIdentity(w, req)
}

// ForwardCodebuddyIntl handles codebuddy-intl and workbuddy. The intl gateway
// added "channel verification" (issue #4892): requests without the
// X-Conversation-* header family are rejected with HTTP 400 code 11128
// "conversation id missing". codebuddy-cn is not gated yet.
//
// Full identity headers are required to avoid account blocking: the gateway
// fingerprints client identity via User-Agent, X-IDE-Type, X-Product, etc.
// Missing or wrong values trigger "unapproved channel" blocks (code 11128).
func ForwardCodebuddyIntl(w http.ResponseWriter, req *Request) error {
	return forwardCodebuddyWithIdentity(w, req)
}

// forwardCodebuddyWithIdentity is the shared body of both CodeBuddy lanes:
// transform, apply the CLI identity fingerprint, forward as a stream.
func forwardCodebuddyWithIdentity(w http.ResponseWriter, req *Request) error {
	body, err := transformCodebuddyBody(req.Body)
	if err != nil {
		return fmt.Errorf("transform codebuddy body: %w", err)
	}

	cfg := req.Config
	merged := make(map[string]string, len(cfg.StaticHeaders)+25)
	for k, v := range cfg.StaticHeaders {
		merged[k] = v
	}
	delete(merged, "x-requested-with")
	delete(merged, "x-codebuddy-request")

	for k, v := range codebuddyIdentityHeaders(req, cfg, body) {
		merged[k] = v
	}
	cfgCopy := *cfg
	cfgCopy.StaticHeaders = merged
	cfg = &cfgCopy

	ctx := req.Ctx
	resp, err := proxy.ForwardOpenAI(ctx, req.Client, cfg, req.APIKey, body, true)
	if err != nil {
		var ue *proxy.UpstreamError
		if errors.As(err, &ue) && isCodebuddyRateLimit(ue.Body) {
			ue.StatusCode = http.StatusTooManyRequests
		}
		return fmt.Errorf("ForwardCodebuddy upstream: %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, "codebuddy-intl")
		defer stallReader.Close()
		return execSSEStream(w, stallReader, req)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read codebuddy response: %w", err)
	}
	if converted, ok := sseToOpenAIJSON(data); ok {
		data = converted
	} else if isCodebuddyRateLimit(data) {
		return &proxy.UpstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`"CodeBuddy frequency limit"`)}
	}
	return jsonResponse(ctx, w, bytes.NewReader(data), req.TranslateResp, req.ResponseBuf)
}

// codebuddyIdentityHeaders builds the full identity header set for CodeBuddy Intl.
// These headers fingerprint the client as a legitimate CLI tool — missing or
// incorrect values cause account-level blocks (HTTP 400 code 11128).
func codebuddyIdentityHeaders(req *Request, cfg *providers.ProviderConfig, body []byte) map[string]string {
	sessionKey := req.SessionID
	if sessionKey == "" && req.ConnData != nil {
		if m, ok := req.ConnData["sessionId"].(string); ok {
			sessionKey = m
		}
	}
	if sessionKey == "" {
		sessionKey = firstUserText(body)
	}
	var convID string
	if sessionKey != "" {
		convID = stableUuidFrom("codebuddy:" + sessionKey)
	} else {
		convID = uuid.NewString()
	}

	domain := "www.codebuddy.ai"
	if req.ConnData != nil {
		if d, ok := req.ConnData["domain"].(string); ok && d != "" {
			domain = d
		}
	}
	if domain == "www.codebuddy.ai" && cfg != nil && (strings.Contains(cfg.BaseURL, "tencent.com") || strings.Contains(cfg.BaseURL, "copilot.")) {
		domain = "copilot.tencent.com"
	}
	if req.Provider == "codebuddy-cn" && domain == "www.codebuddy.ai" {
		domain = "www.codebuddy.cn"
	}

	return codebuddyIdentityHeadersFor(convID, req.APIKey, domain)
}

// CodebuddyIdentityHeadersFor builds the identity header set for a caller that
// has a stable key (a connection id) but no executor Request, so the per-account
// connection test sends the SAME fingerprint a live chat request sends. A probe
// without it is refused like an unapproved channel and a healthy account looks
// blocked.
func CodebuddyIdentityHeadersFor(stableKey, apiKey, domain string) map[string]string {
	if domain == "" {
		domain = "www.codebuddy.ai"
	}
	return codebuddyIdentityHeadersFor(stableUuidFrom("codebuddy:"+stableKey), apiKey, domain)
}

// codebuddyIdentityHeadersFor is the single source of the CLI fingerprint.
func codebuddyIdentityHeadersFor(convID, apiKey, domain string) map[string]string {
	reqID := strings.ReplaceAll(uuid.NewString(), "-", "")
	convReqID := strings.ReplaceAll(uuid.NewString(), "-", "")
	h := map[string]string{
		"Content-Type":                "application/json; charset=utf-8",
		"User-Agent":                  "CLI/2.108.1 CodeBuddy/2.108.1",
		"X-Product":                   "SaaS",
		"X-App":                       "cli",
		"X-IDE-Type":                  "CLI",
		"X-IDE-Name":                  "CLI",
		"X-IDE-Version":               "2.108.1",
		"X-Private-Data":              "false",
		"X-Requested-With":            "XMLHttpRequest",
		"X-CodeBuddy-Request":         "1",
		"X-Domain":                    domain,
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Runtime-Version": "v24.11.1",
		"X-Stainless-Lang":            "js",
		"X-Stainless-Arch":            "x64",
		"X-Stainless-Os":              "Windows",
		"X-Stainless-Package-Version": "6.25.0",
		"X-Stainless-Helper-Method":   "stream",
		"X-Stainless-Retry-Count":     "0",
		"X-Request-ID":                reqID,
		"X-Conversation-ID":           convID,
		"X-Conversation-Request-ID":   convReqID,
		"X-Conversation-Message-ID":   reqID,
		"X-Agent-Intent":              "craft",
		"X-Agent-Purpose":             "conversation",
	}
	if strings.HasPrefix(apiKey, "ck_") {
		h["Authorization"] = "Bearer " + apiKey
		h["X-API-Key"] = apiKey
		delete(h, "X-Domain")
	}
	return h
}

// isCodebuddyRateLimit detects CodeBuddy rate-limit responses that should be
// promoted to HTTP 429 for fallback retry instead of being treated as hard errors.
func isCodebuddyRateLimit(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var data struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return false
	}
	msg := data.Msg
	if msg == "" {
		msg = data.Message
	}
	if msg == "" {
		msg = data.Error.Message
	}
	return data.Code == 6004 || strings.Contains(strings.ToLower(msg), "frequency limit") || strings.Contains(msg, "超出频率限制") || strings.Contains(msg, "限额")
}

// cloneConfigWithHeaders returns a shallow copy of cfg with extra headers
// merged into StaticHeaders without mutating the original.
func cloneConfigWithHeaders(cfg *providers.ProviderConfig, extra map[string]string) *providers.ProviderConfig {
	c := *cfg
	c.StaticHeaders = make(map[string]string, len(cfg.StaticHeaders)+len(extra))
	for k, v := range cfg.StaticHeaders {
		c.StaticHeaders[k] = v
	}
	for k, v := range extra {
		c.StaticHeaders[k] = v
	}
	return &c
}

// codebuddyConversationHeaders builds the X-Conversation-* headers the intl
// gateway requires (issue #4892). The conversation id is derived stably from
// the session key so retries of the same turn share a conversation, while
// request and message ids are fresh UUIDs.
func codebuddyConversationHeaders(req *Request) map[string]string {
	convID := stableConversationID(req)
	return map[string]string{
		"X-Conversation-ID":         convID,
		"X-Conversation-Request-ID": uuid.New().String(),
		"X-Conversation-Message-ID": uuid.New().String(),
	}
}

// stableConversationID produces a deterministic UUID-v5-shaped id from the
// best available session key, mirroring the JS reference's stableUuidFrom.
func stableConversationID(req *Request) string {
	key := req.SessionID
	if key == "" {
		key = firstUserText(req.Body)
		if key != "" {
			key = "cbai:" + key
		}
	}
	if key == "" {
		return uuid.New().String()
	}
	return stableUuidFrom(key)
}

// stableUuidFrom mirrors the JS reference: SHA-1 → UUID-v5-shaped string.
func stableUuidFrom(key string) string {
	h := sha1.Sum([]byte(key))
	hex := fmt.Sprintf("%x", h)
	// Set version nibble (bits 48-51) to 0101 (v5)
	// Set variant bits (bits 64-65) to 10
	timeHigh := (hexVal(hex[12:16]) & 0x0fff) | 0x5000
	clockSeq := (hexVal(hex[16:20]) & 0x3fff) | 0x8000
	return fmt.Sprintf("%s-%s-%04x-%04x-%s",
		hex[0:8], hex[8:12], timeHigh, clockSeq, hex[20:32])
}

func hexVal(s string) uint16 {
	var v uint16
	for _, c := range s {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint16(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint16(c - 'a' + 10)
		}
	}
	return v
}

// firstUserText extracts the text content of the first user message from a
// JSON chat body, for conversation-id derivation.
func firstUserText(body []byte) string {
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	for _, m := range parsed.Messages {
		if m.Role != "user" {
			continue
		}
		switch v := m.Content.(type) {
		case string:
			return v
		case []any:
			var parts []string
			for _, item := range v {
				if block, ok := item.(map[string]any); ok {
					if t, ok := block["text"].(string); ok {
						parts = append(parts, t)
					}
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

// codebuddyRequiredSystemPrompt is the first message every CodeBuddy request
// must carry. The gateway fingerprints the client on it: a request without this
// exact leading system prompt is rejected as
// `400 code 11128 "Illegal API invocation from an unapproved channel"`,
// regardless of how correct the surrounding headers are. VansRouter seeds the
// same constant in its CodeBuddyIntlExecutor/CodeBuddyExecutor.
const codebuddyRequiredSystemPrompt = "You are CodeBuddy Code."

// claudeCodeBlockedPromptRegex catches Claude Code's first-party identity
// prompt which the CodeBuddy gateway fingerprints and blocks with:
// `400 {"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`.
// Rewriting "official CLI for Claude" -> "CLI for Claude" bypasses the gate
// cleanly while keeping all agent instructions intact.
var claudeCodeBlockedPromptRegex = regexp.MustCompile(`(?i)official\s+CLI\s+for\s+Claude`)

// codebuddyChannelMarkerRegex catches the remaining competitor-CLI fingerprints
// the intl gateway scrubs for (vendor JS reference `sanitizeChannelMarkers`):
// other vendors' agent identity lines, the cc_entrypoint marker, and the
// OhMyOpenCode fork banner. A request that still carries one reads as a
// non-CLI client and the ACCOUNT is blocked, not just the request.
var codebuddyChannelMarkerRegex = regexp.MustCompile(`(?i)you are (?:cursor|windsurf|cline|aider|continue|copilot|cody)|cc_entrypoint\s*=\s*(?:cli|vscode|jetbrains|gui)|OhMyOpenCode`)

func sanitizeCodebuddyPrompt(s string) string {
	if claudeCodeBlockedPromptRegex.MatchString(s) {
		s = claudeCodeBlockedPromptRegex.ReplaceAllString(s, "CLI for Claude")
	}
	if codebuddyChannelMarkerRegex.MatchString(s) {
		s = codebuddyChannelMarkerRegex.ReplaceAllString(s, "[external-client-identity]")
	}
	return s
}

func sanitizeCodebuddyContent(content any) any {
	switch v := content.(type) {
	case string:
		return sanitizeCodebuddyPrompt(v)
	case []any:
		for i, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					m["text"] = sanitizeCodebuddyPrompt(t)
				}
			} else if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok {
					m["text"] = sanitizeCodebuddyPrompt(t)
				}
			}
			v[i] = item
		}
		return v
	default:
		return content
	}
}

// transformCodebuddyBody forces stream=true and handles reasoning params.
func transformCodebuddyBody(body []byte) ([]byte, error) {
	var reqMap map[string]interface{}
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}

	// Force stream — CodeBuddy rejects non-stream (HTTP 400, code 11101)
	reqMap["stream"] = true

	// Handle reasoning_effort / reasoning_summary
	if eff, ok := reqMap["reasoning_effort"].(string); ok {
		switch eff {
		case "none", "off":
			// CodeBuddy gateway has no "none" — just omit
			delete(reqMap, "reasoning_effort")
		default:
			// Client explicitly asked for reasoning — mirror the CLI's
			// reasoning_summary so CodeBuddy surfaces the model's reasoning.
			reqMap["reasoning_summary"] = "auto"
		}
	}

	// Shape the messages the way the official IDE client does. Two things matter
	// to the gateway's client fingerprint, and both were missing before:
	//   1. a leading `You are CodeBuddy Code.` system message;
	//   2. user content as typed blocks (`[{type:"text",text:...}]`) instead of
	//      a bare string.
	// An agent CLI sends the bare string, so its request was refused with
	// `11128 Illegal API invocation from an unapproved channel`.
	if msgs, ok := reqMap["messages"].([]interface{}); ok {
		shaped := make([]interface{}, 0, len(msgs)+1)
		shaped = append(shaped, map[string]interface{}{
			"role":    "system",
			"content": codebuddyRequiredSystemPrompt,
		})
		seedSeen := false
		for _, mAny := range msgs {
			m, ok := mAny.(map[string]interface{})
			if !ok {
				shaped = append(shaped, mAny)
				continue
			}
			role, _ := m["role"].(string)
			// Sanitize any competitor signature fingerprint in caller content
			if m["content"] != nil {
				m["content"] = sanitizeCodebuddyContent(m["content"])
			}
			content, _ := m["content"].(string)
			// The caller may already carry the seed prompt (e.g. a client that
			// copied it). Keep only the first copy so the gateway never sees two.
			if role == "system" && content == codebuddyRequiredSystemPrompt {
				if seedSeen {
					continue
				}
				seedSeen = true
				continue
			}
			if role == "user" && content != "" {
				m["content"] = []interface{}{map[string]interface{}{
					"type": "text",
					"text": content,
				}}
			}
			shaped = append(shaped, m)
		}
		reqMap["messages"] = shaped
	}

	return json.Marshal(reqMap)
}

// sseToOpenAIJSON re-aggregates OpenAI chat-completions SSE chunks into a
// single chat.completion JSON object. Returns (nil, false) when raw is not
// SSE-shaped (e.g. an upstream error JSON that should pass through).
func sseToOpenAIJSON(raw []byte) ([]byte, bool) {
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
							// Only append if not already present (avoid duplicating done after delta)
							// For split arguments, delta fragments should be appended
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
	var validToolCalls []map[string]any
	for _, tc := range toolCalls {
		if fn, ok := tc["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok && strings.TrimSpace(n) != "" {
				validToolCalls = append(validToolCalls, tc)
			}
		}
	}
	if len(validToolCalls) > 0 {
		msg["tool_calls"] = validToolCalls
	}
	if len(reasoningParts) > 0 {
		msg["reasoning_content"] = strings.Join(reasoningParts, "")
	}

	result := map[string]any{
		"id":      first["id"],
		"object":  "chat.completion",
		"created": first["created"],
		"model":   first["model"],
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
	}
	if usage != nil {
		result["usage"] = usage
	}
	if result["id"] == nil {
		result["id"] = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if result["created"] == nil {
		result["created"] = time.Now().Unix()
	}
	if result["model"] == nil {
		result["model"] = ""
	}
	b, err := json.Marshal(result)
	if err != nil {
		return nil, false
	}
	return b, true
}

// BuildCodebuddyProbeBody builds a minimal chat request shaped EXACTLY the way
// ForwardCodebuddy{CN,Intl} shape a real request — leading system seed prompt,
// typed-block user content, forced stream=true. The per-account connection test
// must send this shape: a hand-rolled body is refused by the gateway's client
// fingerprint (code 11128) or by the non-stream guard (code 11101), which makes
// a healthy account look broken.
//
// model is the chat model to probe with; it must be one the account serves.
func BuildCodebuddyProbeBody(model string) ([]byte, error) {
	raw := map[string]any{
		"model":    model,
		"stream":   false, // transformCodebuddyBody forces true
		"messages": []map[string]any{{"role": "user", "content": "ping"}},
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return transformCodebuddyBody(b)
}

// CodebuddyConversationHeadersFor builds the X-Conversation-* header family for
// a caller that has a stable key (a connection id, a session id) but no
// executor Request. Same derivation as the live path, so a probe and a chat
// request for the same account land on the same conversation.
func CodebuddyConversationHeadersFor(stableKey string) map[string]string {
	return map[string]string{
		"X-Conversation-ID":         stableUuidFrom(stableKey),
		"X-Conversation-Request-ID": uuid.New().String(),
		"X-Conversation-Message-ID": uuid.New().String(),
	}
}
