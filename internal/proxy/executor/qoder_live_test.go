package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestQoderLiveStream is opt-in: it needs a real Qoder account.
//
//	QODER_LIVE_TOKEN=<dt-…> QODER_LIVE_UID=<uuid> go test ./internal/proxy/executor/ -run QoderLive
//
// It exists because every failure mode here is invisible offline: the signature
// validates either way and the body shape only fails inside the upstream agent
// router, so a stub cannot tell a working request from one the upstream
// rejects.
func TestQoderLiveStream(t *testing.T) {
	token, uid := os.Getenv("QODER_LIVE_TOKEN"), os.Getenv("QODER_LIVE_UID")
	if token == "" || uid == "" {
		t.Skip("set QODER_LIVE_TOKEN and QODER_LIVE_UID to run")
	}
	model := os.Getenv("QODER_LIVE_MODEL")
	if model == "" {
		model = "dfmodel"
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:           context.Background(),
		Client:        &http.Client{Timeout: 60 * time.Second},
		APIKey:        token,
		IsStream:      true,
		ModelName:     model,
		Body:          []byte(`{"model":"x","messages":[{"role":"user","content":"say hi"}],"max_tokens":64}`),
		QoderIdentity: QoderIdentity{UserID: uid, MachineID: os.Getenv("QODER_LIVE_MACHINE")},
		ResponseBuf:   &strings.Builder{},
	}

	done := make(chan error, 1)
	go func() { done <- ForwardQoder(rec, req) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ForwardQoder: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("ForwardQoder did not return within 60s — the stream never terminated")
	}

	out := rec.Body.String()
	// Upstream account throttling (429 / "All backends failed", the account's
	// concurrency queue) is not a client defect; the executor already turned it
	// into a terminating error, which is what this test guards.
	if strings.Contains(out, "All backends failed") || strings.Contains(out, "429") {
		t.Skipf("upstream throttled this run: %q", out)
	}
	if !strings.Contains(out, `"delta"`) {
		t.Fatalf("no delta in response: %q", out)
	}
	// The upstream never closes the socket, so a correct executor must emit the
	// sentinel itself and return; this is the timeout regression guard.
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("stream did not end with [DONE]: %q", out[max(0, len(out)-120):])
	}
	if strings.Contains(out, "statusCodeValue") {
		t.Errorf("envelope leaked to the client: %q", out[:min(200, len(out))])
	}
}

// TestQoderLivePlanRefusal checks the operator-facing error path: a model
// outside the account's plan must answer with an actionable message instead of
// hanging or streaming an empty completion.
func TestQoderLivePlanRefusal(t *testing.T) {
	token, uid := os.Getenv("QODER_LIVE_TOKEN"), os.Getenv("QODER_LIVE_UID")
	if token == "" || uid == "" {
		t.Skip("set QODER_LIVE_TOKEN and QODER_LIVE_UID to run")
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:           context.Background(),
		Client:        &http.Client{Timeout: 60 * time.Second},
		APIKey:        token,
		IsStream:      true,
		ModelName:     "ultimate",
		Body:          []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`),
		QoderIdentity: QoderIdentity{UserID: uid},
		ResponseBuf:   &strings.Builder{},
	}

	done := make(chan error, 1)
	go func() { done <- ForwardQoder(rec, req) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("plan refusal did not terminate within 60s")
	}

	if !strings.Contains(rec.Body.String(), "not included in your current plan") {
		t.Errorf("want a plan message, got %q", rec.Body.String())
	}
}
