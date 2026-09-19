package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/updater"
)

// The running version is shown in two places the operator looks at: next to the
// product name in the sidebar and in the browser tab title. Both are filled in
// from the server so that after an update the page reports the new build without
// the page itself being rebuilt. These tests pin that wiring.

func TestAuthStatusReportsTheRunningVersion(t *testing.T) {
	_, _, r := setupTestDashboard(t)
	req := httptest.NewRequest("GET", "/api/dashboard/auth/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("auth/status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("auth/status is not JSON: %v", err)
	}

	// This route is reachable before sign-in, which is the point: the version is
	// then visible on the login screen too.
	got, ok := body["currentVersion"].(string)
	if !ok || got == "" {
		t.Fatalf("auth/status has no currentVersion field, got %#v", body)
	}
	if got != updater.CurrentVersion {
		t.Errorf("auth/status currentVersion = %q, want %q", got, updater.CurrentVersion)
	}
}

// Both endpoints feed the same renderer, so a field named differently in one of
// them yields a blank version instead of an error. That is exactly the bug this
// pins: auth/status once reported "version" while the renderer read
// "currentVersion".
func TestVersionFieldNameMatchesTheUpdateStatusEndpoint(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	get := func(path string) map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, w.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s is not JSON: %v", path, err)
		}
		return body
	}

	auth := get("/api/dashboard/auth/status")
	upd := get("/api/dashboard/update/status")

	if _, ok := upd["currentVersion"]; !ok {
		t.Fatalf("update/status no longer reports currentVersion; the UI reads it. keys=%v", keysOf(upd))
	}
	if _, ok := auth["currentVersion"]; !ok {
		t.Errorf("auth/status reports a different field name than update/status. keys=%v", keysOf(auth))
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestUIShowsTheVersionInTheSidebar(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `id="brand-version"`) {
		t.Error(`the sidebar brand has no element with id="brand-version"`)
	}
}

func TestUITitlesTheTabWithTheVersion(t *testing.T) {
	ui := readEmbeddedUI(t)

	// Without this the tab keeps whatever the markup shipped with, so the
	// version would appear only after the first update check resolved.
	if !strings.Contains(ui, "document.title = ") {
		t.Error("the UI never sets document.title, so the tab title shows no version")
	}
	// The value must come from the server. A literal would go stale the moment
	// the binary is updated.
	if strings.Contains(ui, `document.title = "9Router v1.8`) {
		t.Error("the tab title hardcodes a version instead of using the reported one")
	}
}

func TestUIFillsTheVersionBeforeSignIn(t *testing.T) {
	ui := readEmbeddedUI(t)

	// checkAuth runs on load and is the only path taken when a password is
	// required, so a version render that lived only in the update poller would
	// leave the login screen blank.
	authIdx := strings.Index(ui, "async function checkAuth()")
	if authIdx < 0 {
		t.Fatal("checkAuth is missing from the UI")
	}
	rest := ui[authIdx:]
	end := strings.Index(rest[1:], "\n  async function ")
	if end < 0 {
		end = len(rest) - 1
	}
	if !strings.Contains(rest[:end+1], "renderVersion(") {
		t.Error("checkAuth does not render the version, so it is blank until sign-in")
	}
}

func TestUIVersionRendersOnlyFromServerData(t *testing.T) {
	ui := readEmbeddedUI(t)

	body := extractFunction(t, ui, "renderVersion")
	if !strings.Contains(body, "currentVersion") {
		t.Error("renderVersion does not read currentVersion from the server payload")
	}
	// A missing version must leave the label hidden rather than showing
	// "vundefined".
	if !strings.Contains(body, "return") {
		t.Error("renderVersion has no early return for a missing version")
	}
}

// extractFunction returns the source of a top-level `function name(...) { ... }`
// by brace matching, so an assertion can be limited to that function instead of
// the whole file.
func extractFunction(t *testing.T, src, name string) string {
	t.Helper()
	marker := "function " + name + "("
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatalf("function %s not found in the UI", name)
	}
	open := strings.Index(src[start:], "{")
	if open < 0 {
		t.Fatalf("function %s has no body", name)
	}
	open += start
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("function %s is not brace balanced", name)
	return ""
}
