package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/tokensaver"
)

// getSettings reads the settings payload the Token Saver page renders from.
func getSettings(t *testing.T, r http.Handler) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/dashboard/settings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET settings = %d: %s", w.Code, w.Body.String())
	}
	return decodeMap(t, w)
}

// The Token Saver page renders its dropdowns from the level vocabulary the
// server reports, so the two cannot drift apart when a level is added.
func TestGetSettingsReportsLevelVocabulary(t *testing.T) {
	_, _, r := setupTestDashboard(t)
	m := getSettings(t, r)

	levels, ok := m["cavemanLevels"].([]any)
	if !ok || len(levels) == 0 {
		t.Fatalf("cavemanLevels missing or empty: %v", m["cavemanLevels"])
	}
	for _, want := range tokensaver.CavemanLevels {
		found := false
		for _, got := range levels {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("cavemanLevels is missing %q: %v", want, levels)
		}
	}
	pony, ok := m["ponytailLevels"].([]any)
	if !ok || len(pony) == 0 {
		t.Fatalf("ponytailLevels missing or empty: %v", m["ponytailLevels"])
	}
}

// Every level the dropdown offers must be accepted by the API, or the operator
// picks a value the server rejects.
func TestUpdateSettingsAcceptsEveryLevel(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	for _, lvl := range tokensaver.CavemanLevels {
		w := postJSON(t, r, "/api/dashboard/settings",
			`{"cavemanEnabled":true,"cavemanLevel":"`+lvl+`"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("cavemanLevel %q rejected: %d %s", lvl, w.Code, w.Body.String())
		}
		if got := h.tokenSaver.CavemanLevel(); got != lvl {
			t.Errorf("cavemanLevel = %q, want %q", got, lvl)
		}
	}
	for _, lvl := range tokensaver.PonytailLevels {
		w := postJSON(t, r, "/api/dashboard/settings",
			`{"ponytailEnabled":true,"ponytailLevel":"`+lvl+`"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("ponytailLevel %q rejected: %d %s", lvl, w.Code, w.Body.String())
		}
		if got := h.tokenSaver.PonytailLevel(); got != lvl {
			t.Errorf("ponytailLevel = %q, want %q", got, lvl)
		}
	}
}

// An unknown level is a client bug, not a value to silently coerce: accepting it
// would leave the operator believing a style is active that never runs.
func TestUpdateSettingsRejectsUnknownLevel(t *testing.T) {
	h, _, r := setupTestDashboard(t)
	h.tokenSaver.SetCaveman(true, "full")

	w := postJSON(t, r, "/api/dashboard/settings", `{"cavemanEnabled":true,"cavemanLevel":"wenyan"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown level: %s", w.Code, w.Body.String())
	}
	if got := h.tokenSaver.CavemanLevel(); got != "full" {
		t.Errorf("a rejected write changed the level to %q", got)
	}
}

// The legacy spellings the older dashboard wrote are read-path aliases: a
// database carrying "medium" must still select a real prompt rather than
// falling through to a default the operator never chose.
//
// The startup path that consumes this is main.go's
// `ts.SetCaveman(settings.CavemanEnabled, settings.CavemanLevel)`, so the
// normalization is asserted where it happens (the shared config) and the prompt
// lookup is asserted here.
func TestLegacyStoredLevelStillSelectsAPrompt(t *testing.T) {
	_, repo, _ := setupTestDashboard(t)

	// Seed the row the way the old dashboard wrote it: "medium" is not a level
	// this build offers.
	if _, err := repo.RawDB().Exec(
		`INSERT INTO settings (id, data) VALUES (1, '{"rtkEnabled":true,"cavemanEnabled":true,"cavemanLevel":"medium"}')
		 ON CONFLICT(id) DO UPDATE SET data = excluded.data`); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	s, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.CavemanLevel != "medium" {
		t.Fatalf("precondition failed: stored level = %q, want the legacy value", s.CavemanLevel)
	}
	if got := tokensaver.GetCavemanPrompt(s.CavemanLevel); got != tokensaver.CavemanFull {
		t.Errorf("legacy level %q did not resolve to the full prompt", s.CavemanLevel)
	}
}

// Headroom is disabled by default: it needs a sidecar the operator has to
// install, so enabling it implicitly would add a failed HTTP call per request.
func TestHeadroomDefaultsToDisabled(t *testing.T) {
	_, _, r := setupTestDashboard(t)
	m := getSettings(t, r)

	if m["headroomEnabled"] != false {
		t.Errorf("headroomEnabled = %v, want false by default", m["headroomEnabled"])
	}
	if m["headroomUrl"] == "" {
		t.Error("headroomUrl must carry the default so the field is not blank")
	}
	if m["headroomCompressUserMessages"] != false {
		t.Error("user-message compression must default to off")
	}
}

// A save must reach both the runtime config and the database, or the next
// restart would silently revert the operator's choice.
func TestUpdateSettingsPersistsHeadroom(t *testing.T) {
	h, repo, r := setupTestDashboard(t)

	w := postJSON(t, r, "/api/dashboard/settings",
		`{"headroomEnabled":true,"headroomUrl":"http://example.test:9000","headroomTimeoutMs":4500,"headroomCompressUserMessages":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	if !h.tokenSaver.HeadroomEnabled() {
		t.Error("runtime config did not enable Headroom")
	}
	if got := h.tokenSaver.HeadroomURL(); got != "http://example.test:9000" {
		t.Errorf("runtime URL = %q", got)
	}
	if got := h.tokenSaver.HeadroomTimeoutMs(); got != 4500 {
		t.Errorf("runtime timeout = %d, want 4500", got)
	}
	if !h.tokenSaver.HeadroomCompressUserMessages() {
		t.Error("runtime config did not enable user-message compression")
	}

	s, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !s.HeadroomEnabled || s.HeadroomUrl != "http://example.test:9000" || s.HeadroomTimeoutMs != 4500 {
		t.Errorf("stored settings = %+v", s)
	}
	if !s.HeadroomCompressUM {
		t.Error("user-message compression was not persisted")
	}
}

// A partial patch must not blank the fields it does not mention — the toggle in
// the UI sends only the switch.
func TestUpdateSettingsHeadroomPatchIsPartial(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	postJSON(t, r, "/api/dashboard/settings",
		`{"headroomEnabled":true,"headroomUrl":"http://example.test:9000","headroomTimeoutMs":4500}`)
	postJSON(t, r, "/api/dashboard/settings", `{"headroomEnabled":false}`)

	if h.tokenSaver.HeadroomEnabled() {
		t.Error("the switch did not turn Headroom off")
	}
	if got := h.tokenSaver.HeadroomURL(); got != "http://example.test:9000" {
		t.Errorf("URL was reset by a patch that did not mention it: %q", got)
	}
	if got := h.tokenSaver.HeadroomTimeoutMs(); got != 4500 {
		t.Errorf("timeout was reset by a patch that did not mention it: %d", got)
	}
}

// The level and the switch are independent fields. Picking an intensity sends
// only the level, so a handler that required the enable flag alongside it would
// silently drop the choice.
func TestUpdateSettingsLevelWithoutEnableFlag(t *testing.T) {
	h, _, r := setupTestDashboard(t)
	h.tokenSaver.SetCaveman(true, "full")
	h.tokenSaver.SetPonytail(true, "full")

	w := postJSON(t, r, "/api/dashboard/settings", `{"cavemanLevel":"ultra"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := h.tokenSaver.CavemanLevel(); got != "ultra" {
		t.Errorf("CavemanLevel = %q, want ultra", got)
	}
	if !h.tokenSaver.CavemanEnabled() {
		t.Error("a level-only patch must not turn the style off")
	}

	w = postJSON(t, r, "/api/dashboard/settings", `{"ponytailLevel":"lite"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := h.tokenSaver.PonytailLevel(); got != "lite" {
		t.Errorf("PonytailLevel = %q, want lite", got)
	}
	if !h.tokenSaver.PonytailEnabled() {
		t.Error("a level-only patch must not turn the style off")
	}
}
