// Package providers — Cline (cline.bot) request shaping.
//
// Cline authenticates through WorkOS. Its API rejects a plain `Bearer <token>`
// and requires the bearer value to carry a `workos:` prefix; a token without it
// answers
//
//	401 Unauthorized: Please make sure you're using the latest version of Cline
//	and re-authenticate your Cline account.
//
// The chat path normalizes the token via handlers/chat.NormalizeProviderToken.
// The helpers here exist so callers OUTSIDE that package (the dashboard model
// catalogue, and anything else that talks to Cline directly) can build the same
// header set without importing a handler. Keeping one definition of the prefix
// rule is the point: this mirrors it rather than re-inventing it.
package providers

import "strings"

// ClineClientVersion is the Cline client version advertised in the identity
// headers. Cline rejects requests whose client version is too old, so this is
// kept alongside the other Cline constants.
const ClineClientVersion = "3.0.61"

// ClineAccessToken normalizes a stored Cline token into the `workos:`-prefixed
// shape the API expects. It is idempotent: a token that already carries the
// prefix (a re-read of a previously normalized value) is returned untouched, so
// callers never produce `workos:workos:...`. An `sk_`-style BYOK key is left
// alone, because those ride a plain bearer (see ClinePass dual-auth). Empty or
// whitespace-only input yields an empty string.
//
// This mirrors the rule in handlers/chat.NormalizeProviderToken.
func ClineAccessToken(token string) string {
	t := strings.TrimSpace(token)
	if t == "" {
		return ""
	}
	if strings.HasPrefix(t, "workos:") || strings.HasPrefix(t, "sk_") {
		return t
	}
	return "workos:" + t
}

// ClineAuthHeader returns the complete `Authorization` header value for a Cline
// request, or an empty string when no usable token is present so callers can
// build unauthenticated probe headers.
func ClineAuthHeader(token string) string {
	normalized := ClineAccessToken(token)
	if normalized == "" {
		return ""
	}
	return "Bearer " + normalized
}

// ClineClientIdentityHeaders returns the Cline client-identification headers.
// Cline's gateway rejects requests that do not identify as a Cline client, and
// some endpoints (the model catalogue) require the full set rather than just
// the Authorization header.
//
// A fresh map is returned on every call so callers can mutate it freely.
func ClineClientIdentityHeaders() map[string]string {
	return map[string]string{
		"HTTP-Referer":       "https://cline.bot",
		"X-Title":            "Cline",
		"User-Agent":         "Cline/" + ClineClientVersion,
		"X-CLIENT-TYPE":      "cline-cli",
		"X-CLIENT-VERSION":   ClineClientVersion,
		"X-CORE-VERSION":     ClineClientVersion,
		"X-PLATFORM":         "linux",
		"X-PLATFORM-VERSION": ClineClientVersion,
		"X-IS-MULTIROOT":     "false",
	}
}
