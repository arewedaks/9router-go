package dashboard

import (
	"os"
	"strings"
	"testing"
)

// A single-entry test is diagnostic: it must report one URL's health without
// touching the pool's active flag, or testing one dead proxy would take the
// whole pool offline while the other entries still work.
func TestProxyPoolSingleURLTestDoesNotFlipPoolState(t *testing.T) {
	src, err := os.ReadFile("proxy_pool_admin_handler.go")
	if err != nil {
		t.Fatalf("read handler: %v", err)
	}
	body := string(src)

	idx := strings.Index(body, `if target := strings.TrimSpace(payload.URL); target != "" {`)
	if idx == -1 {
		t.Fatal("HandleTestProxyPool must branch on a per-URL payload")
	}
	// The branch ends at the next top-level return, which is where the pool-wide
	// path (and its recordAndRespond call) resumes.
	arm := body[idx:]
	if end := strings.Index(arm, "\n\ttarget := firstProxyURL(pool)"); end != -1 {
		arm = arm[:end]
	}
	if strings.Contains(arm, "recordAndRespond") {
		t.Error("a single-URL test must not record a pool-wide status")
	}
	if !strings.Contains(arm, "probeProxy") {
		t.Error("a single-URL test must still run the CONNECT probe")
	}
}

// The details modal must offer a per-entry test that sends the URL, since the
// pool-level Test button only ever probes the first entry.
func TestProxyDetailsOffersPerURLTest(t *testing.T) {
	ui := readEmbeddedUI(t)
	fn := extractFunction(t, ui, "testProxyEditList")
	if fn == "" {
		t.Fatal("UI lacks testProxyEditList for the pool details modal")
	}
	if !strings.Contains(fn, "/test") {
		t.Error("per-URL test must call the pool test endpoint")
	}
	if !strings.Contains(fn, "url: urls[i]") {
		t.Error("per-URL test must send the specific URL being tested")
	}
	if !strings.Contains(ui, `id="proxy-edit-test"`) {
		t.Error("the details modal needs a Test all button")
	}
}
