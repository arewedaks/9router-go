package chat

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
)

// requestWithKey builds a request whose context carries the given API key, the
// way the auth middleware leaves it.
func requestWithKey(key *models.APIKey) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if key == nil {
		return r
	}
	return r.WithContext(middleware.WithApiKeyForTest(r.Context(), key))
}

// A key with no ACL configured must behave exactly as before: everything
// allowed. This is the compatibility guarantee for every pre-existing key.
func TestEnforceModelACL_NoRestrictionAllowsEverything(t *testing.T) {
	key := &models.APIKey{ID: "k1", Key: "sk-x", IsActive: 1}
	info := &ModelInfo{Provider: "openai", Model: "gpt-4o"}
	w := httptest.NewRecorder()

	if enforceModelACL(w, requestWithKey(key), info, "gpt-4o", "llm") {
		t.Fatalf("unrestricted key was rejected: %s", w.Body.String())
	}
}

// A provider the key is not granted must be refused with 403 before any
// upstream call.
func TestEnforceModelACL_DeniesUnlistedProvider(t *testing.T) {
	key := &models.APIKey{ID: "k1", Key: "sk-x", IsActive: 1, AllowedProviders: []string{"openai"}}
	info := &ModelInfo{Provider: "anthropic", Model: "claude-sonnet-4"}
	w := httptest.NewRecorder()

	if !enforceModelACL(w, requestWithKey(key), info, "claude-sonnet-4", "llm") {
		t.Fatal("key restricted to openai was allowed to reach anthropic")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// The listed provider must still work.
func TestEnforceModelACL_AllowsListedProvider(t *testing.T) {
	key := &models.APIKey{ID: "k1", Key: "sk-x", IsActive: 1, AllowedProviders: []string{"openai"}}
	info := &ModelInfo{Provider: "openai", Model: "gpt-4o"}
	w := httptest.NewRecorder()

	if enforceModelACL(w, requestWithKey(key), info, "gpt-4o", "llm") {
		t.Fatalf("listed provider was rejected: %s", w.Body.String())
	}
}

// An empty list means "nothing allowed" — distinct from nil.
func TestEnforceModelACL_EmptyListDeniesAll(t *testing.T) {
	key := &models.APIKey{ID: "k1", Key: "sk-x", IsActive: 1, AllowedProviders: []string{}}
	info := &ModelInfo{Provider: "openai", Model: "gpt-4o"}
	w := httptest.NewRecorder()

	if !enforceModelACL(w, requestWithKey(key), info, "gpt-4o", "llm") {
		t.Fatal("empty allowedProviders must deny everything")
	}
}

// A kind the key is not granted must be refused even when the provider is fine.
func TestEnforceModelACL_DeniesUnlistedKind(t *testing.T) {
	key := &models.APIKey{ID: "k1", Key: "sk-x", IsActive: 1, AllowedKinds: []string{"llm"}}
	info := &ModelInfo{Provider: "openai", Model: "text-embedding-3-small"}
	w := httptest.NewRecorder()

	if !enforceModelACL(w, requestWithKey(key), info, "text-embedding-3-small", "embedding") {
		t.Fatal("key restricted to llm was allowed an embedding request")
	}
}

// A combo must be blocked when the key is not granted it.
func TestEnforceModelACL_DeniesUnlistedCombo(t *testing.T) {
	key := &models.APIKey{ID: "k1", Key: "sk-x", IsActive: 1, AllowedCombos: []string{"fast"}}
	info := &ModelInfo{
		Provider:    "openai",
		Model:       "gpt-4o",
		ComboModels: []string{"openai/gpt-4o", "anthropic/claude-sonnet-4"},
		Strategy:    "fallback",
	}
	w := httptest.NewRecorder()

	if !enforceModelACL(w, requestWithKey(key), info, "combo/slow", "llm") {
		t.Fatal("key restricted to combo 'fast' was allowed combo 'slow'")
	}
}

// A combo the key IS granted must still not reach a provider the key is denied:
// otherwise the provider restriction is bypassable by fanning out.
func TestEnforceModelACL_ComboCannotBypassProviderRestriction(t *testing.T) {
	key := &models.APIKey{
		ID: "k1", Key: "sk-x", IsActive: 1,
		AllowedCombos:    []string{"mixed"},
		AllowedProviders: []string{"openai"},
	}
	info := &ModelInfo{
		Provider:    "openai",
		Model:       "gpt-4o",
		ComboModels: []string{"openai/gpt-4o", "anthropic/claude-sonnet-4"},
		Strategy:    "fallback",
	}
	w := httptest.NewRecorder()

	if !enforceModelACL(w, requestWithKey(key), info, "combo/mixed", "llm") {
		t.Fatal("combo reached a provider the key is not granted — restriction is bypassable")
	}
}

// A granted combo routing only to granted providers must pass.
func TestEnforceModelACL_ComboFullyGranted(t *testing.T) {
	key := &models.APIKey{
		ID: "k1", Key: "sk-x", IsActive: 1,
		AllowedCombos:    []string{"mixed"},
		AllowedProviders: []string{"openai", "anthropic"},
	}
	info := &ModelInfo{
		Provider:    "openai",
		Model:       "gpt-4o",
		ComboModels: []string{"openai/gpt-4o", "anthropic/claude-sonnet-4"},
		Strategy:    "fallback",
	}
	w := httptest.NewRecorder()

	if enforceModelACL(w, requestWithKey(key), info, "combo/mixed", "llm") {
		t.Fatalf("fully granted combo was rejected: %s", w.Body.String())
	}
}

// A dashboard-session request carries no API key in context and must never be
// blocked by a per-key ACL.
func TestEnforceModelACL_SessionRequestUnaffected(t *testing.T) {
	info := &ModelInfo{Provider: "anthropic", Model: "claude-sonnet-4"}
	w := httptest.NewRecorder()

	if enforceModelACL(w, requestWithKey(nil), info, "claude-sonnet-4", "llm") {
		t.Fatal("a session-authenticated request must not be subject to a key ACL")
	}
}

// providerOfComboEntry must resolve aliases so an ACL listing either form works.
func TestProviderOfComboEntry_ResolvesAlias(t *testing.T) {
	if got := providerOfComboEntry("cc/claude-sonnet-4"); got != "claude" {
		t.Errorf("providerOfComboEntry(cc/...) = %q, want claude", got)
	}
	if got := providerOfComboEntry("openai/gpt-4o"); got != "openai" {
		t.Errorf("providerOfComboEntry(openai/...) = %q, want openai", got)
	}
	if got := providerOfComboEntry("noprefix"); got != "" {
		t.Errorf("providerOfComboEntry(noprefix) = %q, want empty", got)
	}
}
