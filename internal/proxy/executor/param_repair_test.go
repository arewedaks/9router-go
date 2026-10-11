package executor

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/paramfix"
)

// proxyForward exercises the repair path the executor actually uses, so the
// tests do not depend on a wrapper that only exists for callers holding a
// first response.
func proxyForward(t *testing.T, req *Request) (*http.Response, error) {
	t.Helper()
	result := forwardWithParamRepair(req)
	return result.resp, result.err
}

// decodeBody reads a request body into a map for assertions.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, raw)
	}
	return m
}

// A strict OpenAI-compatible gateway rejects the whole request over `store`,
// which a client SDK added by default. The repair drops the field and the retry
// succeeds, so the caller never sees the 400 that used to break the provider.
func TestRepairRetriesWithoutRejectedField(t *testing.T) {
	var attempts int
	var sawStore []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body := decodeBody(t, r)
		_, hasStore := body["store"]
		sawStore = append(sawStore, hasStore)

		if hasStore {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"Unrecognized request arguments supplied: store","type":"invalid_request_error"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	req := &Request{
		Ctx:    context.Background(),
		Client: srv.Client(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL},
		Body:   []byte(`{"model":"solar-pro4","store":false,"messages":[{"role":"user","content":"hi"}]}`),
	}

	resp, err := proxyForward(t, req)
	if err != nil {
		t.Fatalf("forward failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected the retry to succeed, got %d", resp.StatusCode)
	}
	if attempts != 2 {
		t.Errorf("expected exactly 2 attempts (original + repair), got %d", attempts)
	}
	if len(sawStore) != 2 || !sawStore[0] || sawStore[1] {
		t.Errorf("first attempt must carry store and the retry must not: %v", sawStore)
	}
}

// A rejection naming a field that changes the request's meaning must be passed
// through untouched — silently dropping `messages` would serve a different
// request than the caller asked for.
func TestRepairLeavesUnsafeFieldsAlone(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Unrecognized request arguments supplied: messages"}`))
	}))
	defer srv.Close()

	req := &Request{
		Ctx:    context.Background(),
		Client: srv.Client(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL},
		Body:   []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
	}

	result := forwardWithParamRepair(req)
	if attempts != 1 {
		t.Errorf("an unsafe field must not trigger a retry, got %d attempts", attempts)
	}
	var uErr *proxy.UpstreamError
	if !errors.As(result.err, &uErr) {
		t.Fatalf("the upstream 400 must reach the caller, got %v", result.err)
	}
	if !strings.Contains(string(uErr.Body), "messages") {
		t.Errorf("the upstream error body must survive, got %s", uErr.Body)
	}
	// The unmodified body must be what a retry would have sent.
	if !strings.Contains(string(result.body), "messages") {
		t.Errorf("the request body must be unchanged, got %s", result.body)
	}
}

// A 400 that is not a parameter rejection (context overflow, bad model) must
// reach the caller unchanged, with its body intact.
func TestRepairPassesThroughUnrelatedErrors(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"context length exceeded"}}`))
	}))
	defer srv.Close()

	req := &Request{
		Ctx:    context.Background(),
		Client: srv.Client(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL},
		Body:   []byte(`{"model":"m","store":false}`),
	}

	result := forwardWithParamRepair(req)
	if attempts != 1 {
		t.Errorf("an unrelated 400 must not retry, got %d attempts", attempts)
	}
	var uErr *proxy.UpstreamError
	if !errors.As(result.err, &uErr) {
		t.Fatalf("the upstream 400 must reach the caller, got %v", result.err)
	}
	if !strings.Contains(string(uErr.Body), "context length exceeded") {
		t.Errorf("error body must reach the caller, got %s", uErr.Body)
	}
}

// The repair must stop after maxParamRepairs fields: a provider that rejects
// every field in turn would otherwise loop forever.
func TestRepairBudgetIsBounded(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body := decodeBody(t, r)
		for _, f := range []string{"store", "stream_options", "service_tier", "metadata", "user", "prompt_cache_key"} {
			if _, ok := body[f]; ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"Unrecognized request arguments supplied: ` + f + `"}`))
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	req := &Request{
		Ctx:    context.Background(),
		Client: srv.Client(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL},
		Body:   []byte(`{"model":"m","store":false,"stream_options":{},"service_tier":"auto","metadata":{},"user":"u","prompt_cache_key":"k"}`),
	}

	_ = forwardWithParamRepair(req)
	if attempts > paramfix.MaxRepairs+1 {
		t.Errorf("repair must be bounded to %d extra attempts, got %d total", paramfix.MaxRepairs, attempts)
	}
}
