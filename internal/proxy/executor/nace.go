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
// calls the Decisions API, and maps the structured decision answers to a readable OpenAI chat.completion response.
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
		// If questions is a JSON map (object) or state is used, route to /v1/systemone.
		// If questions is a JSON list (array) or input is used, route to /v1/decisions.
		if _, isMap := reqMap["questions"].(map[string]any); isMap || reqMap["state"] != nil {
			if strings.HasSuffix(targetURL, "/decisions") {
				targetURL = strings.TrimSuffix(targetURL, "/decisions") + "/systemone"
			}
		}
	} else {
		// Adapt chat completions to Decisions format
		input, instructions := extractNaceInputAndInstructions(reqMap["messages"])
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
					"instructions": instructions,
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

	contentStr := formatNaceAnswers(decResp.Answers)

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
		if req.TTFT != nil && *req.TTFT == 0 && req.StartTime.Unix() > 0 {
			*req.TTFT = time.Since(req.StartTime).Milliseconds()
		}
		if req.ResponseBuf != nil {
			req.ResponseBuf.Write([]byte(contentStr))
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

		// Emit finish with usage
		finishChunk, _ := json.Marshal(map[string]any{
			"id":      chunkID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   decResp.Model,
			"choices": []map[string]any{
				{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
			},
			"usage": map[string]any{
				"prompt_tokens":     decResp.Usage.InputTokens,
				"completion_tokens": decResp.Usage.OutputTokens,
				"total_tokens":      decResp.Usage.TotalTokens,
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

// extractNaceInputAndInstructions separates conversation messages into
// the Drex state input and the evaluation instruction question.
func extractNaceInputAndInstructions(rawMessages any) (string, string) {
	msgs, ok := rawMessages.([]any)
	if !ok || len(msgs) == 0 {
		return "hello", "Is the statement true or the question answered affirmatively?"
	}

	var userTexts []string
	var systemInstruction string

	for _, m := range msgs {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		text := extractMessageContent(msgMap["content"])
		if text == "" {
			continue
		}

		if role == "system" {
			// If it's a short task-specific system prompt (not an agent harness boilerplate),
			// use it as custom instructions.
			if len(text) <= 1000 && !isAgentHarnessBoilerplate(text) {
				systemInstruction = text
			}
		} else {
			userTexts = append(userTexts, text)
		}
	}

	input := strings.Join(userTexts, "\n\n")
	if strings.TrimSpace(input) == "" {
		if systemInstruction != "" {
			input = systemInstruction
		} else {
			input = "hello"
		}
	}

	instructions := "Is the statement true or the question answered affirmatively?"
	if systemInstruction != "" {
		instructions = systemInstruction
	}

	return input, instructions
}

// isAgentHarnessBoilerplate checks if system text is generic coding agent boilerplate.
func isAgentHarnessBoilerplate(s string) bool {
	lower := strings.ToLower(s)
	markers := []string{
		"you are an ai agent",
		"you are a helpful assistant",
		"you have access to the following tools",
		"tool_call",
		"bash command",
		"exec_command",
		"terminal",
		"workspace",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

func extractMessageContent(content any) string {
	switch c := content.(type) {
	case string:
		return strings.TrimSpace(c)
	case []any:
		var parts []string
		for _, part := range c {
			if partMap, ok := part.(map[string]any); ok {
				if t, ok := partMap["text"].(string); ok && strings.TrimSpace(t) != "" {
					parts = append(parts, strings.TrimSpace(t))
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// formatNaceAnswers renders decision answers into human/agent-readable text
// instead of dumping raw internal JSON.
func formatNaceAnswers(answers []any) string {
	if len(answers) == 0 {
		return "No decision answers returned."
	}

	var lines []string
	for _, raw := range answers {
		ans, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := ans["type"].(string)
		switch strings.ToLower(typ) {
		case "predicate", "noul":
			prob := getNumber(ans, "probability")
			if prob == 0 {
				prob = getNumber(ans, "noul")
			}
			verdict := "No"
			if prob >= 0.5 {
				verdict = "Yes"
			}
			name, _ := ans["name"].(string)
			if name != "" && name != "eval" && name != "evaluation" && name != "answer" {
				lines = append(lines, fmt.Sprintf("%s: %s (probability: %.1f%%)", name, verdict, prob*100))
			} else {
				lines = append(lines, fmt.Sprintf("%s (probability: %.1f%%)", verdict, prob*100))
			}
		case "choice":
			choice, _ := ans["choice"].(string)
			conf := getNumber(ans, "confidence")
			name, _ := ans["name"].(string)
			prefix := ""
			if name != "" && name != "choice" {
				prefix = name + ": "
			}
			if conf > 0 {
				lines = append(lines, fmt.Sprintf("%s%s (confidence: %.1f%%)", prefix, choice, conf*100))
			} else {
				lines = append(lines, fmt.Sprintf("%s%s", prefix, choice))
			}
		case "score":
			score := getNumber(ans, "score")
			conf := getNumber(ans, "confidence")
			name, _ := ans["name"].(string)
			prefix := ""
			if name != "" && name != "score" {
				prefix = name + ": "
			}
			if conf > 0 {
				lines = append(lines, fmt.Sprintf("%sScore: %.2f (confidence: %.1f%%)", prefix, score, conf*100))
			} else {
				lines = append(lines, fmt.Sprintf("%sScore: %.2f", prefix, score))
			}
		default:
			b, _ := json.Marshal(ans)
			lines = append(lines, string(b))
		}
	}

	if len(lines) == 0 {
		b, _ := json.Marshal(answers)
		return string(b)
	}
	return strings.Join(lines, "\n")
}

func getNumber(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f
		}
	}
	return 0
}