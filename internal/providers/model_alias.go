package providers

import "strings"

// OutputAlias resolves the short alias a model id should be exported under,
// mirroring the upstream helper in `src/sse/services/allowedModels.js`:
//
//	const outputAlias = (
//	  conn?.providerSpecificData?.prefix   // 1. the node's configured prefix
//	  || getProviderAlias(providerId)      // 2. the static alias for the provider
//	  || staticAlias                       // 3. the provider id itself
//	).trim();
//
// Every exported model id is then built as `${outputAlias}/${modelId}`, so the
// prefix always wins over the provider's internal id. That is the whole point:
// a custom endpoint is stored under a generated key
// ("openai-compatible-chat-<uuid>", built upstream as
// `${PREFIX}${apiType}-${randomUUID()}`) but must be advertised to clients as
// `atri/glm-4.7`, never as `<uuid>/glm-4.7`.
//
// `prefix` is the node's configured prefix (empty when the caller has no node);
// `providerID` is the raw provider/node key. Returns the provider id unchanged
// when nothing more specific is known, so unknown providers keep working.
func OutputAlias(providerID, prefix string) string {
	if p := strings.TrimSpace(prefix); p != "" {
		return p
	}
	norm := strings.ToLower(strings.TrimSpace(providerID))
	if canonical, ok := ProviderAliasMap[norm]; ok {
		return canonical
	}
	// A generated compatible node id has no static alias; fall back to the raw
	// id only as a last resort, exactly like upstream's `staticAlias`.
	return strings.TrimSpace(providerID)
}

// NormalizeModelAlias maps a stored custom-model provider alias onto the short
// alias used for export.
//
// Upstream accepts a custom model whose `providerAlias` is any of: the static
// alias, the output alias (prefix), or the raw provider id — see the guard in
// `allowedModels.js`:
//
//	if (alias !== staticAlias && alias !== outputAlias && alias !== providerId) return acc;
//
// A restore of a VansRouter backup therefore legitimately stores generated ids
// such as "anthropic-compatible-0b2ee40e-…" in `customModels.providerAlias`.
// Exporting that verbatim is what produced 1500 uuid-prefixed model names.
// This helper returns the alias to advertise: the node's prefix when the alias
// names a known node, otherwise the alias unchanged.
func NormalizeModelAlias(alias string, nodePrefixByID map[string]string) string {
	key := strings.TrimSpace(alias)
	if key == "" {
		return key
	}
	if prefix, ok := nodePrefixByID[key]; ok && strings.TrimSpace(prefix) != "" {
		return strings.TrimSpace(prefix)
	}
	return key
}
