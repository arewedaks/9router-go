package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/updater"
)

// Replacing the running binary must not be authorised by a session cookie alone:
// a valid cookie is held by any tab, and a restart drops in-flight requests.
func TestUpdateApplyRequiresPassword(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	for _, tc := range []struct {
		name string
		pw   string
	}{
		{"no password header", ""},
		{"wrong password", "not-the-password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/dashboard/update/apply", strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			if tc.pw != "" {
				req.Header.Set("x-9r-password", tc.pw)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: an unconfirmed caller could restart the server (%s)",
					w.Code, w.Body.String())
			}
		})
	}
}

// A correct password must reach the updater. It will report "up to date" on a
// test box, which is enough to prove the request was not rejected by the gate.
func TestUpdateApplyAcceptsCorrectPassword(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	req := httptest.NewRequest("POST", "/api/dashboard/update/apply", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-9r-password", "123456")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Fatal("the correct password was rejected")
	}
}

// The sidebar button polls this on every page load, so it must never 500: a
// failure here would show an error to an operator who merely opened the page.
func TestUpdateStatusIsAlwaysJSON(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/dashboard/update/status", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("status is not JSON: %v (%s)", err, w.Body.String())
	}
	if _, ok := body["hasUpdate"]; !ok {
		t.Errorf("status omits hasUpdate, so the button cannot decide whether to show: %s", w.Body.String())
	}
}

// The sidebar button reads hasUpdate and untrustedSource straight off the
// response. When status returned updater.GetStatus() verbatim those fields were
// nested under cachedInfo, so a refused release rendered as "up to date" and the
// operator never learned an update had been declined.
func TestUpdateStatusIsFlat(t *testing.T) {
	h, repo, _ := setupTestDashboard(t)

	// Seed the cache the way a background check would, including a refusal.
	updater.SetCachedInfoForTest(&updater.UpdateInfo{
		CurrentVersion:  "1.8.17",
		LatestVersion:   "9.9.9",
		HasUpdate:       false,
		UntrustedSource: "update source is not a trusted repository",
	})
	t.Cleanup(func() { updater.SetCachedInfoForTest(nil) })
	_ = repo

	w := httptest.NewRecorder()
	h.HandleUpdateStatus(w, httptest.NewRequest("GET", "/api/dashboard/update/status", nil))

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("status is not JSON: %v", err)
	}
	if _, nested := body["cachedInfo"]; nested {
		t.Error("status still nests the version data under cachedInfo; the button " +
			"reads the top level and would silently show the wrong state")
	}
	if got, _ := body["untrustedSource"].(string); got == "" {
		t.Error("untrustedSource is not at the top level, so a refused update is invisible")
	}
	if got, _ := body["latestVersion"].(string); got != "9.9.9" {
		t.Errorf("latestVersion = %q, want 9.9.9", got)
	}
}

// The browser polls this endpoint every 30 minutes, while the background check
// used to refresh only every 6 hours. Serving the cache unconditionally meant a
// release published just after a background check stayed invisible for hours:
// the sidebar button the operator was waiting for never appeared, with no error
// to explain why. A cache older than the poll interval must be refreshed.
func TestUpdateStatusRefreshesStaleCache(t *testing.T) {
	if !staleBeyondPollInterval(time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)) {
		t.Error("a cache 3 hours old is stale for a 30-minute poll interval, but was treated as fresh")
	}
	if staleBeyondPollInterval(time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)) {
		t.Error("a cache 2 minutes old is fresh; refreshing it would hit the network on every page load")
	}
}

// Not knowing when the cache was written means we cannot claim it is current.
// The safe answer is to check again, not to trust an unlabelled value.
func TestUpdateStatusTreatsMissingTimestampAsStale(t *testing.T) {
	if !staleBeyondPollInterval("") {
		t.Error("an empty lastCheckTime must be treated as stale")
	}
	if !staleBeyondPollInterval("not-a-timestamp") {
		t.Error("an unparseable lastCheckTime must be treated as stale")
	}
}

// The status endpoint must never wait on the network. A blocking check makes a
// slow or unreachable GitHub hang the dashboard, so the poll has to answer from
// the cache and refresh in the background instead. This pins the handler to the
// non-blocking call: swapping in updater.CheckUpdate would make it block.
func TestUpdateStatusDoesNotBlockOnTheNetwork(t *testing.T) {
	src, err := os.ReadFile("update_handler.go")
	if err != nil {
		t.Fatalf("read handler source: %v", err)
	}
	// Scope to HandleUpdateStatus: HandleUpdateCheck is a different endpoint and
	// is SUPPOSED to block, because the operator explicitly asked for a fresh
	// check when opening the update dialog.
	body := string(src)
	start := strings.Index(body, "func (h *Handler) HandleUpdateStatus")
	if start == -1 {
		t.Fatal("HandleUpdateStatus not found in the source")
	}
	fn := body[start:]
	if end := strings.Index(fn, "\nfunc "); end != -1 {
		fn = fn[:end]
	}

	if !strings.Contains(fn, "updater.RefreshInBackground()") {
		t.Error("HandleUpdateStatus does not trigger a background refresh")
	}
	if strings.Contains(fn, "updater.CheckUpdate(") {
		t.Error("HandleUpdateStatus calls CheckUpdate synchronously; a slow GitHub would hang the dashboard")
	}
}
