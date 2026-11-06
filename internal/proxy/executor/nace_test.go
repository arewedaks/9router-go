package executor

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

func TestForwardNace_ChatCompletionsAdaptedToDecisions(t *testing.T) {
	var gotPath, gotAuth string
	var decReq struct {
		Model     string `json:"model"`
		Input     string `json:"input"`
		Questions []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"questions"`
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &decReq)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"model": "drex-v2.0",
			"answers": [
				{"type":"predicate","name":"evaluation","probability":0.95}
			],
			"usage": {"input_tokens":12,"output_tokens":8,"total_tokens":20},
			"request_id": "req_nace_test"
		}`))
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{
		BaseURL:    upstream.URL + "/decisions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	}

	chatBody := `{"model":"nc/drex-v2.0","messages":[{"role":"user","content":"Is this valid?"}],"max_tokens":100}`
	req := &Request{
		Ctx:       context.Background(),
		Client:    upstream.Client(),
		Config:    cfg,
		APIKey:    "nace_sk_test_key",
		Body:      []byte(chatBody),
		ModelName: "drex-v2.0",
		IsStream:  false,
	}

	rec := httptest.NewRecorder()
	err := ForwardNace(rec, req)
	if err != nil {
		t.Fatalf("ForwardNace: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/decisions" {
		t.Errorf("upstream path = %q, want /decisions", gotPath)
	}
	if gotAuth != "Bearer nace_sk_test_key" {
		t.Errorf("upstream auth = %q, want Bearer nace_sk_test_key", gotAuth)
	}
	if decReq.Model != "drex-v2.0" {
		t.Errorf("decReq.Model = %q, want drex-v2.0", decReq.Model)
	}
	if decReq.Input != "Is this valid?" {
		t.Errorf("decReq.Input = %q, want Is this valid?", decReq.Input)
	}
	if len(decReq.Questions) == 0 {
		t.Fatalf("decReq.Questions is empty")
	}

	var chatResp struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("unmarshal chat response: %v", err)
	}
	if chatResp.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", chatResp.Object)
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Role != "assistant" {
		t.Errorf("invalid choices: %+v", chatResp.Choices)
	}
	content := chatResp.Choices[0].Message.Content
	if strings.Contains(content, `[{"type"`) {
		t.Errorf("content leaked raw JSON: %q", content)
	}
	if !strings.Contains(content, "Yes") || !strings.Contains(content, "95.0%") {
		t.Errorf("content = %q, want formatted 'Yes (probability: 95.0%%)'", content)
	}
}

func TestExtractNaceInputAndInstructions_FiltersBoilerplate(t *testing.T) {
	msgs := []any{
		map[string]any{
			"role":    "system",
			"content": "You are an AI agent. You have access to the following tools: bash command, exec_command, workspace.",
		},
		map[string]any{
			"role":    "user",
			"content": "Is the invoice approved?",
		},
	}
	input, instr := extractNaceInputAndInstructions(msgs)
	if input != "Is the invoice approved?" {
		t.Errorf("input = %q, want 'Is the invoice approved?'", input)
	}
	if instr != "Is the statement true or the question answered affirmatively?" {
		t.Errorf("instructions = %q, want default affirmative check", instr)
	}
}

func TestExtractNaceInputAndInstructions_KeepsCustomTask(t *testing.T) {
	msgs := []any{
		map[string]any{
			"role":    "system",
			"content": "Is the customer sentiment positive?",
		},
		map[string]any{
			"role":    "user",
			"content": "I really love the quick delivery!",
		},
	}
	input, instr := extractNaceInputAndInstructions(msgs)
	if input != "I really love the quick delivery!" {
		t.Errorf("input = %q, want user content", input)
	}
	if instr != "Is the customer sentiment positive?" {
		t.Errorf("instructions = %q, want custom task prompt", instr)
	}
}

func TestForwardNace_DecisionsPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"model":"drex-latest","answers":[{"type":"choice","name":"test","choice":"billing"}]}`))
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: upstream.URL + "/decisions",
	}

	decBody := `{"model":"drex-latest","input":"sample","questions":[{"type":"choice","name":"test","choices":[{"value":"billing"}]}]}`
	req := &Request{
		Ctx:       context.Background(),
		Client:    upstream.Client(),
		Config:    cfg,
		APIKey:    "nace_sk_test_key",
		Body:      []byte(decBody),
		ModelName: "drex-latest",
	}

	rec := httptest.NewRecorder()
	err := ForwardNace(rec, req)
	if err != nil {
		t.Fatalf("ForwardNace: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"choice":"billing"`) {
		t.Errorf("expected passthrough body, got: %s", rec.Body.String())
	}
}
