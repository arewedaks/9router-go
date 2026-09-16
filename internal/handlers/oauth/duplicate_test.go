package oauth

import (
	"encoding/base64"
	json "encoding/json/v2"
	"strings"
	"testing"
)

// makeJWT builds an unsigned (alg=none style) JWT whose payload carries the
// given claims. Only the payload is read, which is all jwtSubject inspects.
func makeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	pl := base64.RawURLEncoding.EncodeToString(body)
	return hdr + "." + pl + ".signature"
}

func TestJWTSubject(t *testing.T) {
	tok := makeJWT(t, map[string]any{
		"sub":   "88e091b3-a29f-4ed5-992c-187ac660e262",
		"email": "tempeduai9@gmail.com",
	})

	if got := jwtSubject(tok); got != "88e091b3-a29f-4ed5-992c-187ac660e262" {
		t.Errorf("jwtSubject = %q, want the sub claim", got)
	}
}

func TestJWTSubjectRejectsNonJWT(t *testing.T) {
	cases := map[string]string{
		"empty":        "",
		"opaque":       "rUujJaersQ7oXOGMH2L1lDhga",
		"two parts":    "aaa.bbb",
		"bad base64":   "aaa.!!!!.ccc",
		"no sub claim": makeJWT(t, map[string]any{"email": "x@y.z"}),
	}
	for name, tok := range cases {
		if got := jwtSubject(tok); got != "" {
			t.Errorf("%s: jwtSubject = %q, want empty", name, got)
		}
	}
}

func TestJWTSubjectToleratesPadding(t *testing.T) {
	// A payload whose base64 length needs padding must still decode.
	tok := makeJWT(t, map[string]any{"sub": "abc"})
	if got := jwtSubject(tok); got != "abc" {
		t.Errorf("padded jwtSubject = %q, want abc", got)
	}
}

// TestIdentityForProviderCodebuddySubIsStable is the regression for the reported
// bug: two codebuddy-intl connections sharing one JWT `sub` are the same account.
func TestIdentityForProviderCodebuddySubIsStable(t *testing.T) {
	sub := "88e091b3-a29f-4ed5-992c-187ac660e262"
	// Two tokens issued six days apart: different iat/exp/jti, same subject.
	first := makeJWT(t, map[string]any{"sub": sub, "email": "tempeduai9@gmail.com", "iat": 1789059859})
	second := makeJWT(t, map[string]any{"sub": sub, "email": "tempeduai9@gmail.com", "iat": 1789299451})

	a := IdentityForProvider("codebuddy-intl", first, "tempeduai9@gmail.com")
	b := IdentityForProvider("codebuddy-intl", second, "tempeduai9@gmail.com")

	if a.Empty() || b.Empty() {
		t.Fatal("expected non-empty identities")
	}
	if a.Kind != "jwt_sub" {
		t.Errorf("kind = %q, want jwt_sub (subject is the stable account identity)", a.Kind)
	}
	if a != b {
		t.Errorf("token rotation produced different identities:\n  %+v\n  %+v", a, b)
	}
}

func TestIdentityForProviderFallsBackToEmail(t *testing.T) {
	// No JWT (opaque token) but an email is known.
	got := IdentityForProvider("codebuddy-intl", "opaque-token", "user@example.com")
	if got.Kind != "email" || got.Value != "user@example.com" {
		t.Errorf("identity = %+v, want email user@example.com", got)
	}
}

func TestIdentityForGitHubPrefersImmutableID(t *testing.T) {
	// A renamed login still maps to the same numeric id.
	a := IdentityForGitHub(273372247, "arewedaks", "")
	b := IdentityForGitHub(273372247, "arewedaks-renamed", "")
	if a.Kind != "github_user_id" {
		t.Errorf("kind = %q, want github_user_id", a.Kind)
	}
	if a != b {
		t.Errorf("login rename changed identity: %+v vs %+v", a, b)
	}
}

func TestIdentityForGitHubFallsBackToLogin(t *testing.T) {
	got := IdentityForGitHub(0, "arewedaks", "")
	if got.Kind != "github_login" || got.Value != "arewedaks" {
		t.Errorf("identity = %+v, want github_login arewedaks", got)
	}
}

// TestIdentityFromPayloadAntigravityUsesEmail covers the provider whose identity
// lives in providerSpecificData/email rather than a JWT.
func TestIdentityFromPayloadAntigravityUsesEmail(t *testing.T) {
	payload := map[string]any{
		"providerSpecificData": map[string]any{"email": "zzpajrizz6@gmail.com"},
	}
	got := identityFromPayload("antigravity", payload, "")
	if got.Kind != "email" || got.Value != "zzpajrizz6@gmail.com" {
		t.Errorf("identity = %+v, want email zzpajrizz6@gmail.com", got)
	}
}

// TestIdentityFromPayloadDistinctCloudflareAccountsAreNotDuplicates guards the
// most important non-goal: providers whose normal usage is many unrelated
// credentials must never be flagged. Cloudflare accounts are distinguished by
// accountId, so two different ids must yield different identities (or none).
func TestIdentityFromPayloadDistinctCloudflareAccountsAreNotDuplicates(t *testing.T) {
	a := map[string]any{"providerSpecificData": map[string]any{"accountId": "4199924fc8535bf2d2f9b52c0a1c523b"}}
	b := map[string]any{"providerSpecificData": map[string]any{"accountId": "a0fb42480351678ff738bcc7f2e2d060"}}

	ia := identityFromPayload("cloudflare-ai", a, "")
	ib := identityFromPayload("cloudflare-ai", b, "")
	if !ia.Empty() || !ib.Empty() {
		t.Errorf("cloudflare-ai must not produce a duplicate identity, got %+v / %+v", ia, ib)
	}
}

// TestIdentityFromPayloadKiroKeysAreNotDuplicates pins the same guarantee for
// API-key style providers, which carry no identity fields at all.
func TestIdentityFromPayloadKiroKeysAreNotDuplicates(t *testing.T) {
	payload := map[string]any{"apiKey": "sk-something"}
	if got := identityFromPayload("kiro", payload, ""); !got.Empty() {
		t.Errorf("kiro must not produce a duplicate identity, got %+v", got)
	}
}

func TestAnyToStringKeepsIntegerIDs(t *testing.T) {
	// JSON numbers arrive as float64; a large id must not become scientific.
	if got := anyToString(float64(273372247)); got != "273372247" {
		t.Errorf("anyToString(273372247) = %q, want 273372247", got)
	}
	if got := anyToString("  x  "); got != "x" {
		t.Errorf("anyToString should trim, got %q", got)
	}
	if got := anyToString(nil); got != "" {
		t.Errorf("anyToString(nil) = %q, want empty", got)
	}
	if got := anyToString(true); got != "" {
		t.Errorf("anyToString(bool) = %q, want empty", got)
	}
}

func TestDuplicateIdentityEmptyAndLabel(t *testing.T) {
	if !(DuplicateIdentity{}).Empty() {
		t.Error("zero identity should be empty")
	}
	if !(DuplicateIdentity{Kind: "email", Value: "  "}).Empty() {
		t.Error("whitespace value should be empty")
	}
	got := DuplicateIdentity{Kind: "jwt_sub", Value: "abc"}.Label()
	if !strings.Contains(got, "jwt_sub") || !strings.Contains(got, "abc") {
		t.Errorf("Label = %q, want it to mention kind and value", got)
	}
}

func TestDuplicateErrorMessageIsActionable(t *testing.T) {
	err := &DuplicateError{
		Provider:       "codebuddy-intl",
		ExistingConnID: "a003682e",
		ExistingName:   "Account 1",
		Identity:       DuplicateIdentity{Kind: "jwt_sub", Value: "88e091b3"},
	}
	msg := err.Error()
	for _, want := range []string{"codebuddy-intl", "Account 1", "88e091b3", "replace=true"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q should mention %q", msg, want)
		}
	}
}

func TestToAnyMapAcceptsMultipleShapes(t *testing.T) {
	raw := []byte(`{"email":"a@b.c"}`)
	for name, in := range map[string]any{
		"map":    map[string]any{"email": "a@b.c"},
		"bytes":  raw,
		"string": string(raw),
	} {
		m := toAnyMap(in)
		if m == nil || m["email"] != "a@b.c" {
			t.Errorf("%s: toAnyMap = %v", name, m)
		}
	}
	if toAnyMap(nil) != nil {
		t.Error("toAnyMap(nil) should be nil")
	}
	if toAnyMap("not json") != nil {
		t.Error("toAnyMap on invalid JSON should be nil")
	}
}
