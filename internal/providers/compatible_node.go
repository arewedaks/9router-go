package providers

import (
	"regexp"
	"strings"
)

// Compatible provider node ids mirror upstream OmniRoute
// (src/app/api/provider-nodes/route.ts): the id embeds the node kind so the
// resolution layer can tell an OpenAI-compatible endpoint from an Anthropic one
// without loading the row. The id is internal — the prefix is what routing uses.
const (
	OpenAICompatiblePrefix    = "openai-compatible-"
	AnthropicCompatiblePrefix = "anthropic-compatible-"
	ClaudeCodeCompatibleNode  = "claude-code-compatible-"
)

// Compatible node types as stored in providerNodes.type.
const (
	NodeTypeOpenAICompatible    = "openai-compatible"
	NodeTypeAnthropicCompatible = "anthropic-compatible"
)

var (
	// Trailing "/messages" (optionally versioned) and its query string, e.g.
	// "https://api.anthropic.com/v1/messages?beta=true".
	anthropicMessagesSuffix = regexp.MustCompile(`(?i)/(?:v\d+/)?messages(?:\?[^#]*)?$`)
	// Trailing "/chat/completions" or "/completions".
	openAIChatSuffix = regexp.MustCompile(`(?i)/(?:chat/)?completions$`)
)

// SanitizeBaseURL normalises an operator-supplied base URL so the executor can
// append a path without doubling segments. Mirrors upstream sanitize* helpers:
// trim, drop a trailing slash, then strip the endpoint suffix the operator may
// have pasted (people often paste the full chat URL instead of the root).
//
// An empty input stays empty: the create handler applies the type's default
// first, and validate surfaces "base URL required" rather than guessing.
func SanitizeBaseURL(baseURL, nodeType string) string {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return ""
	}
	base = strings.TrimRight(base, "/")
	switch nodeType {
	case NodeTypeAnthropicCompatible:
		base = anthropicMessagesSuffix.ReplaceAllString(base, "")
	default:
		base = openAIChatSuffix.ReplaceAllString(base, "")
	}
	return strings.TrimRight(base, "/")
}

// CompatibleNodeID builds the generated id for a new compatible node, e.g.
// "openai-compatible-chat-<uuid>". apiType is only meaningful for OpenAI
// endpoints (chat/responses/embeddings/...); Anthropic nodes omit it.
func CompatibleNodeID(nodeType, apiType, uuid string) string {
	switch nodeType {
	case NodeTypeAnthropicCompatible:
		return AnthropicCompatiblePrefix + uuid
	default:
		at := strings.TrimSpace(apiType)
		if at == "" {
			at = "chat"
		}
		return OpenAICompatiblePrefix + at + "-" + uuid
	}
}

// NodePrefixCollision reports whether another node already owns the prefix. A
// prefix routes "<prefix>/<model>" to exactly one node, so a duplicate would make
// models ambiguous — the create handler rejects it instead of silently choosing.
// excludeID lets a future update handler ignore the row being edited.
func NodePrefixCollision(existing map[string]string, prefix, excludeID string) (string, bool) {
	p := strings.TrimSpace(prefix)
	if p == "" {
		return "", false
	}
	for id, existingPrefix := range existing {
		if id == excludeID {
			continue
		}
		if strings.TrimSpace(existingPrefix) == p {
			return id, true
		}
	}
	return "", false
}
