package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// ForwardGemini forwards to gemini-native endpoints (antigravity).
func ForwardGemini(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardGemini(ctx, req.Client, req.Config, req.APIKey, string(req.Body), req.IsStream, req.ProjectID, req.ModelName)
	if err != nil {
		return fmt.Errorf("ForwardGemini: %w", err)
	}

	var bodyCloser io.Closer = resp.Body
	defer func() {
		if bodyCloser != nil {
			bodyCloser.Close()
		}
	}()

	// The antigravity envelope wraps each SSE event individually
	// (`{"response":{...}}` per event), so it is unwrapped inside the scanner.
	// Reading the whole body first and unwrapping once only ever parsed the
	// first event — a multi-event stream came back as a single chunk, which is
	// why long generations showed up as `chunks=12 bytesWritten=3571`.
	if req.ProjectID != "" {
		if req.IsStream {
			if req.Ctx != nil {
				stallReader := proxy.NewStallReaderWithContext(req.Ctx, resp.Body, 0, "antigravity")
				bodyCloser = stallReader
				return geminiStream(w, stallReader, req.Ctx, true)
			}
			return geminiStream(w, resp.Body, ctx, true)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
		if err != nil {
			return fmt.Errorf("read response body: %w", err)
		}
		return geminiNonStream(w, bytes.NewReader(translator.UnwrapAntigravityResponse(raw)))
	}
	if req.IsStream {
		stallReader := proxy.NewStallReaderWithContext(req.Ctx, resp.Body, 0, "gemini")
		bodyCloser = stallReader
		return geminiStream(w, stallReader, req.Ctx, false)
	}
	return geminiNonStream(w, resp.Body)
}

// geminiStream translates Gemini SSE to OpenAI SSE. unwrap is set for the
// antigravity envelope, where every event is a standalone {"response":{...}}.
func geminiStream(w http.ResponseWriter, upstream io.Reader, ctx context.Context, unwrap bool) error {
	hw := proxy.NewHeartbeatWriter(ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)
	state := &translator.GeminiStreamState{}
	return proxy.ScanStream(upstream, func(chunk []byte) {
		cs := strings.TrimSpace(string(chunk))
		if cs == "" || cs == "[DONE]" {
			return
		}
		payload := []byte(cs)
		if unwrap {
			payload = translator.UnwrapAntigravityResponse(payload)
		}
		oc, err := translator.TranslateGeminiChunkToOpenAI(payload, state)
		if err != nil || oc == nil {
			return
		}
		hw.Write(oc)
		hw.Write([]byte("\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	})
}

func geminiNonStream(w http.ResponseWriter, upstream io.Reader) error {
	body, err := io.ReadAll(io.LimitReader(upstream, 10*1024*1024))
	if err != nil {
		return err
	}
	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
				Role string `json:"role"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}
	resp := translator.OpenAIResponse{
		ID:    "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Model: "gemini",
	}
	if geminiResp.UsageMetadata != nil {
		resp.Usage = &translator.OpenAIUsage{
			PromptTokens:     geminiResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
		}
	}
	for _, c := range geminiResp.Candidates {
		var b strings.Builder
		for _, p := range c.Content.Parts {
			b.WriteString(p.Text)
		}
		fr := "stop"
		if c.FinishReason != "" && c.FinishReason != "STOP" {
			fr = strings.ToLower(c.FinishReason)
		}
		resp.Choices = append(resp.Choices, translator.OpenAIResponseChoice{
			Index:        0,
			Message:      translator.OpenAIRespMsg{Role: "assistant", Content: b.String()},
			FinishReason: &fr,
		})
	}
	openaiResp, err := json.Marshal(resp)
	if err != nil {
		// Fall back to writing the raw Gemini response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(openaiResp)
	return nil
}
