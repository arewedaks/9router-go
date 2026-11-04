package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/proxy"
)

// ForwardNace forwards requests for Nace (Drex decision models).
// If the payload already has a "questions" field, it is forwarded directly to the Decisions API.
// If it is a standard Chat Completions request (messages array), it adapts the messages to input + questions,
// calls the Decisions API, and maps the structured decision answers back to an OpenAI chat.completion response.
func ForwardNace(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	targetURL := "https://console.nace.ai/v1/decisions"
	if req.Config != nil && req.Config.BaseURL != "" {
		base := strings.TrimRight(req.Config.BaseURL, "/")
		if strings.HasSuffix(base, "/decisions") || strings.HasSuffix(base, "/systemone") {
			targetURL = base
		} else {
			targetURL = base + "/decisions"
		}
	}

	var reqMap map[string]any
	if err := json.Unmarshal(req.Body, &reqMap); err != nil {
		return fmt.Errorf("nace: parse body: %w", err)
	}

	isDecisionsWire := reqMap["questions"] != nil
	var outBody []byte

	if isDecisionsWire {
		outBody = req.Body
	} else {
		// Adapt chat completions to Decisions format
		input := extractChatInput(reqMap["messages"])
		model := req.ModelName
		if model == "" {
			if m, ok := reqMap["model"].(string); ok {
				model = m
			}
		}
		if model == "" {
			model = "drex-latest"
		}
		// Strip provider namespace if present (e.g. "nace/drex-v2.0" -> "drex-v2.0")
		if idx := strings.Index(model, "/"); idx >= 0 {
			model = model[idx+1:]
		}
		decReq := map[string]any{
			"model": model,
			"input": input,
			"questions": []map[string]any{
				{
					"type":         "predicate",
					"name":         "evaluation",
					"instructions": "Evaluate the input statement or question.",
				},
			},
		}
		b, err := json.Marshal(decReq)
		if err != nil {
			return fmt.Errorf("nace: marshal decisions request: %w", err)
		}
		outBody = b
	}

	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "application/json",
		"Authorization": "Bearer " + req.APIKey,
	}

	resp, err := proxy.DoRequest(ctx, req.Client, "POST", targetURL, headers, outBody)
	if err != nil {
		return fmt.Errorf("nace upstream: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("nace: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &proxy.UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       respBody,
			Header:     resp.Header,
		}
	}

	if isDecisionsWire {
		// Passthrough decisions response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		if req.ResponseBuf != nil {
			req.ResponseBuf.Write(respBody)
		}
		return nil
	}

	// Map Decisions answer to OpenAI Chat Completion
	var decResp struct {
		Model   string `json:"model"`
		Answers []any  `json:"answers"`
		Usage   struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(respBody, &decResp)

	answersJSON, _ := json.Marshal(decResp.Answers)
	contentStr := string(answersJSON)

	if req.IsStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		created := time.Now().Unix()
		chunkID := "chatcmpl-" + decResp.RequestID
		if decResp.RequestID == "" {
			chunkID = fmt.Sprintf("chatcmpl-drex-%d", created)
		}

		// Emit delta role
		roleChunk, _ := json.Marshal(map[string]any{
			"id":      chunkID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   decResp.Model,
			"choices": []map[string]any{
				{"index": 0, "delta": map[string]any{"role": "assistant"}},
			},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", roleChunk)

		// Emit delta content
		contentChunk, _ := json.Marshal(map[string]any{
			"id":      chunkID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   decResp.Model,
			"choices": []map[string]any{
				{"index": 0, "delta": map[string]any{"content": contentStr}},
			},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", contentChunk)

		// Emit finish
		finishChunk, _ := json.Marshal(map[string]any{
			"id":      chunkID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   decResp.Model,
			"choices": []map[string]any{
				{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
			},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finishChunk)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return nil
	}

	chatResp := map[string]any{
		"id":      "chatcmpl-" + decResp.RequestID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   decResp.Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": contentStr,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     decResp.Usage.InputTokens,
			"completion_tokens": decResp.Usage.OutputTokens,
			"total_tokens":      decResp.Usage.TotalTokens,
		},
	}
	out, _ := json.Marshal(chatResp)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
	if req.ResponseBuf != nil {
		req.ResponseBuf.Write(out)
	}
	return nil
}

func extractChatInput(rawMessages any) string {
	msgs, ok := rawMessages.([]any)
	if !ok || len(msgs) == 0 {
		return "hello"
	}
	var texts []string
	for _, m := range msgs {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		switch c := msgMap["content"].(type) {
		case string:
			if strings.TrimSpace(c) != "" {
				texts = append(texts, strings.TrimSpace(c))
			}
		case []any:
			for _, part := range c {
				if partMap, ok := part.(map[string]any); ok {
					if t, ok := partMap["text"].(string); ok && strings.TrimSpace(t) != "" {
						texts = append(texts, strings.TrimSpace(t))
					}
				}
			}
		}
	}
	if len(texts) == 0 {
		return "hello"
	}
	return strings.Join(texts, "\n\n")
}
