package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// Importing Zed models answers 401 unless the ladder is followed: the sign-in
// credential mints an LLM token for the account's organization, and only that
// token is accepted by /models.
func TestFetchZedModelsMintsLLMTokenBeforeListing(t *testing.T) {
	providers.ResetZedLLMTokens()
	t.Cleanup(providers.ResetZedLLMTokens)

	var sawMe, sawMint, sawList string
	var mintBody map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("/client/users/me", func(w http.ResponseWriter, r *http.Request) {
		sawMe = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"user":{"username":"boz"},"organizations":[{"id":"org-personal","is_personal":true}],"default_organization_id":"org-personal"}`)
	})
	mux.HandleFunc("/client/llm_tokens", func(w http.ResponseWriter, r *http.Request) {
		sawMint = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &mintBody)
		_, _ = io.WriteString(w, `{"token":"llm-token-1"}`)
	})
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		sawList = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"models":[
			{"id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5","max_token_count":200000,"max_output_tokens":64000}
		]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	origBase, origModels := providers.ZedCloudBase, zedModelsURL
	providers.ZedCloudBase = srv.URL
	zedModelsURL = srv.URL + "/models"
	t.Cleanup(func() { providers.ZedCloudBase, zedModelsURL = origBase, origModels })

	h := &Handler{}
	res := h.fetchZedModels("zed", `{"accessToken":"access-1","zedUserId":"41234"}`, srv.Client().Timeout)
	if res.Error != "" {
		t.Fatalf("fetch failed: %s", res.Error)
	}
	if sawMe != "41234 access-1" {
		t.Errorf("/client/users/me Authorization = %q", sawMe)
	}
	if sawMint != "41234 access-1" {
		t.Errorf("/client/llm_tokens Authorization = %q", sawMint)
	}
	if mintBody["organization_id"] != "org-personal" {
		t.Errorf("minted for organization %q, want the default organization", mintBody["organization_id"])
	}
	if sawList != "Bearer llm-token-1" {
		t.Errorf("/models Authorization = %q, want the minted bearer token", sawList)
	}
	if len(res.Models) != 1 || res.Models[0].ContextLength != 200000 || res.Models[0].MaxOutput != 64000 {
		t.Fatalf("models = %+v", res.Models)
	}
	if res.Models[0].Name != "Claude Sonnet 4.5" {
		t.Errorf("name = %q", res.Models[0].Name)
	}
}

// A stale cached token must be replaced once instead of surfacing a 401.
func TestFetchZedModelsRefreshesAStaleToken(t *testing.T) {
	providers.ResetZedLLMTokens()
	t.Cleanup(providers.ResetZedLLMTokens)

	var minted int
	mux := http.NewServeMux()
	mux.HandleFunc("/client/users/me", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"default_organization_id":"org-1","organizations":[]}`)
	})
	mux.HandleFunc("/client/llm_tokens", func(w http.ResponseWriter, r *http.Request) {
		minted++
		// First mint is stale on arrival, second is the good one.
		if minted == 1 {
			_, _ = io.WriteString(w, `{"token":"stale-token"}`)
			return
		}
		_, _ = io.WriteString(w, `{"token":"fresh-token"}`)
	})
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"models":[{"id":"zed-large","max_token_count":100000,"max_output_tokens":8000}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	origBase, origModels := providers.ZedCloudBase, zedModelsURL
	providers.ZedCloudBase = srv.URL
	zedModelsURL = srv.URL + "/models"
	t.Cleanup(func() { providers.ZedCloudBase, zedModelsURL = origBase, origModels })

	h := &Handler{}
	res := h.fetchZedModels("zed", `{"accessToken":"access-1","zedUserId":"41234"}`, srv.Client().Timeout)
	if res.Error != "" {
		t.Fatalf("fetch failed: %s", res.Error)
	}
	if res.Status != http.StatusOK || len(res.Models) != 1 {
		t.Fatalf("status %d, models %+v", res.Status, res.Models)
	}
	if minted != 2 {
		t.Fatalf("minted %d tokens, want 2 (stale then fresh)", minted)
	}
}

// A connection that has not been signed in must say so instead of hitting the
// network with half a credential.
func TestFetchZedModelsExplainsMissingSignIn(t *testing.T) {
	h := &Handler{}
	res := h.fetchZedModels("zed", `{"accessToken":"tok-1"}`, 0)
	if res.Error == "" || !strings.Contains(res.Error, "Sign in with Zed") {
		t.Fatalf("error = %q", res.Error)
	}
	if !res.Supported {
		t.Error("zed must stay a supported provider so the panel offers import")
	}
}
