package tokensaver

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultHeadroomTimeoutMs matches the reference implementation's default.
// A busy machine that cannot answer in time is not a reason to fail the
// request, so the caller treats a timeout as "nothing happened".
const DefaultHeadroomTimeoutMs = 3000

// headroomMaxResponseBytes caps how much of the proxy's reply we read. The
// response is a rewritten conversation, so it can legitimately be large; the
// cap only exists to stop a broken proxy from streaming unbounded data into
// memory.
const headroomMaxResponseBytes = 32 << 20

// HeadroomRequest is the subset of the /v1/compress contract this proxy uses.
type HeadroomRequest struct {
	Messages []any  `json:"messages"`
	Model    string `json:"model"`
	Config   *struct {
		CompressUserMessages bool `json:"compress_user_messages"`
	} `json:"config,omitempty"`
}

// HeadroomResponse is the proxy's reply. Only messages is required; the token
// counters are reported for logging and may be absent.
type HeadroomResponse struct {
	Messages     []any `json:"messages"`
	TokensBefore int   `json:"tokens_before"`
	TokensAfter  int   `json:"tokens_after"`
	TokensSaved  int   `json:"tokens_saved"`
}

// HeadroomOptions configures one compression call.
type HeadroomOptions struct {
	BaseURL              string
	Model                string
	TimeoutMs            int
	CompressUserMessages bool
}

// Endpoint builds the /v1/compress URL from a base that may already carry a
// path. Mirrors the reference buildCompressEndpoint, including its tolerance
// for a base with a trailing slash.
func (o HeadroomOptions) Endpoint() string {
	base := strings.TrimRight(strings.TrimSpace(o.BaseURL), "/")
	if base == "" {
		return ""
	}
	return base + "/v1/compress"
}

func (o HeadroomOptions) timeout() time.Duration {
	ms := o.TimeoutMs
	if ms <= 0 {
		ms = DefaultHeadroomTimeoutMs
	}
	return time.Duration(ms) * time.Millisecond
}

// CompressWithHeadroom posts the conversation to a Headroom proxy and returns
// the compressed messages.
//
// Fail-open by design, matching the reference: a missing proxy, a timeout, a
// non-2xx answer or a malformed reply all leave the caller's body untouched.
// Headroom is an optional optimisation, and turning an unreachable sidecar
// into a failed chat request would make it a liability rather than a saver.
func CompressWithHeadroom(ctx context.Context, client *http.Client, messages []any, opts HeadroomOptions) ([]any, *HeadroomResponse, error) {
	endpoint := opts.Endpoint()
	if endpoint == "" {
		return nil, nil, errors.New("headroom: no proxy URL configured")
	}
	if len(messages) == 0 {
		return nil, nil, errors.New("headroom: no messages to compress")
	}

	payload := HeadroomRequest{Messages: messages, Model: opts.Model}
	if opts.CompressUserMessages {
		payload.Config = &struct {
			CompressUserMessages bool `json:"compress_user_messages"`
		}{CompressUserMessages: true}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("headroom: marshal request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, opts.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("headroom: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("headroom: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("headroom: proxy returned HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, headroomMaxResponseBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("headroom: read response: %w", err)
	}
	var parsed HeadroomResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, nil, fmt.Errorf("headroom: decode response: %w", err)
	}
	if len(parsed.Messages) == 0 {
		return nil, nil, errors.New("headroom: response carried no messages[]")
	}
	return parsed.Messages, &parsed, nil
}

// MessageKey reports which top-level key holds the conversation array, and
// whether the body is a shape Headroom understands. OpenAI chat uses
// "messages"; the Responses API uses "input". Anything else (Claude Messages,
// Kiro) has to be translated by the caller first.
func MessageKey(body map[string]any) (string, bool) {
	if _, ok := body["messages"].([]any); ok {
		return "messages", true
	}
	if _, ok := body["input"].([]any); ok {
		return "input", true
	}
	return "", false
}

// ResponsesInputIsCompressible reports whether a Responses-API `input` array is
// safe to hand to the proxy.
//
// The Responses contract allows non-message items (function_call, reasoning,
// tool outputs). Headroom only understands chat messages, and the reference
// skips the whole request rather than letting those items be rewritten into
// something the provider would reject.
func ResponsesInputIsCompressible(items []any) bool {
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := m["type"].(string); ok && t != "" && t != "message" {
			return false
		}
	}
	return true
}

// FormatHeadroomLog renders the proxy's token counters for the console log.
func FormatHeadroomLog(stats *HeadroomResponse) string {
	if stats == nil {
		return ""
	}
	pct := 0.0
	if stats.TokensBefore > 0 {
		pct = float64(stats.TokensSaved) / float64(stats.TokensBefore) * 100
	}
	return fmt.Sprintf("reported token delta=%d before=%d after=%d (%.1f%%)",
		stats.TokensSaved, stats.TokensBefore, stats.TokensAfter, pct)
}
