package oauth

import (
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"strings"

	"9router/proxy/internal/log"
)

// DuplicateIdentity is a provider-scoped, stable identifier for an account.
//
// It exists because the connection ID is not an identity: adding the same
// account twice mints a fresh random ID each time, so the duplicate is invisible
// to the fallback logic — which then "falls back" between two copies of one
// account and burns the same quota twice. This was observed in practice with
// codebuddy-intl, where two connections shared one JWT `sub`.
//
// Key is intentionally narrow. Only fields that upstream issues per *account*
// (not per session) qualify, so providers whose normal usage is many unrelated
// credentials are never flagged:
//
//	codebuddy-intl / codebuddy-cn → JWT `sub` (Keycloak subject)
//	cline                         → JWT `sub`, then email
//	github                        → githubUserId, then githubLogin
//	antigravity                   → email
//
// An empty Kind means "no stable identity available"; callers must skip the
// duplicate check in that case rather than guess from the display name.
type DuplicateIdentity struct {
	Kind  string // "jwt_sub", "email", "github_user_id", "github_login"
	Value string
}

// Empty reports whether no stable identity was derivable.
func (d DuplicateIdentity) Empty() bool {
	return d.Kind == "" || strings.TrimSpace(d.Value) == ""
}

// Label renders a human-readable identity for error messages.
func (d DuplicateIdentity) Label() string {
	if d.Empty() {
		return ""
	}
	switch d.Value {
	case "":
		return d.Kind
	}
	return fmt.Sprintf("%s %s", d.Kind, d.Value)
}

// DuplicateError describes an add-account attempt that matched an existing
// connection. It carries the existing connection so the caller can offer a
// "replace instead" path, and so the message can name it.
type DuplicateError struct {
	Provider       string
	ExistingConnID string
	ExistingName   string
	Identity       DuplicateIdentity
}

func (e *DuplicateError) Error() string {
	who := e.Identity.Label()
	acct := e.ExistingName
	if acct == "" {
		acct = e.ExistingConnID
	}
	return fmt.Sprintf("this %s account is already connected as %q (%s); pass replace=true to update its token instead of adding a second copy", e.Provider, acct, who)
}

// FindDuplicateConnection scans the provider's existing connections for one
// whose stored identity matches the candidate. It returns nil when the provider
// has no stable identity concept, when nothing matches, or when the candidate
// identity itself is empty — never guessing from the display name, which is
// user-editable and provider-dependent.
func (h *OAuthHandler) FindDuplicateConnection(provider string, candidate DuplicateIdentity) (*DuplicateError, error) {
	if candidate.Empty() {
		return nil, nil
	}
	conns, err := h.Repo.GetProviderConnections(provider, false)
	if err != nil {
		return nil, fmt.Errorf("list %s connections: %w", provider, err)
	}
	for _, c := range conns {
		if c == nil {
			continue
		}
		// Only compare like with like: a `jwt_sub` must not match an `email`.
		got := extractStoredIdentity(provider, c.Data, derefStr(c.Email))
		if got.Empty() || got.Kind != candidate.Kind {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(got.Value), strings.TrimSpace(candidate.Value)) {
			return &DuplicateError{
				Provider:       provider,
				ExistingConnID: c.ID,
				ExistingName:   derefStr(c.Name),
				Identity:       got,
			}, nil
		}
	}
	return nil, nil
}

// extractStoredIdentity derives a connection's identity from its stored payload,
// mirroring the derivation used on the candidate side. Kept separate so both
// directions can be tested independently.
func extractStoredIdentity(provider string, data any, emailColumn string) DuplicateIdentity {
	m := toAnyMap(data)
	if m == nil {
		if emailColumn != "" {
			return DuplicateIdentity{Kind: "email", Value: emailColumn}
		}
		return DuplicateIdentity{}
	}
	return identityFromPayload(provider, m, emailColumn)
}

// identityForProvider picks the identity fields that are meaningful for a given
// provider, in priority order.
func identityForProvider(provider string, payload map[string]any, emailColumn string) DuplicateIdentity {
	psd := toAnyMap(payload["providerSpecificData"])

	// JWT-based providers. The `sub` claim is the account subject; it survives
	// token rotation, which is exactly why it catches duplicates that a token
	// comparison would miss.
	if isJWTIdentityProvider(provider) {
		if sub := jwtClaim(payload, "sub"); sub != "" {
			return DuplicateIdentity{Kind: "jwt_sub", Value: sub}
		}
	}

	switch {
	case provider == "github":
		// GitHub issues a numeric, immutable user id. Prefer it over the login,
		// which the user can rename.
		if id := anyToString(psd["githubUserId"]); id != "" {
			return DuplicateIdentity{Kind: "github_user_id", Value: id}
		}
		if login := anyToString(psd["githubLogin"]); login != "" {
			return DuplicateIdentity{Kind: "github_login", Value: login}
		}
	case provider == "antigravity":
		if e := firstNonEmpty(anyToString(psd["email"]), anyToString(payload["email"]), emailColumn); e != "" {
			return DuplicateIdentity{Kind: "email", Value: e}
		}
	}

	// Generic fallbacks: an explicit email anywhere in the payload, then the
	// indexed email column.
	if e := firstNonEmpty(anyToString(payload["email"]), anyToString(psd["email"]), emailColumn); e != "" {
		return DuplicateIdentity{Kind: "email", Value: e}
	}
	return DuplicateIdentity{}
}

// identityFromPayload is the shared derivation used by both the candidate and
// the stored-connection paths.
func identityFromPayload(provider string, payload map[string]any, emailColumn string) DuplicateIdentity {
	return identityForProvider(provider, payload, emailColumn)
}

// isJWTIdentityProvider lists providers whose upstream issues a JWT access
// token carrying a stable subject. Kept explicit so a new provider is opted in
// deliberately rather than by accident.
func isJWTIdentityProvider(provider string) bool {
	switch provider {
	case "codebuddy-intl", "codebuddy-cn", "codebuddy", "cline", "clinepass":
		return true
	}
	return false
}

// IdentityFromTokenFields builds a candidate identity for providers whose
// credential arrives as an OAuth payload rather than a raw JWT payload map.
// `jwtToken` is the access token (a JWT for codebuddy/cline); `email` is a
// plain fallback surfaced by the provider's userinfo.
func IdentityForProvider(provider, jwtToken, email string) DuplicateIdentity {
	if isJWTIdentityProvider(provider) {
		if sub := jwtSubject(jwtToken); sub != "" {
			return DuplicateIdentity{Kind: "jwt_sub", Value: sub}
		}
	}
	if e := strings.TrimSpace(email); e != "" {
		return DuplicateIdentity{Kind: "email", Value: e}
	}
	return DuplicateIdentity{}
}

// IdentityForGitHub builds a candidate identity from the Copilot identity
// lookup. Prefers the immutable numeric id.
func IdentityForGitHub(userID int64, login, email string) DuplicateIdentity {
	if userID != 0 {
		return DuplicateIdentity{Kind: "github_user_id", Value: fmt.Sprintf("%d", userID)}
	}
	if l := strings.TrimSpace(login); l != "" {
		return DuplicateIdentity{Kind: "github_login", Value: l}
	}
	if e := strings.TrimSpace(email); e != "" {
		return DuplicateIdentity{Kind: "email", Value: e}
	}
	return DuplicateIdentity{}
}

// jwtSubject decodes an unverified JWT payload and returns the `sub` claim.
// The signature is not checked: this value is only used to compare a new
// credential against one already stored for the same provider, on the operator's
// own machine. An attacker-controlled token cannot reach this path without
// already passing the provider's exchange step.
func jwtSubject(token string) string {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(parts[1], "="))
	if err != nil {
		// Tolerate padded base64 as well.
		if p2, e2 := base64.URLEncoding.DecodeString(parts[1]); e2 == nil {
			payload = p2
		} else {
			return ""
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	if sub, ok := claims["sub"].(string); ok {
		return strings.TrimSpace(sub)
	}
	return ""
}

// jwtClaim reads a claim from a JWT stored under the `accessToken` key of a
// connection payload.
func jwtClaim(payload map[string]any, claim string) string {
	tok := anyToString(payload["accessToken"])
	if tok == "" {
		return ""
	}
	if claim == "sub" {
		return jwtSubject(tok)
	}
	return ""
}

// toAnyMap coerces a decoded-JSON value (or raw JSON bytes/string) into a map.
// Connection `Data` arrives in several shapes across call sites, so normalising
// here keeps the identity logic uniform.
func toAnyMap(v any) map[string]any {
	switch t := v.(type) {
	case nil:
		return nil
	case map[string]any:
		return t
	case []byte:
		if len(t) == 0 {
			return nil
		}
		var m map[string]any
		if err := json.Unmarshal(t, &m); err != nil {
			return nil
		}
		return m
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(t), &m); err != nil {
			return nil
		}
		return m
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil
		}
		return m
	}
}

// anyToString renders a JSON scalar as a string, preserving integer precision
// for IDs that arrive as float64.
func anyToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		// JSON numbers decode to float64. Use a non-scientific format so an
		// id like 273372247 never becomes "2.73372247e+08".
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return strings.TrimSpace(fmt.Sprintf("%g", t))
	case int:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	case bool:
		return ""
	default:
		return ""
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// logDuplicateSuppressed records a suppressed duplicate so operators can see why
// an add-account attempt did not create a new row.
func logDuplicateSuppressed(provider string, dup *DuplicateError) {
	if dup == nil {
		return
	}
	log.Warn("oauth", "duplicate account rejected",
		"provider", provider,
		"existingConn", dup.ExistingConnID,
		"existingName", dup.ExistingName,
		"identity", dup.Identity.Label())
}

// derefStr safely dereferences an optional string column.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
