package proxy

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SetSSEHeaders sets SSE headers without writing them.
// Callers that must inspect the first upstream bytes before committing the
// response call this, peek, and only then WriteHeader(200) via FlushAndWrite
// or Commit. Forcing the status through a separate step is what lets the
// fallback loop retry a bare-error body instead of failing with a
// finish_reason-less stream after headers have already gone out.
func SetSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

// FlushAndWrite commits the response with HTTP 200 and returns its flusher.
// It pairs with a prior SetSSEHeaders call.
func FlushAndWrite(w http.ResponseWriter) http.Flusher {
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	if f != nil {
		f.Flush()
	}
	return f
}

// WriteSSEHeaders sets standard SSE headers on the response and writes HTTP 200.
// Returns the http.Flusher if the ResponseWriter supports it.
func WriteSSEHeaders(w http.ResponseWriter) http.Flusher {
	SetSSEHeaders(w)
	return FlushAndWrite(w)
}

// ParseBareErrorBody promotes a 200 response whose body is a single bare
// error object (no SSE framing) into an error. This is the case OpenCode's
// free-tier gate answers at generation time with FreeUsageLimitError — a
// `{"type":"error",...}` object served with HTTP 200 — and relays that hand
// back a throttle as `{"error":...}`. The `data:`-framed bytes of a real
// stream, and any tool payload, pass through (return nil).
func ParseBareErrorBody(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil
	}
	if !bytes.Contains(trimmed, []byte(`"type":"error"`)) {
		return nil
	}
	return detectBareErrorEvent(trimmed)
}

// detectBareErrorEvent inspects one raw upstream chunk for the 200-with-bare-
// error-object shape (OpenCode's FreeUsageLimitError at generation time):
// one JSON object, no `data:` framing, a top-level "type":"error". Ordinary
// tool payloads and completions, and any SSE framing, pass through: only this
// exact shape is promoted to an error so SSECopy can return it to the caller
// instead of writing it verbatim onto the client stream.
func detectBareErrorEvent(chunk []byte) error {
	raw := chunk
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	if !bytes.Contains(trimmed, []byte(`"type":"error"`)) {
		return nil
	}
	// The frame is already here in memory (this chunk is the whole body for a
	// bare object); unmarshal the object directly rather than through the
	// handler's upstreamErrorFromBody, which lives in the chat layer.
	var probe struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
		Choices []any `json:"choices"`
	}
	if err := jsonUnmarshal(trimmed, &probe); err != nil {
		return nil
	}
	if probe.Error == nil || len(probe.Choices) > 0 {
		return nil
	}
	text := strings.ToLower(probe.Error.Type + " " + probe.Error.Message + " " + fmt.Sprint(probe.Error.Code))
	status := 502
	switch {
	case strings.Contains(text, "rate") && (strings.Contains(text, "limit") || strings.Contains(text, "exceed")):
		status = 429
	case strings.Contains(text, "quota"), strings.Contains(text, "credit"), strings.Contains(text, "balance"),
		strings.Contains(text, "free usage"), strings.Contains(text, "free tier"), strings.Contains(text, "free models"):
		status = 429
	case strings.Contains(text, "auth"), strings.Contains(text, "invalid api key"), strings.Contains(text, "unauthorized"):
		status = 401
	}
	return &UpstreamError{StatusCode: status, Body: chunk}
}

func jsonUnmarshal(data []byte, v any) error {
	return stdjson.Unmarshal(data, v)
}

// SSECopy reads from upstream in a raw loop and writes each chunk to the client.
// A simplified passthrough that does NOT parse SSE framing — use when translation is not needed.
// onChunk is called for each chunk before writing (for metrics/TTFT tracking).
// Returns the first upstream or write error so a truncated stream is not
// reported as a successful completion.
func SSECopy(w http.ResponseWriter, upstream io.Reader, flusher http.Flusher, onChunk func([]byte)) error {
	// Allocate a local buffer instead of using the shared pool. The buffer is
	// alive for the entire read loop, so there is no safe point to return it
	// to the pool, and the pool would add a race window between ReleaseByteSlice
	// and the next iteration's write/flush completing.
	buf := make([]byte, 4096)
	var tail [64]byte
	tailLen := 0
	hasTerminal := false
	hasDone := false
	seenSSE := false

	for {
		n, err := upstream.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			// A 200 whose body is a single bare error object in OpenCode's
			// FreeUsageLimitError shape — one JSON object, no `data:` framing
			// — is not a stream no matter what Content-Type claims. Passing
			// it verbatim leaves strict OpenAI streaming clients (Pi harness)
			// on a stream with no finish_reason, and the fallback loop never
			// sees the throttle. But the fail only landed on continue
			// passthrough of one frame on opencode's keyless gateway, so the
			// client got raw error JSON instead of a governable error.
			// Detect this before the first write/flush so the error propagates
			// as a proper upstream error and fallback can retry.
			bare := detectBareErrorEvent(chunk)
			if bare != nil {
				return bare
			}
			if onChunk != nil {
				onChunk(chunk)
			}
			if _, werr := w.Write(chunk); werr != nil {
				return fmt.Errorf("write stream to client: %w", werr)
			}
			if flusher != nil {
				flusher.Flush()
			}

			var checkBuf []byte
			if tailLen > 0 {
				checkBuf = append(tail[:tailLen], chunk...)
			} else {
				checkBuf = chunk
			}
			if bytes.Contains(chunk, []byte("data:")) {
				seenSSE = true
			}
			if bytes.Contains(checkBuf, []byte("[DONE]")) {
				hasDone = true
				return nil
			}
			if bytes.Contains(checkBuf, []byte(`"finish_reason":`)) && !bytes.Contains(checkBuf, []byte(`"finish_reason":null`)) {
				hasTerminal = true
			}
			if bytes.Contains(checkBuf, []byte(`"stop_reason":`)) && !bytes.Contains(checkBuf, []byte(`"stop_reason":null`)) {
				hasTerminal = true
			}
			if bytes.Contains(checkBuf, []byte(`"message_stop"`)) {
				hasTerminal = true
			}

			if n >= 64 {
				copy(tail[:], chunk[n-64:])
				tailLen = 64
			} else {
				copy(tail[:], chunk)
				tailLen = n
			}
		}
		if err != nil {
			if err == io.EOF {
				if seenSSE && !hasDone {
					// PR #4079: If stream tail was not terminated with double newline, emit one
					// so [DONE] or the synthesized terminal does not merge with prior line.
					if tailLen > 0 && !bytes.HasSuffix(tail[:tailLen], []byte("\n\n")) {
						if bytes.HasSuffix(tail[:tailLen], []byte("\n")) {
							_, _ = w.Write([]byte("\n"))
						} else {
							_, _ = w.Write([]byte("\n\n"))
						}
					}
					// If upstream ended without finish_reason, synthesize network_error terminal
					// so clients like Oh My Pi do not fail with "Stream ended without finish_reason"
					if !hasTerminal {
						_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"network_error\"}]}\n\n"))
					}
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					if flusher != nil {
						flusher.Flush()
					}
				}
				return nil
			}
			return fmt.Errorf("read upstream stream: %w", err)
		}
	}
}

// DefaultHeartbeatInterval is the default period for sending SSE keep-alive ping comments.
// Set to 15 seconds so strict clients (Oh My Pi / Cline / Roo) with 30-60s idle timeouts
// never consider the stream stalled during prolonged thinking/reasoning phases.
const DefaultHeartbeatInterval = 15 * time.Second

// HeartbeatWriter wraps an http.ResponseWriter to periodically emit SSE keep-alive
// comments (": keep-alive\n\n") when no data has been written for the interval.
// Thread-safe: synchronizes concurrent writes, flushes, and heartbeat ticks.
type HeartbeatWriter struct {
	w         http.ResponseWriter
	flusher   http.Flusher
	interval  time.Duration
	lastWrite time.Time
	mu        sync.Mutex
	stopCh    chan struct{}
	done      bool
}

// NewHeartbeatWriter starts a background ticker that emits ": keep-alive\n\n"
// every interval if no writes occurred since the last interval.
// Call Close() when the stream completes to terminate the ticker.
func NewHeartbeatWriter(ctx context.Context, w http.ResponseWriter, interval time.Duration) *HeartbeatWriter {
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	flusher, _ := w.(http.Flusher)
	hw := &HeartbeatWriter{
		w:         w,
		flusher:   flusher,
		interval:  interval,
		lastWrite: time.Now(),
		stopCh:    make(chan struct{}),
	}
	go func() {
		ticker := time.NewTicker(hw.interval)
		defer ticker.Stop()
		for {
			if ctx != nil && ctx.Done() != nil {
				select {
				case <-hw.stopCh:
					return
				case <-ctx.Done():
					_ = hw.Close()
					return
				case <-ticker.C:
					hw.mu.Lock()
					if hw.done {
						hw.mu.Unlock()
						return
					}
					if time.Since(hw.lastWrite) >= hw.interval {
						if _, err := hw.w.Write([]byte(": keep-alive\n\n")); err == nil {
							if hw.flusher != nil {
								hw.flusher.Flush()
							}
						}
					}
					hw.mu.Unlock()
				}
			} else {
				select {
				case <-hw.stopCh:
					return
				case <-ticker.C:
					hw.mu.Lock()
					if hw.done {
						hw.mu.Unlock()
						return
					}
					if time.Since(hw.lastWrite) >= hw.interval {
						if _, err := hw.w.Write([]byte(": keep-alive\n\n")); err == nil {
							if hw.flusher != nil {
								hw.flusher.Flush()
							}
						}
					}
					hw.mu.Unlock()
				}
			}
		}
	}()
	return hw
}

func (hw *HeartbeatWriter) Header() http.Header {
	return hw.w.Header()
}

func (hw *HeartbeatWriter) WriteHeader(statusCode int) {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	hw.w.WriteHeader(statusCode)
}

func (hw *HeartbeatWriter) Write(b []byte) (int, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	if hw.done {
		return 0, io.ErrClosedPipe
	}
	hw.lastWrite = time.Now()
	n, err := hw.w.Write(b)
	return n, err
}

func (hw *HeartbeatWriter) Flush() {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	if hw.flusher != nil && !hw.done {
		hw.flusher.Flush()
	}
}

func (hw *HeartbeatWriter) Close() error {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	if !hw.done {
		hw.done = true
		close(hw.stopCh)
	}
	return nil
}
