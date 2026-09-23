package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
)

// UpstreamError captures a non-200 upstream response.
type UpstreamError struct {
	StatusCode int
	Body       []byte
}

func (e *UpstreamError) Error() string {
	// Include the upstream body (truncated) so 4xx/5xx failures are diagnosable
	// from the fallback log alone — the body often carries Google/Antigravity's
	// actual rejection reason ("Invalid tool parameters", unknown model, etc.).
	body := strings.TrimSpace(string(e.Body))
	if strings.HasPrefix(body, "<!DOCTYPE html") || strings.HasPrefix(body, "<html") {
		lower := strings.ToLower(body)
		if strings.Contains(lower, "cloudflare") || strings.Contains(lower, "attention required") {
			return fmt.Sprintf("upstream returned %d: Cloudflare WAF challenge (Attention Required!): check User-Agent or network proxy", e.StatusCode)
		}
		if titleStart := strings.Index(lower, "<title>"); titleStart != -1 {
			titleEnd := strings.Index(lower[titleStart:], "</title>")
			if titleEnd != -1 {
				titleText := strings.TrimSpace(body[titleStart+7 : titleStart+titleEnd])
				return fmt.Sprintf("upstream returned %d: HTML page (%s)", e.StatusCode, titleText)
			}
		}
		return fmt.Sprintf("upstream returned %d: HTML error page", e.StatusCode)
	}
	if len(body) > 512 {
		body = body[:512] + "... (truncated)"
	}
	if body != "" {
		return fmt.Sprintf("upstream returned %d: %s", e.StatusCode, body)
	}
	return fmt.Sprintf("upstream returned %d", e.StatusCode)
}

// DoRequest sends an HTTP POST to url with body and auth, returns the raw response.
// Caller must close resp.Body.
//
// One replay is attempted when the first attempt dies on a connection-level
// error. That is the signature of a keep-alive socket the peer closed while it
// sat idle in the pool: the write fails with EOF/reset before any response
// exists. net/http replays only idempotent requests, and everything this
// package sends is a POST, so the replay has to be explicit here. It is safe
// precisely because no response was received — either the request never
// reached the peer, or its reply was lost — and a dead pooled connection is by
// far the common case. Retries are bounded to those connection errors, so a
// request the upstream actually answered is never sent twice.
func DoRequest(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body []byte) (*http.Response, error) {
	resp, err := doRequestOnce(ctx, client, method, url, headers, body)
	if err != nil && retryableConnError(err) {
		resp, err = doRequestOnce(ctx, client, method, url, headers, body)
	}
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("upstream returned %d and body read failed: %w", resp.StatusCode, readErr)
		}
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}
	return resp, nil
}

// doRequestOnce performs a single upstream attempt.
func doRequestOnce(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return client.Do(req)
}

// retryableConnError reports whether err is a connection-level failure that
// occurred before the upstream produced any response. A canceled or expired
// context is deliberately excluded: the caller asked to stop, so replaying
// would work against it.
func retryableConnError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	// HTTP/2 reports the pool-invalidation cases as text rather than sentinel
	// errors, so the message is the only signal available.
	msg := err.Error()
	return strings.Contains(msg, "http2: server sent GOAWAY") ||
		strings.Contains(msg, "connection reset by peer")
}

