package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
)

// ForwardZed talks to Zed's cloud completions endpoint. Three things make it
// unlike a normal provider:
//
//   - the sign-in credential (`<user_id> <access_token>`) is not accepted here;
//     /completions wants `Bearer <llm token>`, minted per organization
//     (providers.ZedLLMToken);
//   - the body is not a chat request but a wrapper — {provider, model,
//     provider_request} — where provider_request is the *native* request of that
//     provider (Anthropic Messages, OpenAI Responses, Gemini generateContent);
//   - the stream is native provider events, not OpenAI chunks, so each provider
//     needs its own reader.
//
// Tools and images are not mapped yet: text-only conversations work end to end.
// ponytail: mapping tool definitions per provider is the next step; the ceiling
// is a chat client that uses tools against a zed/* model.
func ForwardZed(w http.ResponseWriter, req *Request) error {
	if req.Config == nil {
		return fmt.Errorf("zed: no provider config")
	}
	userID := strings.TrimSpace(req.ZedUserID)
	accessToken := strings.TrimSpace(req.APIKey)
	if userID == "" || accessToken == "" {
		return fmt.Errorf("zed: connection is missing the user id or access token — sign in again from the dashboard")
	}

	chat, err := parseZedChat(req.Body)
	if err != nil {
		return err
	}
	if len(chat.messages) == 0 {
		return fmt.Errorf("zed: messages are required")
	}

	model := strings.TrimSpace(chat.model)
	if model == "" {
		model = strings.TrimSpace(req.ModelName)
	}

	provider, err := providers.ZedModelProvider(req.Ctx, req.Client, userID, accessToken, model)
	if err != nil {
		return err
	}
	llmToken, err := providers.ZedLLMToken(req.Ctx, req.Client, userID, accessToken)
	if err != nil {
		return err
	}

	upstreamBody, err := zedProviderRequest(provider, model, chat)
	if err != nil {
		return err
	}
	envelope, err := json.Marshal(map[string]any{
		"provider":         provider,
		"model":            model,
		"provider_request": json.RawMessage(upstreamBody),
	})
	if err != nil {
		return fmt.Errorf("zed: build body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(req.Ctx, http.MethodPost, providers.ZedCloudBase+"/completions", bytes.NewReader(envelope))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+llmToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		return fmt.Errorf("zed: upstream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if req.IsStream {
		return streamZed(w, req, provider, model, resp.Body)
	}
	return completeZed(w, req, provider, model, resp.Body)
}

// zedChat is the subset of an OpenAI chat request the Zed lane needs.
type zedChat struct {
	model     string
	system    string
	messages  []zedMessage
	maxTokens int
}

type zedMessage struct {
	role string
	text string
}

// parseZedChat flattens an OpenAI chat body. Content may be a string or an array
// of parts; only text parts are kept (see the ceiling note above).
func parseZedChat(body []byte) (zedChat, error) {
	var raw struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens           int `json:"max_tokens"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return zedChat{}, fmt.Errorf("zed: parse body: %w", err)
	}
	out := zedChat{model: raw.Model, maxTokens: raw.MaxTokens}
	if out.maxTokens == 0 {
		out.maxTokens = raw.MaxCompletionTokens
	}
	for _, m := range raw.Messages {
		text := zedTextOf(m.Content)
		if text == "" {
			continue
		}
		if m.Role == "system" || m.Role == "developer" {
			if out.system != "" {
				out.system += "\n\n"
			}
			out.system += text
			continue
		}
		out.messages = append(out.messages, zedMessage{role: m.Role, text: text})
	}
	if out.maxTokens <= 0 {
		out.maxTokens = 4096
	}
	return out, nil
}

// zedTextOf reduces an OpenAI content value to plain text.
func zedTextOf(content json.RawMessage) string {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
		return ""
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Text != "" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
