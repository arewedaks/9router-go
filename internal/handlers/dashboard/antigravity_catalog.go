package dashboard

import (
	json "encoding/json/v2"
	"strings"

	"9router/proxy/internal/providers"
)

// resolveAliasFn is an indirection over providers.ResolveAlias so tests can
// stub alias resolution without importing the providers package everywhere.
var resolveAliasFn = providers.ResolveAlias

// antigravityStaticModel pairs an upstream id with a friendly label.
type antigravityStaticModel struct {
	ID   string
	Name string
}

// antigravityStaticCatalog is the fallback catalogue used when live discovery
// is unreachable (offline phone, blocked daily-* host, expired token). It
// reflects the user-callable models observed on a live Antigravity account and
// is intentionally conservative: only ids that the current client can call.
var antigravityStaticCatalog = []antigravityStaticModel{
	{"gemini-3.8-flash-tiered", "Gemini 3.8 Flash (Tiered)"},
	{"gemini-3.8-flash-high", "Gemini 3.8 Flash (High)"},
	{"gemini-3.8-flash-medium", "Gemini 3.8 Flash (Medium)"},
	{"gemini-3.8-flash-low", "Gemini 3.8 Flash (Low)"},
	{"gemini-3.7-flash-tiered", "Gemini 3.7 Flash (Tiered)"},
	{"gemini-3.7-flash-high", "Gemini 3.7 Flash (High)"},
	{"gemini-3.7-flash-medium", "Gemini 3.7 Flash (Medium)"},
	{"gemini-3.7-flash-low", "Gemini 3.7 Flash (Low)"},
	{"gemini-3.6-flash-tiered", "Gemini 3.6 Flash (Tiered)"},
	{"gemini-3.6-flash-high", "Gemini 3.6 Flash (High)"},
	{"gemini-3.6-flash-medium", "Gemini 3.6 Flash (Medium)"},
	{"gemini-3.6-flash-low", "Gemini 3.6 Flash (Low)"},
	{"gemini-pro-agent", "Gemini 3.1 Pro (High)"},
	{"gemini-3.1-pro-low", "Gemini 3.1 Pro (Low)"},
	{"gemini-3.1-pro-high", "Gemini 3.1 Pro (High)"},
	{"gemini-3.1-flash-lite", "Gemini 3.1 Flash Lite"},
	{"gemini-3.5-flash-lite", "Gemini 3.5 Flash Lite"},
	{"gemini-3-flash", "Gemini 3 Flash"},
	{"claude-opus-4-6-thinking", "Claude Opus 4.6 (Thinking)"},
	{"claude-sonnet-4-6", "Claude Sonnet 4.6 (Thinking)"},
	{"gpt-oss-120b-medium", "GPT-OSS 120B (Medium)"},
}

// antigravityStaticModels returns the fallback catalogue, filtered through the
// same discoverability rules as the live path so the two never disagree.
func antigravityStaticModels() []UpstreamModel {
	out := make([]UpstreamModel, 0, len(antigravityStaticCatalog))
	for _, m := range antigravityStaticCatalog {
		if !isDiscoverableAntigravityModel(m.ID) {
			continue
		}
		out = append(out, UpstreamModel{
			ID:   m.ID,
			Name: m.Name,
			Kind: inferModelKind(m.ID),
		})
	}
	return out
}

// isAntigravityProvider reports whether a provider id (canonical or alias)
// addresses the Antigravity / Cloud Code provider.
func isAntigravityProvider(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "antigravity", "agy":
		return true
	default:
		return false
	}
}

// antigravityUserAgentForData resolves the User-Agent a connection should
// present, based on its stored client profile (ide|cli). Falls back to the IDE
// identity when the profile is absent or the data cannot be parsed.
func antigravityUserAgentForData(data string) string {
	if strings.TrimSpace(data) == "" {
		return antigravityUserAgent
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		return antigravityUserAgent
	}
	return providers.AntigravityUserAgentForData(parsed)
}

// antigravityClientProfileFromData extracts the normalized client profile from
// a connection data blob (defaults to "ide").
func antigravityClientProfileFromData(data string) providers.AntigravityClientProfile {
	if strings.TrimSpace(data) == "" {
		return providers.AntigravityProfileIDE
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		return providers.AntigravityProfileIDE
	}
	return providers.ClientProfileFromData(parsed)
}
