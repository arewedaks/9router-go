package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Workers AI keys its catalogue under an account id and has no OpenAI-style
// /models route, so the generic registry branch answers "does not support
// models listing" and VansRouter's derived URL answers 405. These tests pin the
// working route, the account-id requirement, and the pagination bounds.

// swapCloudflareBase points the production API base at a test server and
// returns the restore func. Declared here so the tests need no global state.
func swapCloudflareBase(u string) func() {
	old := cloudflareAPIBase
	cloudflareAPIBase = u
	return func() { cloudflareAPIBase = old }
}

func TestIsCloudflareProvider(t *testing.T) {
	for _, id := range []string{"cloudflare-ai", "cf", "CLOUDFLARE-AI", "cloudflare"} {
		if !isCloudflareProvider(id) {
			t.Errorf("isCloudflareProvider(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"openai", "cloudflare-ai-x", "", "c"} {
		if isCloudflareProvider(id) {
			t.Errorf("isCloudflareProvider(%q) = true, want false", id)
		}
	}
}

func TestCloudflareAccountID(t *testing.T) {
	cases := map[string]string{
		`{"providerSpecificData":{"accountId":"abc123"}}`:    "abc123",
		`{"providerSpecificData":{"account_id":"snake"}}`:    "snake",
		`{"providerSpecificData":{"accountID":"camel"}}`:     "camel",
		`{"accountId":"flat"} `:                              "flat", // legacy shape
		`{"providerSpecificData":{"accountId":"  padded "}}`: "padded",
		`{"providerSpecificData":{}}`:                        "",
		`{}`:                                                 "",
		``:                                                   "",
		`not json`:                                           "",
	}
	for in, want := range cases {
		if got := cloudflareAccountID(in); got != want {
			t.Errorf("cloudflareAccountID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCloudflareKindFromTask(t *testing.T) {
	cases := map[string]string{
		"Text Generation":              "llm",
		"Text-to-Text":                 "llm",
		"Text Embeddings":              "embedding",
		"Text-to-Image":                "image",
		"Image-to-Image":               "image",
		"Automatic Speech Recognition": "audio",
		"Text-to-Speech":               "audio",
		"":                             "llm", // unknown stays callable
		"Something New":                "llm",
	}
	for task, want := range cases {
		if got := cloudflareKindFromTask(task); got != want {
			t.Errorf("cloudflareKindFromTask(%q) = %q, want %q", task, got, want)
		}
	}
}

// The route, the account id in the path, and the bearer token are the three
// things that make Cloudflare work; assert all of them.
func TestFetchCloudflareModelsUsesSearchRouteWithAccountID(t *testing.T) {
	var gotPath, gotAuth, gotPerPage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotPerPage = r.URL.Query().Get("per_page")
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, map[string]any{
			"success": true,
			"result": []map[string]any{
				{"name": "@cf/meta/llama-3.2-3b-instruct", "task": map[string]any{"name": "Text Generation"}},
				{"name": "@cf/baai/bge-m3", "task": map[string]any{"name": "Text Embeddings"}},
			},
			"result_info": map[string]any{"page": 1, "per_page": 50, "count": 2, "total_count": 2},
		})
	}))
	defer srv.Close()
	defer swapCloudflareBase(srv.URL)()

	h := &Handler{}
	res, err := h.fetchCloudflareModels("cf", `{"apiKey":"tok","providerSpecificData":{"accountId":"acc-1"}}`, 5*time.Second)
	if err != nil {
		t.Fatalf("fetchCloudflareModels: %v", err)
	}
	if !res.Supported {
		t.Fatalf("Supported = false, want true")
	}
	if len(res.Models) != 2 {
		t.Fatalf("models = %d, want 2 (%+v)", len(res.Models), res.Models)
	}
	// The URL is the account-scoped search route, not /models.
	if !strings.Contains(gotPath, "/ai/models/search") {
		t.Errorf("path = %q, want the /ai/models/search route", gotPath)
	}
	if !strings.Contains(gotPath, "acc-1") {
		t.Errorf("path = %q, want the account id in it", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", gotAuth)
	}
	if gotPerPage != "50" {
		t.Errorf("per_page = %q, want 50", gotPerPage)
	}
}

// Without an account id the route cannot be built; the static catalogue must be
// returned instead of an error so Import still does something useful.
func TestFetchCloudflareModelsWithoutAccountIDFallsBack(t *testing.T) {
	// Point the base at a closed server: reaching the network here would be a bug.
	defer swapCloudflareBase("http://127.0.0.1:1/accounts")()
	h := &Handler{}
	res, err := h.fetchCloudflareModels("cloudflare-ai", `{"apiKey":"tok"}`, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Supported || len(res.Models) == 0 {
		t.Fatalf("want a supported fallback catalogue, got supported=%v models=%d", res.Supported, len(res.Models))
	}
	if !strings.Contains(res.Warning, "Account ID") {
		t.Errorf("warning = %q, want it to mention the Account ID", res.Warning)
	}
}

// No token: same fallback path, no network call.
func TestFetchCloudflareModelsWithoutTokenFallsBack(t *testing.T) {
	defer swapCloudflareBase("http://127.0.0.1:1/accounts")()
	h := &Handler{}
	res, err := h.fetchCloudflareModels("cloudflare-ai", `{"providerSpecificData":{"accountId":"a"}}`, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Supported || len(res.Models) == 0 {
		t.Fatalf("want a supported fallback catalogue, got supported=%v models=%d", res.Supported, len(res.Models))
	}
}

// fetchUpstreamModels must route Cloudflare before the generic registry branch,
// which has no entry for it and would answer "does not support models listing".
func TestFetchUpstreamModelsRoutesCloudflareAwayFromRegistryBranch(t *testing.T) {
	defer swapCloudflareBase("http://127.0.0.1:1/accounts")()
	h := &Handler{}
	res, err := h.fetchUpstreamModels("cf", `{"apiKey":"tok","providerSpecificData":{"accountId":"a"}}`, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Supported {
		t.Fatalf("Supported = false (the generic branch was reached): %+v", res)
	}
	if strings.Contains(res.Error, "does not support models listing") {
		t.Fatalf("Cloudflare fell through to the registry branch: %+v", res)
	}
	if len(res.Models) == 0 {
		t.Fatalf("no models returned")
	}
}
