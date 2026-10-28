package executor

import (
	"net/http"
	"testing"
)

// The gateway's update gate reads x-freebuff-client-version and answers 410
// "This version of Freebuff is out of date" when it is missing or below the
// running release. Every freebuff call must carry it.
func TestFreebuffClientHeadersCarryVersion(t *testing.T) {
	headers := freebuffClientHeaders()
	if headers["x-freebuff-client"] != "cli" {
		t.Errorf("x-freebuff-client = %q, want cli", headers["x-freebuff-client"])
	}
	if v := headers["x-freebuff-client-version"]; v == "" {
		t.Error("x-freebuff-client-version is empty; the gateway would answer 410")
	}
	if headers["x-freebuff-sse-gzip-recovery"] != "1" {
		t.Errorf("x-freebuff-sse-gzip-recovery = %q, want 1", headers["x-freebuff-sse-gzip-recovery"])
	}

	// The override wins, so a version bump needs no rebuild.
	t.Setenv("FREEBUFF_CLI_VERSION", "9.9.9-test")
	if got := freebuffClientHeaders()["x-freebuff-client-version"]; got != "9.9.9-test" {
		t.Errorf("override ignored, got %q", got)
	}
}

// The default must stay ahead of the gate. Pinned so a silent regression to an
// empty string — which is what caused the 410 in the first place — fails here.
func TestFreebuffCLIVersionPinned(t *testing.T) {
	t.Setenv("FREEBUFF_CLI_VERSION", "")
	if FreebuffCLIVersion == "" {
		t.Fatal("FreebuffCLIVersion is empty")
	}
	if freebuffClientVersion() != FreebuffCLIVersion {
		t.Errorf("freebuffClientVersion() = %q, want %q", freebuffClientVersion(), FreebuffCLIVersion)
	}
}

func TestFreebuffSetClientHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.test/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	freebuffSetClientHeaders(req)
	if req.Header.Get("x-freebuff-client-version") == "" {
		t.Fatal("version header not set on the request")
	}
	if req.Header.Get("x-freebuff-client") != "cli" {
		t.Errorf("client header = %q", req.Header.Get("x-freebuff-client"))
	}
}
