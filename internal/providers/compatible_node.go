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

// Compatible node types and compat modes as stored in providerNodes.
const (
	NodeTypeOpenAICompatible    = "openai-compatible"
	NodeTypeAnthropicCompatible = "anthropic-compatible"
)

// CompatModeCC marks an Anthropic node that speaks the Claude Code variant, which
// strips a versioned /messages suffix and has its own chat path.
const CompatModeCC = "cc"

var (
	// Trailing "/messages?<query>" exactly, as upstream sanitizeAnthropicBaseUrl
	// does. Note it must NOT swallow a preceding "/v1".
	anthropicMessagesSuffix = regexp.MustCompile(`(?i)/messages(?:\?[^#]*)?$`)
	// The Claude Code variant tolerates a version segment: "/(v1/)?messages".
	claudeCodeMessagesSuffix = regexp.MustCompile(`(?i)/(?:v\d+/)?messages(?:\?[^#]*)?$`)
	// Trailing "/chat/completions" or "/completions".
	openAIChatSuffix = regexp.MustCompile(`(?i)/(?:chat/)?completions$`)
)

// SanitizeBaseURL normalises an operator-supplied base URL so the executor can
// append a path without doubling segments. Mirrors upstream sanitize* helpers:
// trim, drop a trailing slash, then strip the endpoint suffix the operator may
// have pasted (people often paste the full chat URL instead of the root).
//
// compatMode selects the Claude Code variant for Anthropic nodes. An empty input
// stays empty: the create handler applies the type's default first, and validate
// surfaces "base URL required" rather than guessing.
func SanitizeBaseURL(baseURL, nodeType, compatMode string) string {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return ""
	}
	base = strings.TrimRight(base, "/")
	switch nodeType {
	case NodeTypeAnthropicCompatible:
		if compatMode == CompatModeCC {
			base = claudeCodeMessagesSuffix.ReplaceAllString(base, "")
		} else {
			base = anthropicMessagesSuffix.ReplaceAllString(base, "")
		}
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

// compatAPITypes maps the openai-compatible apiType to the path suffix the
// executor appends. Mirrors upstream getApiDefaultPath.
var compatAPITypes = map[string]struct{ Label, Path string }{
	"chat":                 {"Chat Completions", "/chat/completions"},
	"responses":            {"Responses API", "/responses"},
	"embeddings":           {"Embeddings", "/embeddings"},
	"audio-transcriptions": {"Audio Transcriptions", "/audio/transcriptions"},
	"audio-speech":         {"Audio Speech", "/audio/speech"},
	"images-generations":   {"Images Generations", "/images/generations"},
}

// APITypeLabel returns the protocol in words, as the compatible-node card shows
// it ("Messages API" for the Anthropic protocol, "Chat Completions" by default).
func APITypeLabel(nodeType, apiType string) string {
	if nodeType == NodeTypeAnthropicCompatible {
		return "Messages API"
	}
	if t, ok := compatAPITypes[apiType]; ok {
		return t.Label
	}
	return "Chat Completions"
}

// APIPath returns the endpoint path (leading slash removed) that a compatible
// node calls. An explicit chatPath wins, so the card shows the operator's own
// override rather than the default it replaced.
func APIPath(nodeType, compatMode, apiType, chatPath string) string {
	path := strings.TrimSpace(chatPath)
	if path == "" {
		path = defaultAPIPath(nodeType, compatMode, apiType)
	}
	return strings.TrimPrefix(path, "/")
}

func defaultAPIPath(nodeType, compatMode, apiType string) string {
	if nodeType == NodeTypeAnthropicCompatible {
		if compatMode == CompatModeCC {
			return "/v1/messages?beta=true"
		}
		return "/messages"
	}
	if t, ok := compatAPITypes[apiType]; ok {
		return t.Path
	}
	return "/chat/completions"
}
