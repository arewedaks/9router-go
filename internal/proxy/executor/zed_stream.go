package executor

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// zedScanLines reads Zed's stream. Unlike standard SSE, Zed emits one bare JSON
// object per line (no "data:" framing), so ScanStream cannot be used here.
func zedScanLines(r io.Reader, onLine func([]byte)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		onLine(line)
	}
	return scanner.Err()
}

// zedTranslate maps one bare Zed event line to OpenAI SSE frames for the given
// provider, reusing the existing per-protocol translators.
func zedTranslate(provider string, line []byte, responseID string, created int64,
	claudeState *translator.ClaudeToOpenAIStreamState,
	geminiState *translator.GeminiStreamState,
	codexState *CodexStreamState) []string {
	switch provider {
	case "anthropic":
		out, err := translator.TranslateClaudeChunkToOpenAI(line, claudeState)
		if err != nil || len(out) == 0 {
			return nil
		}
		return []string{string(out)}
	case "open_ai":
		return ProcessCodexEvent(string(line), codexState, responseID, created)
	case "google":
		out, err := translator.TranslateGeminiChunkToOpenAI(line, geminiState)
		if err != nil || len(out) == 0 {
			return nil
		}
		return []string{string(out)}
	default:
		return nil
	}
}

// streamZed translates Zed's bare-line stream into OpenAI chat chunks.
func streamZed(w http.ResponseWriter, req *Request, provider, model string, upstream io.Reader) error {
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)

	responseID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	var claudeState translator.ClaudeToOpenAIStreamState
	var geminiState translator.GeminiStreamState
	var codexState CodexStreamState
	doneSeen := false
	var writeErr error

	err := zedScanLines(upstream, func(line []byte) {
		if doneSeen || writeErr != nil {
			return
		}
		for _, frame := range zedTranslate(provider, line, responseID, created, &claudeState, &geminiState, &codexState) {
			if doneSeen || writeErr != nil {
				return
			}
			if req.TTFT != nil && *req.TTFT == 0 {
				*req.TTFT = time.Since(req.StartTime).Milliseconds()
			}
			if req.ResponseBuf != nil {
				req.ResponseBuf.Write([]byte(frame))
			}
			if _, werr := hw.Write([]byte(frame)); werr != nil {
				writeErr = werr
				return
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
	})
	if writeErr != nil {
		return fmt.Errorf("write to client: %w", writeErr)
	}
	if err == nil && !doneSeen {
		_, _ = hw.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	return err
}

// completeZed aggregates Zed's stream into a single chat.completion object for
// non-streaming clients.
func completeZed(w http.ResponseWriter, req *Request, provider, model string, upstream io.Reader) error {
	responseID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	var claudeState translator.ClaudeToOpenAIStreamState
	var geminiState translator.GeminiStreamState
	var codexState CodexStreamState

	var buf bytes.Buffer
	_ = zedScanLines(upstream, func(line []byte) {
		for _, frame := range zedTranslate(provider, line, responseID, created, &claudeState, &geminiState, &codexState) {
			buf.WriteString(frame)
		}
	})

	converted, ok := sseToOpenAIJSON(buf.Bytes())
	if !ok {
		converted = []byte(fmt.Sprintf(`{"id":"%s","object":"chat.completion","created":%d,"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`, responseID, created))
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(converted), req.TranslateResp, req.ResponseBuf)
}
