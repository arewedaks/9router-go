package dashboard

import (
	"strings"
	"testing"
)

// TestUIConsoleLogTabExists verifies that the Live Console Log tab is registered
// in the sidebar navigation, is present as a tab-pane, and is handled by the
// pre-paint and bundle routers.
func TestUIConsoleLogTabExists(t *testing.T) {
	ui := readEmbeddedUI(t)

	mustContain := []string{
		`id="nav-console"`,
		`navigateTab('console')`,
		`id="tab-console" class="tab-pane"`,
		`id="console-output" class="console-terminal"`,
		`id="console-status-badge"`,
		`id="console-line-count"`,
		`id="console-search"`,
		`id="console-level-filter"`,
		`id="console-autoscroll"`,
		`copyConsoleLogs()`,
		`clearConsoleLogs()`,
	}
	for _, item := range mustContain {
		if !strings.Contains(ui, item) {
			t.Errorf("expected embedded UI to contain %q, but missing", item)
		}
	}
}

// TestUIConsoleLogLifecycleStreaming ensures that console log streaming
// lifecycle is wired to connect to the SSE endpoint on tab entry, tear down on
// exit, strip ANSI codes, and classify log levels.
func TestUIConsoleLogLifecycleStreaming(t *testing.T) {
	ui := readEmbeddedUI(t)

	mustContain := []string{
		// EventSource cannot send an Authorization header, so the API-key login
		// path must pass the key as a query parameter via consoleLogURL().
		`new EventSource(consoleLogURL("/api/translator/console-logs/stream"))`,
		`function consoleLogURL(`,
		`"/api/translator/console-logs"`,
		`startConsoleLogs()`,
		`stopConsoleLogs()`,
		`stripAnsi(`,
		`detectLogLevel(`,
		`renderConsoleLine(`,
		`tabId === "console"`,
		// The buffer fetch and the remote clear must go through api() so they send
		// the credential (header or cookie); a raw fetch would 401.
		`api("/api/translator/console-logs")`,
		`api("/api/translator/console-logs", { method: "DELETE" })`,
	}
	for _, item := range mustContain {
		if !strings.Contains(ui, item) {
			t.Errorf("expected embedded UI to contain %q, but missing", item)
		}
	}
}
