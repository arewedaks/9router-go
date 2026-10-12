package chat

import (
	"context"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/translator"
)

// resolveJailbreak returns the injection document for a provider, or nil when
// nothing should be injected: a provider toggle left off, or an empty document
// with the toggle on, both mean pass-through.
func (h *ChatHandler) resolveJailbreak(provider string) *db.JailbreakDoc {
	if h.Repo == nil {
		return nil
	}
	on, err := h.Repo.JailbreakProviderEnabled(provider)
	if err != nil {
		log.Warn("jailbreak", "toggle lookup failed", "provider", provider, "error", err)
		return nil
	}
	if !on {
		return nil
	}
	doc, err := h.Repo.GetJailbreakDoc()
	if err != nil {
		log.Warn("jailbreak", "doc lookup failed", "provider", provider, "error", err)
		return nil
	}
	if doc.Prompt == "" {
		return nil
	}
	return doc
}

// applyJailbreak mutates the outbound request body for a provider whose toggle
// is on and returns a context carrying the Gemini-safety flag for the forward
// chain. Called before the body is marshalled, so the prompt-cache hash always
// reflects the injected prompt. Every forward path calls this: single chat,
// Claude single, combo fallback, Claude combo, fusion panel, fusion judge.
func (h *ChatHandler) applyJailbreak(ctx context.Context, body map[string]any, model, provider string) context.Context {
	doc := h.resolveJailbreak(provider)
	if doc == nil {
		return ctx
	}
	prependSystemPrompt(body, doc.Prompt)
	log.Info("jailbreak", "applied", "model", model, "provider", provider,
		"prompt_bytes", len(doc.Prompt), "safety_off", doc.GeminiSafety)
	if doc.GeminiSafety {
		ctx = translator.WithGeminiSafety(ctx, true)
	}
	return ctx
}

// prependSystemPrompt injects the configured prompt ahead of the original
// content. Two wire formats reach here: OpenAI (system role in "messages") and
// Claude (top-level "system", string or block array). Both get the prompt
// first, original content intact after it.
func prependSystemPrompt(body map[string]any, prompt string) {
	if raw, ok := body["system"]; ok {
		switch v := raw.(type) {
		case string:
			body["system"] = prompt + "\n\n" + v
			return
		case []any:
			body["system"] = append([]any{map[string]any{"type": "text", "text": prompt}}, v...)
			return
		}
	}
	msgs, _ := body["messages"].([]any)
	body["messages"] = append([]any{map[string]any{"role": "system", "content": prompt}}, msgs...)
}
