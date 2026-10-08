package dashboard

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Adding the same credential twice must be refused.
//
// A duplicated key is never a second account: the router round-robins between
// two connections that reach the same upstream account, so one rate limit is
// consumed twice as fast while the dashboard reports redundancy that does not
// exist. Re-adding a key already present is almost always a paste of the same
// value, so the write is rejected instead of silently creating a twin.
func TestUpsertProviderRejectsDuplicateAPIKey(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	first := `{"provider":"openrouter","name":"acc-alpha","authType":"apikey","apiKey":"sk-same-key-111"}`
	if w := postJSON(t, r, "/api/dashboard/providers", first); w.Code != http.StatusOK {
		t.Fatalf("first add = %d, want 200: %s", w.Code, w.Body.String())
	}

	second := `{"provider":"openrouter","name":"acc-beta","authType":"apikey","apiKey":"sk-same-key-111"}`
	w := postJSON(t, r, "/api/dashboard/providers", second)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate key = %d, want 409: %s", w.Code, w.Body.String())
	}
	if got := decodeMap(t, w)["error"]; got == nil {
		t.Error("rejection should carry an error message")
	}
}

// A different key on the same provider must still be accepted: that is the
// ordinary case of connecting a second account.
func TestUpsertProviderAcceptsDistinctKeys(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	base := `{"provider":"openrouter","authType":"apikey","name":%q,"apiKey":%q}`
	for i, key := range []string{"sk-aaa-1", "sk-bbb-2", "sk-ccc-3"} {
		body := fmt.Sprintf(base, fmt.Sprintf("acc-%d", i), key)
		if w := postJSON(t, r, "/api/dashboard/providers", body); w.Code != http.StatusOK {
			t.Fatalf("distinct key %s = %d, want 200: %s", key, w.Code, w.Body.String())
		}
	}
}

// The same string under two providers is normal — one gateway key reused across
// endpoints — so the check must be scoped to a single provider.
func TestUpsertProviderAllowsSameKeyAcrossProviders(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	for _, prov := range []string{"openrouter", "cloudflare-ai"} {
		body := fmt.Sprintf(`{"provider":%q,"authType":"apikey","name":"shared","apiKey":"sk-shared-99"}`, prov)
		if w := postJSON(t, r, "/api/dashboard/providers", body); w.Code != http.StatusOK {
			t.Fatalf("same key across %s = %d, want 200: %s", prov, w.Code, w.Body.String())
		}
	}
}

// Editing an existing account without changing its key must not be refused as a
// duplicate of itself.
func TestUpsertProviderAllowsKeepingOwnKeyOnEdit(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	create := `{"id":"conn-keep","provider":"openrouter","name":"before","authType":"apikey","apiKey":"sk-keep-me-42"}`
	if w := postJSON(t, r, "/api/dashboard/providers", create); w.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200: %s", w.Code, w.Body.String())
	}

	// Same id, same key, new name — a rename through the same endpoint.
	edit := `{"id":"conn-keep","provider":"openrouter","name":"after","authType":"apikey","apiKey":"sk-keep-me-42"}`
	if w := postJSON(t, r, "/api/dashboard/providers", edit); w.Code != http.StatusOK {
		t.Fatalf("self-edit = %d, want 200 (must not collide with itself): %s", w.Code, w.Body.String())
	}
}

// Connections created elsewhere carry the secret under a different field, so
// the duplicate check has to understand those payloads too.
func TestUpsertProviderRejectsDuplicateAccessToken(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	first := `{"provider":"twinmind","name":"oauth-a","authType":"oauth","data":"{\"accessToken\":\"tok-xyz-7\"}"}`
	if w := postJSON(t, r, "/api/dashboard/providers", first); w.Code != http.StatusOK {
		t.Fatalf("first add = %d, want 200: %s", w.Code, w.Body.String())
	}

	second := `{"provider":"twinmind","name":"oauth-b","authType":"oauth","data":"{\"accessToken\":\"tok-xyz-7\"}"}`
	if w := postJSON(t, r, "/api/dashboard/providers", second); w.Code != http.StatusConflict {
		t.Fatalf("duplicate accessToken = %d, want 409: %s", w.Code, w.Body.String())
	}
}

// Keyless providers have no secret to compare; several of them must stay
// addable, and an empty key must never be treated as a duplicate of another
// empty key.
func TestUpsertProviderAllowsMultipleKeylessAccounts(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"provider":"opencode","name":"free-%d","authType":"none"}`, i)
		if w := postJSON(t, r, "/api/dashboard/providers", body); w.Code != http.StatusOK {
			t.Fatalf("keyless account %d = %d, want 200: %s", i, w.Code, w.Body.String())
		}
	}
}

// A bulk paste must say *why* a key was refused. Reporting only a failure count
// leaves the operator guessing whether the key was a duplicate, malformed, or
// the server was down — and a duplicate is the one case they can act on.
func TestBulkAddReportsDuplicateReason(t *testing.T) {
	ui := readEmbeddedUI(t)

	body := extractFunc(t, ui, "async function addBulkConnections")
	if !strings.Contains(body, "reasons.push") {
		t.Error("bulk add should collect the reason each key was refused")
	}
	if !strings.Contains(body, "errorMessage(data, res.status)") {
		t.Error("bulk add should surface the server's error message, not just a count")
	}
	if !strings.Contains(body, "reasons.join") {
		t.Error("bulk add should include the collected reasons in the status line")
	}
}
