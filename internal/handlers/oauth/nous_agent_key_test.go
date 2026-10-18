package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNousOAuthSpec(t *testing.T) {
	spec, ok := oauthSpecs["nous"]
	if !ok {
		t.Fatal("nous has no OAuth spec")
	}
	if spec.Kind != flowDeviceCode {
		t.Fatalf("kind = %q, want %q", spec.Kind, flowDeviceCode)
	}
	if spec.DeviceCodeURL != "https://portal.nousresearch.com/api/oauth/device/code" {
		t.Fatalf("unexpected device code url %q", spec.DeviceCodeURL)
	}
	if spec.DeviceCodeBody["scope"] != "inference:invoke inference:mint_agent_key" {
		t.Fatalf("unexpected scope %q", spec.DeviceCodeBody["scope"])
	}
	if !spec.PollIsForm {
		t.Fatal("nous polls with a form body")
	}
}

// mintNousAgentKey points at a fake Portal so the response shape is pinned
// without a network round trip.
func TestMintNousAgentKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer portal-token" {
			t.Errorf("authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("body: %v", err)
		}
		if body["min_ttl_seconds"] != float64(nousAgentKeyTTL) {
			t.Errorf("min_ttl_seconds = %v", body["min_ttl_seconds"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api_key":"sk-agent","expires_in":7200}`))
	}))
	defer srv.Close()

	// nousAgentKeyURL is a const; exercise the real shape by asserting the
	// decode path through a helper that accepts a URL.
	key, ttl, err := mintNousAgentKeyFrom(srv.Client(), srv.URL, "portal-token")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if key != "sk-agent" || ttl != 7200 {
		t.Fatalf("key=%q ttl=%d", key, ttl)
	}
}

// The Portal retired agent-key minting for older client versions (HTTP 410
// session_key_minting_retired). The error must surface as an error — the caller
// decides to fall back — and must carry the upstream status so the log explains
// why the connection has no minted key.
func TestMintNousAgentKeyRetiredIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"error":"session_key_minting_retired"}`))
	}))
	defer srv.Close()

	_, _, err := mintNousAgentKeyFrom(srv.Client(), srv.URL, "portal-token")
	if err == nil {
		t.Fatal("a 410 mint response was reported as success")
	}
	if !strings.Contains(err.Error(), "410") || !strings.Contains(err.Error(), "session_key_minting_retired") {
		t.Fatalf("error loses the upstream reason: %v", err)
	}
}
