package chat

import (
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
)

// ACL enforcement for client API keys.
//
// An API key may carry three access lists — allowedProviders, allowedCombos,
// allowedKinds — with the shared semantics (see models.APIKey): nil means
// everything is allowed, an empty list means nothing is, and a populated list
// means only its entries. A key with no lists configured (every key created
// before this feature) is therefore unrestricted, exactly as before.
//
// Enforcement happens here rather than in middleware because only the handler
// knows what the request actually resolves to: the provider comes from the
// model/combo lookup, and the kind comes from the endpoint. The middleware
// authenticates; the handler authorises.

// EnforceModelACL is the exported entry point for the media handlers, which
// resolve a model through ChatHandler but own their own kind ("embedding",
// "image", …). Chat handlers call enforceModelACL directly.
//
// Returns true when the request was rejected (the caller must stop).
func EnforceModelACL(w http.ResponseWriter, r *http.Request, modelInfo *ModelInfo, requested string, kind string) bool {
	return enforceModelACL(w, r, modelInfo, requested, kind)
}

// providerOfComboEntry extracts the provider from a "provider/model" combo
// entry, resolving the short alias to its canonical id so an ACL listing either
// form matches. An entry without a slash carries no provider.
func providerOfComboEntry(entry string) string {
	prefix, _, ok := strings.Cut(entry, "/")
	if !ok {
		return ""
	}
	return resolveProviderAlias(prefix)
}

// aclDenied writes the 403 an ACL rejection produces and reports whether it
// wrote. The message names the denied resource so a client can tell a
// permission failure from an auth failure.
func aclDenied(w http.ResponseWriter, resource, name string) {
	handlerutil.WriteJSONError(w, http.StatusForbidden,
		"API key is not permitted to use this "+resource+": "+name)
}

// enforceProviderACL rejects the request when the key may not use the resolved
// provider. Returns true when the request was rejected (the caller must stop).
func enforceProviderACL(w http.ResponseWriter, r *http.Request, provider string) bool {
	key := middleware.GetAuthenticatedApiKey(r)
	if key == nil {
		// No key in context means the request was authenticated by a dashboard
		// session cookie, which is the operator's own credential and carries no
		// per-key restriction.
		return false
	}
	if !key.IsProviderAllowed(provider) {
		aclDenied(w, "provider", provider)
		return true
	}
	return false
}

// enforceComboACL rejects the request when the key may not use the named combo.
// Returns true when the request was rejected.
func enforceComboACL(w http.ResponseWriter, r *http.Request, combo string) bool {
	key := middleware.GetAuthenticatedApiKey(r)
	if key == nil {
		return false
	}
	if !key.IsComboAllowed(combo) {
		aclDenied(w, "combo", combo)
		return true
	}
	return false
}

// enforceKindACL rejects the request when the key may not issue this kind of
// request ("llm", "embedding", "image", "tts", "stt", "web"). Returns true when
// the request was rejected.
func enforceKindACL(w http.ResponseWriter, r *http.Request, kind string) bool {
	key := middleware.GetAuthenticatedApiKey(r)
	if key == nil {
		return false
	}
	if !key.IsKindAllowed(kind) {
		aclDenied(w, "request kind", kind)
		return true
	}
	return false
}

// enforceModelACL applies the provider and kind rules to a resolved model.
//
// A combo is checked against allowedCombos by its alias and against
// allowedProviders by every provider it can route to — a key restricted to one
// provider must not reach another one through a combo. A plain model is checked
// against its single provider. The kind is "llm" for every chat-format endpoint.
//
// Returns true when the request was rejected.
func enforceModelACL(w http.ResponseWriter, r *http.Request, modelInfo *ModelInfo, requested string, kind string) bool {
	if enforceKindACL(w, r, kind) {
		return true
	}

	if len(modelInfo.ComboModels) > 0 {
		if enforceComboACL(w, r, requested) {
			return true
		}
		// Every provider the combo may route to must be permitted, otherwise the
		// restriction would be bypassable by picking a combo that fans out.
		for _, entry := range modelInfo.ComboModels {
			provider := providerOfComboEntry(entry)
			if provider == "" {
				continue
			}
			if enforceProviderACL(w, r, provider) {
				return true
			}
		}
		return false
	}

	return enforceProviderACL(w, r, modelInfo.Provider)
}
