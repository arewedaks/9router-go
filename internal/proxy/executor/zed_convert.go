package executor

import (
	"encoding/json"
	"fmt"
)

// zedProviderRequest builds the native request each provider expects inside the
// wrapper. Shapes were measured against the live endpoint:
//
//	anthropic → Messages API   {"model","max_tokens","system","messages":[{"role","content":[{"type":"text","text"}]}]}
//	open_ai   → Responses API  {"model","input":[{"role","content":[{"type":"input_text","text"}]}],"max_output_tokens"}
//	google    → generateContent {"contents":[{"role","parts":[{"text"}]}],"systemInstruction":{"parts":[{"text"}]}}
func zedProviderRequest(provider, model string, chat zedChat) ([]byte, error) {
	switch provider {
	case "anthropic":
		type block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		type message struct {
			Role    string  `json:"role"`
			Content []block `json:"content"`
		}
		msgs := make([]message, 0, len(chat.messages))
		for _, m := range chat.messages {
			role := m.role
			if role != "assistant" {
				role = "user"
			}
			msgs = append(msgs, message{Role: role, Content: []block{{Type: "text", Text: m.text}}})
		}
		payload := map[string]any{"model": model, "max_tokens": chat.maxTokens, "messages": msgs}
		if chat.system != "" {
			payload["system"] = chat.system
		}
		return json.Marshal(payload)

	case "open_ai":
		type part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		type item struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []part `json:"content"`
		}
		input := make([]item, 0, len(chat.messages))
		for _, m := range chat.messages {
			role := m.role
			if role != "assistant" {
				role = "user"
			}
			input = append(input, item{Type: "message", Role: role, Content: []part{{Type: "input_text", Text: m.text}}})
		}
		// Zed's open_ai lane returns an empty body unless the Responses API is
		// asked to stream, so stream is forced and unwrapped either way.
		payload := map[string]any{"model": model, "input": input, "max_output_tokens": chat.maxTokens, "stream": true}
		if chat.system != "" {
			payload["instructions"] = chat.system
		}
		return json.Marshal(payload)

	case "google":
		type part struct {
			Text string `json:"text"`
		}
		type content struct {
			Role  string `json:"role"`
			Parts []part `json:"parts"`
		}
		contents := make([]content, 0, len(chat.messages))
		for _, m := range chat.messages {
			role := m.role
			if role == "assistant" {
				role = "model"
			} else {
				role = "user"
			}
			contents = append(contents, content{Role: role, Parts: []part{{Text: m.text}}})
		}
		payload := map[string]any{"contents": contents}
		if chat.system != "" {
			payload["systemInstruction"] = map[string]any{"parts": []part{{Text: chat.system}}}
		}
		return json.Marshal(payload)

	default:
		return nil, fmt.Errorf("zed: unsupported provider %q for model %s", provider, model)
	}
}
