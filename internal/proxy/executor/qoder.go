package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

const qoderRSAPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

// qoderSigPath is the path the COSY signature covers. The upstream strips the
// "/algo" prefix before checking and does not sign the query string, so this is
// a constant regardless of the request URL.
const qoderSigPath = "/api/v2/service/pro/sse/agent_chat_generation"

// qoderDone is the end-of-reply sentinel the gateway wraps in the envelope body.
const qoderDone = "[" + "DONE" + "]"

// qoderChatURL is the agent surface. FetchKeys/AgentId select the model-result
// agent; Encode=1 announces that the body uses Qoder's custom base64 alphabet
// plus the three-block rotation (see qoderEncodeBody).
const qoderChatURL = "https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation" +
	"?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"

func parseQoderPublicKey() (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(qoderRSAPublicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("invalid PEM block")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not RSA public key")
	}
	return rsaPub, nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - (len(data) % blockSize)
	padText := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padText...)
}

func aesEncryptCBCBase64(plaintext []byte, key []byte) (string, error) {
	if len(key) != 16 {
		return "", fmt.Errorf("aes key must be 16 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, key)
	mode.CryptBlocks(ciphertext, padded)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func rsaEncryptBase64(data []byte) (string, error) {
	pub, err := parseQoderPublicKey()
	if err != nil {
		return "", err
	}
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, pub, data)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

func md5Hex(data []byte) string {
	h := md5.Sum(data)
	return hex.EncodeToString(h[:])
}

const (
	qoderStdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	qoderCustomAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
)

// qoderEncodeBody encodes a request body with Qoder's custom base64 alphabet
// and the three-block rotation that the Encode=1 query flag announces.
//
// Without it the server's CustomBase64Util.decode rejects the standard "="
// padding ("Illegal base64 character 22") and answers with a 500 Java stack
// trace; this is the encoding every current qodercli build sends.
func qoderEncodeBody(raw []byte) []byte {
	std := base64.StdEncoding.EncodeToString(raw)
	var table [256]byte
	for i := range table {
		table[i] = byte(i)
	}
	for i := 0; i < len(qoderStdAlphabet); i++ {
		table[qoderStdAlphabet[i]] = qoderCustomAlphabet[i]
	}
	table['='] = '$'

	n := len(std)
	a := n / 3
	out := make([]byte, 0, n)
	for _, seg := range []string{std[n-a:], std[a : n-a], std[:a]} {
		for i := 0; i < len(seg); i++ {
			out = append(out, table[seg[i]])
		}
	}
	return out
}

// qoderIdentity is the account identity carried inside the signed payload. The
// upstream resolves the caller against this uid: a random uid is rejected with
// 403 "Login expired" (code 105), so it must be the id /api/v1/userinfo
// returns, not a per-request UUID.
type QoderIdentity struct {
	UserID    string
	Name      string
	Email     string
	MachineID string
}

// QoderIdentityFromConn extracts the identity Qoder sign-in stored on the
// connection. userId is required: the executor sends it as the COSY uid, and a
// blank uid makes the upstream answer 403 "Login expired" regardless of token.
func QoderIdentityFromConn(psd map[string]interface{}) QoderIdentity {
	if psd == nil {
		return QoderIdentity{}
	}
	str := func(k string) string {
		s, _ := psd[k].(string)
		return strings.TrimSpace(s)
	}
	return QoderIdentity{
		UserID:    str("userId"),
		Name:      str("name"),
		Email:     str("email"),
		MachineID: str("machineId"),
	}
}

type qoderUserInfo struct {
	UID           string `json:"uid"`
	OAuthToken    string `json:"security_oauth_token"`
	Name          string `json:"name"`
	AID           string `json:"aid"`
	Email         string `json:"email"`
	OrganizationI string `json:"organization_id,omitempty"`
}

type qoderPayload struct {
	Version     string `json:"version"`
	RequestID   string `json:"requestId"`
	Info        string `json:"info"`
	CosyVersion string `json:"cosyVersion"`
	IDeVersion  string `json:"ideVersion"`
}

// buildQoderCosyHeaders signs one request. body must be the exact bytes sent.
func buildQoderCosyHeaders(body []byte, id QoderIdentity, token, modelKey string) (map[string]string, error) {
	aesKey := []byte(uuid.New().String()[:16])

	// Structs, not maps: json.Marshal must emit a byte-identical payload for a
	// given request, and Go randomizes map key order.
	infoJSON, err := json.Marshal(qoderUserInfo{
		UID: id.UserID, OAuthToken: token, Name: id.Name, Email: id.Email,
	})
	if err != nil {
		return nil, err
	}
	infoB64, err := aesEncryptCBCBase64(infoJSON, aesKey)
	if err != nil {
		return nil, err
	}
	cosyKeyB64, err := rsaEncryptBase64(aesKey)
	if err != nil {
		return nil, err
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	payloadJSON, err := json.Marshal(qoderPayload{
		Version: "v1", RequestID: uuid.New().String(), Info: infoB64,
		CosyVersion: "1.0.0", IDeVersion: "",
	})
	if err != nil {
		return nil, err
	}
	payloadB64 := base64.StdEncoding.EncodeToString(payloadJSON)

	sig := md5Hex([]byte(strings.Join([]string{
		payloadB64, cosyKeyB64, timestamp, string(body), qoderSigPath,
	}, "\n")))

	machineID := id.MachineID
	if machineID == "" {
		machineID = uuid.New().String()
	}

	return map[string]string{
		"Authorization":          "Bearer COSY." + payloadB64 + "." + sig,
		"Cosy-Key":               cosyKeyB64,
		"Cosy-User":              id.UserID,
		"Cosy-Date":              timestamp,
		"Cosy-Version":           "1.0.0",
		"Cosy-Machineid":         machineID,
		"Cosy-Machinetoken":      machineID,
		"Cosy-Machinetype":       "5",
		"Cosy-Machineos":         "x86_64_windows",
		"Cosy-Clienttype":        "5",
		"Cosy-Clientip":          "127.0.0.1",
		"Cosy-Bodyhash":          md5Hex(body),
		"Cosy-Bodylength":        fmt.Sprintf("%d", len(body)),
		"Cosy-Sigpath":           qoderSigPath,
		"Cosy-Data-Policy":       "disagree",
		"Cosy-Organization-Id":   "",
		"Cosy-Organization-Tags": "",
		"Login-Version":          "v2",
		"X-Request-Id":           uuid.New().String(),
		"X-Model-Key":            modelKey,
		"X-Model-Source":         "system",
		"Content-Type":           "application/json",
		"Accept":                 "text/event-stream",
		"Cache-Control":          "no-cache",
		"Accept-Encoding":        "identity",
	}, nil
}

// The agent surface takes a fixed envelope, not an OpenAI chat body: the
// signature validates either, but the agent router only routes this shape.
type (
	qoderMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	qoderParameters struct {
		MaxTokens       int    `json:"max_tokens"`
		EnableThinking  bool   `json:"enable_thinking"`
		ReasoningEffort string `json:"reasoning_effort,omitempty"`
	}
	qoderModelConfig struct {
		Key         string `json:"key"`
		IsReasoning bool   `json:"is_reasoning"`
		Source      string `json:"source,omitempty"`
	}
	qoderChatExtra struct {
		Context       []any            `json:"context"`
		ModelConfig   qoderModelConfig `json:"modelConfig"`
		OriginalCtent string           `json:"originalContent"`
	}
	qoderChatContext struct {
		ChatPrompt string         `json:"chatPrompt"`
		ImageURLs  any            `json:"imageUrls"`
		Extra      qoderChatExtra `json:"extra"`
		Features   []any          `json:"features"`
		Text       string         `json:"text"`
	}
	qoderBusiness struct {
		Product string `json:"product"`
		Version string `json:"version"`
		Type    string `json:"type"`
		Stage   string `json:"stage"`
		ID      string `json:"id"`
		Name    string `json:"name"`
		BeginAt int64  `json:"begin_at"`
	}
	qoderRequest struct {
		RequestID    string           `json:"request_id"`
		RequestSetID string           `json:"request_set_id"`
		ChatRecordID string           `json:"chat_record_id"`
		SessionID    string           `json:"session_id"`
		Stream       bool             `json:"stream"`
		ChatTask     string           `json:"chat_task"`
		IsReply      bool             `json:"is_reply"`
		IsRetry      bool             `json:"is_retry"`
		Source       int              `json:"source"`
		Version      string           `json:"version"`
		SessionType  string           `json:"session_type"`
		AgentID      string           `json:"agent_id"`
		TaskID       string           `json:"task_id"`
		CodeLanguage string           `json:"code_language"`
		ChatPrompt   string           `json:"chat_prompt"`
		ImageURLs    any              `json:"image_urls"`
		AliyunType   string           `json:"aliyun_user_type"`
		System       string           `json:"system"`
		Messages     []qoderMessage   `json:"messages"`
		Tools        []any            `json:"tools"`
		Parameters   qoderParameters  `json:"parameters"`
		ChatContext  qoderChatContext `json:"chat_context"`
		ModelConfig  qoderModelConfig `json:"model_config"`
		Business     qoderBusiness    `json:"business"`
	}
)

// buildQoderBody converts the caller's OpenAI chat request into the agent
// shape, flattening content parts to text (the envelope takes String content).
func buildQoderBody(openAIBody []byte, modelKey string) ([]byte, error) {
	var in struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(openAIBody, &in); err != nil {
		return nil, fmt.Errorf("qoder: parse request: %w", err)
	}

	var systemParts, convo []string
	var lastUser string
	msgs := make([]qoderMessage, 0, len(in.Messages))
	for _, m := range in.Messages {
		text := qoderFlattenContent(m.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if m.Role == "system" {
			systemParts = append(systemParts, text)
			continue
		}
		msgs = append(msgs, qoderMessage{Role: m.Role, Content: text})
		convo = append(convo, m.Role+": "+text)
		if m.Role == "user" {
			lastUser = text
		}
	}
	if len(msgs) == 0 {
		return nil, &proxy.UpstreamError{
			StatusCode: http.StatusBadRequest,
			Body:       []byte(`{"error":{"message":"qoder: no message content"}}`),
		}
	}

	maxTokens := in.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}

	recordID := uuid.New().String()
	cfg := qoderModelConfig{Key: modelKey, IsReasoning: false, Source: "system"}
	messages := msgs
	// The server ignores a top-level `system`; the prompt has to ride as a
	// leading role:system message instead.
	if len(systemParts) > 0 {
		messages = append([]qoderMessage{
			{Role: "system", Content: strings.Join(systemParts, "\n")},
		}, msgs...)
	}

	return json.Marshal(qoderRequest{
		RequestID: recordID, RequestSetID: recordID, ChatRecordID: recordID,
		SessionID: uuid.New().String(), Stream: true,
		ChatTask: "FREE_INPUT", IsReply: true, IsRetry: false,
		Source: 1, Version: "3", SessionType: "qodercli",
		AgentID: "agent_common", TaskID: "common",
		Messages:    messages,
		Tools:       []any{},
		Parameters:  qoderParameters{MaxTokens: maxTokens, EnableThinking: false, ReasoningEffort: "none"},
		ModelConfig: cfg,
		ChatContext: qoderChatContext{Extra: qoderChatExtra{Context: []any{}, ModelConfig: cfg, OriginalCtent: lastUser}, Features: []any{}, Text: strings.Join(convo, "\n")},
		Business:    qoderBusiness{Product: "cli", Version: "1.0.0", Type: "agent", Stage: "start", ID: uuid.New().String(), Name: qoderTruncate(lastUser, 30), BeginAt: time.Now().UnixMilli()},
	})
}

// qoderFlattenContent turns message content (string or content-part array)
// into plain text.
func qoderFlattenContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return strings.Join(out, "")
	}
	return ""
}

func qoderTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// qoderStatusReport maps a gateway error code to an HTTP status and a message
// the operator can act on. Code 112 is the not-in-plan answer: the account
// signed in but has no entitlement for the requested model.
func qoderStatusReport(envelopeBody string) (int, string) {
	var inner struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(envelopeBody), &inner) != nil {
		return http.StatusBadGateway, envelopeBody
	}
	switch inner.Code {
	case "112":
		return http.StatusPaymentRequired, "Qoder: model not included in your current plan (" + inner.Message + ")"
	case "105":
		return http.StatusUnauthorized, "Qoder session expired — sign in again"
	case "101":
		return http.StatusUnauthorized, "Qoder signature rejected"
	case "10605":
		// RESOURCE_LIMIT: the account's concurrency queue is full. The message
		// nests the real payload, which carries how long to wait.
		var q struct {
			RetryAfterSeconds int  `json:"retryAfterSeconds"`
			WaitTime          int  `json:"waitTime"`
			ServiceAvailable  bool `json:"serviceAvailable"`
		}
		_ = json.Unmarshal([]byte(inner.Message), &q)
		secs := q.RetryAfterSeconds
		if secs == 0 {
			secs = q.WaitTime
		}
		if !q.ServiceAvailable && secs > 0 {
			return http.StatusTooManyRequests, fmt.Sprintf(
				"Qoder: account is busy (plan queue full) — retry in about %ds", secs)
		}
		return http.StatusTooManyRequests, "Qoder: account is busy — retry shortly"
	}
	return http.StatusBadGateway, inner.Message
}

// qoderReadEvents walks the upstream SSE stream line by line and stops as soon
// as onEvent asks it to. It cannot use proxy.ScanStream: that helper reads to
// EOF, and the Qoder gateway holds the socket open after [DONE], so the read
// never returns — which is what made every call report a timeout.
func qoderReadEvents(upstream io.Reader, onEvent func(payload []byte) bool) error {
	br := bufio.NewReaderSize(upstream, 64*1024)
	var dataLines [][]byte
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			switch {
			case len(line) == 0:
				// Blank line terminates the event.
				if len(dataLines) > 0 {
					payload := bytes.Join(dataLines, []byte("\n"))
					dataLines = dataLines[:0]
					if len(payload) > 0 && onEvent(payload) {
						return nil
					}
				}
			case line[0] == ':':
				// keep-alive comment
			case bytes.HasPrefix(line, []byte("data:")):
				chunk := bytes.TrimPrefix(line, []byte("data:"))
				if len(chunk) > 0 && chunk[0] == ' ' {
					chunk = chunk[1:]
				}
				dataLines = append(dataLines, chunk)
			default:
				// `event:finish` and friends carry no payload we need.
			}
		}
		if err != nil {
			if err != io.EOF {
				return err
			}
			// Flush a trailing event that had no blank line.
			if len(dataLines) > 0 {
				payload := bytes.Join(dataLines, []byte("\n"))
				if len(payload) > 0 {
					onEvent(payload)
				}
			}
			return nil
		}
	}
}

// execQoderStream unwraps Qoder's doubly-wrapped SSE envelope
// (data:{"headers":…,"body":"<openai chunk>","statusCodeValue":200}) into the
// OpenAI chunks the client expects, ending as soon as the sentinel arrives.
func execQoderStream(w http.ResponseWriter, upstream io.Reader, req *Request) error {
	startTime := req.StartTime
	if startTime.IsZero() {
		startTime = time.Now()
	}
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)

	write := func(b []byte) {
		hw.Write(b)
		if flusher != nil {
			flusher.Flush()
		}
	}

	sawDone := false
	err := qoderReadEvents(upstream, func(payload []byte) bool {
		if bytes.Equal(bytes.TrimSpace(payload), []byte(qoderDone)) {
			sawDone = true
			return true
		}
		var env struct {
			Body            string `json:"body"`
			StatusCodeValue int    `json:"statusCodeValue"`
		}
		if json.Unmarshal(payload, &env) != nil {
			// Not the envelope: forward the payload untouched.
			write([]byte("data: " + string(payload) + "\n\n"))
			return false
		}
		if env.StatusCodeValue != 0 && env.StatusCodeValue != http.StatusOK {
			status, msg := qoderStatusReport(env.Body)
			if req.ResponseBuf != nil {
				req.ResponseBuf.Write([]byte(msg))
			}
			write([]byte(fmt.Sprintf(
				"data: {\"error\":{\"message\":%q,\"type\":\"upstream_error\",\"code\":%d}}\n\n",
				msg, status)))
			sawDone = true
			return true
		}
		if env.Body == "" {
			return false
		}
		if env.Body == qoderDone {
			sawDone = true
			return true
		}
		if req.TTFT != nil && *req.TTFT == 0 {
			*req.TTFT = time.Since(startTime).Milliseconds()
		}
		if req.ResponseBuf != nil {
			req.ResponseBuf.Write([]byte(env.Body))
		}
		write([]byte("data: " + env.Body + "\n\n"))
		return false
	})
	if err == nil && !sawDone {
		err = fmt.Errorf("qoder: stream ended before [DONE]")
	}
	if sawDone {
		write([]byte("data: [DONE]\n\n"))
	}
	return err
}

// ForwardQoder handles requests for Qoder using COSY signing.
func ForwardQoder(w http.ResponseWriter, req *Request) error {
	modelKey := req.ModelName
	if modelKey == "" {
		modelKey = "auto"
	}

	body, err := buildQoderBody(req.Body, modelKey)
	if err != nil {
		return err
	}
	wire := qoderEncodeBody(body)
	// The signature covers the bytes that actually go on the wire, so headers
	// must be signed over the encoded body, not the JSON envelope.
	headers, err := buildQoderCosyHeaders(wire, req.QoderIdentity, req.APIKey, modelKey)
	if err != nil {
		return fmt.Errorf("build Qoder COSY headers: %w", err)
	}

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.DoRequest(ctx, req.Client, "POST", qoderChatURL, headers, wire)
	if err != nil {
		return fmt.Errorf("ForwardQoder: %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execQoderStream(w, resp.Body, req)
	}
	return qoderJSONResponse(ctx, w, resp.Body, req)
}

// qoderJSONResponse answers a non-streaming call. The upstream only speaks SSE,
// so the envelope is drained and its deltas merged into one chat.completion —
// returning the raw stream to a non-streaming client would hand it a
// doubly-wrapped envelope it cannot parse.
func qoderJSONResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, req *Request) error {
	var text strings.Builder
	var id, model string
	var errMsg string
	var errStatus int

	_ = qoderReadEvents(upstream, func(payload []byte) bool {
		var env struct {
			Body            string `json:"body"`
			StatusCodeValue int    `json:"statusCodeValue"`
		}
		if json.Unmarshal(payload, &env) != nil || env.Body == "" {
			return false
		}
		if env.Body == qoderDone {
			return true
		}
		if env.StatusCodeValue != 0 && env.StatusCodeValue != http.StatusOK {
			errStatus, errMsg = qoderStatusReport(env.Body)
			return true
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(env.Body), &chunk) != nil {
			return false
		}
		if chunk.ID != "" {
			id = chunk.ID
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if len(chunk.Choices) > 0 {
			text.WriteString(chunk.Choices[0].Delta.Content)
		}
		return false
	})

	if errMsg != "" {
		if req.ResponseBuf != nil {
			req.ResponseBuf.Write([]byte(errMsg))
		}
		return &proxy.UpstreamError{
			StatusCode: errStatus,
			Body: []byte(fmt.Sprintf(
				`{"error":{"message":%q,"type":"upstream_error","code":%d}}`, errMsg, errStatus)),
		}
	}
	if id == "" {
		id = uuid.New().String()
	}
	if model == "" {
		model = req.ModelName
	}

	out, err := json.Marshal(map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": text.String()},
			"finish_reason": "stop",
		}},
	})
	if err != nil {
		return err
	}
	if req.ResponseBuf != nil {
		req.ResponseBuf.Write(out)
	}
	if req.TranslateResp {
		if translated, usage, terr := translator.TranslateOpenAIToClaude(out); terr == nil && translated != nil {
			if usage != nil {
				translator.SetUsage(ctx, usage)
			}
			out = translated
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
	return nil
}
