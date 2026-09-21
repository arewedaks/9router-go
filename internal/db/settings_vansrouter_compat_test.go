package db

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// A VansRouter settings blob carries keys this build does not model. Because
// saveSettings marshals the whole SettingsData struct back over row id = 1,
// any unmodelled key is destroyed the first time 9router-go saves anything.
// That breaks the shared-database migration path: open the dashboard once, and
// the operator's VansRouter-only settings are gone.
func TestSaveSettingsPreservesUnknownVansRouterKeys(t *testing.T) {
	r := newSettingsRepo(t)

	// A blob shaped like VansRouter's, including keys this struct has no field
	// for. `requireApiKey` is one of the real ones.
	seed := `{
		"rtkEnabled": true,
		"requireApiKey": false,
		"tunnelEnabled": true,
		"someFutureFlag": {"nested": [1, 2, 3]},
		"providerStrategies": {"openai": {"rotateStrategy": "round-robin", "strictModelAssignment": true}}
	}`
	if _, err := r.db.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		seed); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	// Any ordinary 9router-go write triggers a full-blob rewrite.
	if err := r.SetAutoUpdate(true); err != nil {
		t.Fatalf("SetAutoUpdate: %v", err)
	}

	var raw string
	if err := r.db.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode written blob: %v", err)
	}

	// The write must have applied.
	if v, ok := got["autoUpdate"].(bool); !ok || !v {
		t.Errorf("autoUpdate was not persisted: %v", got["autoUpdate"])
	}

	for _, key := range []string{"requireApiKey", "tunnelEnabled", "someFutureFlag"} {
		if _, ok := got[key]; !ok {
			t.Errorf("unmodelled key %q was destroyed by a settings write", key)
		}
	}

	// The unmodelled VALUE must survive intact, not just the key.
	if v, ok := got["requireApiKey"].(bool); !ok || v {
		t.Errorf("requireApiKey value changed: got %v, want false", got["requireApiKey"])
	}
	if m, ok := got["someFutureFlag"].(map[string]any); !ok || m["nested"] == nil {
		t.Errorf("nested unknown value was mangled: %v", got["someFutureFlag"])
	}

	// The per-strategy passthrough already worked; keep it that way.
	ps, _ := got["providerStrategies"].(map[string]any)
	openai, _ := ps["openai"].(map[string]any)
	if _, ok := openai["strictModelAssignment"]; !ok {
		t.Error("strictModelAssignment was dropped from the strategy object")
	}
}

// requireApiKey itself must round-trip once modelled: VansRouter writes it, and
// reading it here has to give the same answer so a toggle flipped in one build
// shows correctly in the other.
func TestRequireAPIKeyRoundTrips(t *testing.T) {
	r := newSettingsRepo(t)

	if _, err := r.db.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		`{"requireApiKey": false}`); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	s, err := r.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.RequireAPIKey == nil {
		t.Fatal("requireApiKey=false was not read; it must not stay nil")
	}
	if *s.RequireAPIKey {
		t.Error("requireApiKey=false read back as true")
	}

	// Flipping it back on and re-reading must round-trip as well.
	on := true
	if err := r.SetRequireAPIKey(&on); err != nil {
		t.Fatalf("SetRequireAPIKey: %v", err)
	}
	s, err = r.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings after write: %v", err)
	}
	if s.RequireAPIKey == nil || !*s.RequireAPIKey {
		t.Errorf("requireApiKey did not round-trip as true: %v", s.RequireAPIKey)
	}

	// Absent means unset, not false: the default (require a key) must apply.
	if _, err := r.db.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		`{"rtkEnabled": true}`); err != nil {
		t.Fatalf("re-seed settings: %v", err)
	}
	s, err = r.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings after re-seed: %v", err)
	}
	if s.RequireAPIKey != nil {
		t.Errorf("an absent requireApiKey must stay nil so the default applies, got %v", *s.RequireAPIKey)
	}
}

// allowRemoteNoApiKey is the second half of the exposure switch: it only widens
// access past loopback while requireApiKey is off. It has to round-trip under
// the same key VansRouter writes, or a migrated database loses the operator's
// choice and silently falls back to loopback-only.
func TestAllowRemoteNoApiKeyRoundTrips(t *testing.T) {
	r := newSettingsRepo(t)

	on := true
	if err := r.SetAllowRemoteNoApiKey(&on); err != nil {
		t.Fatalf("SetAllowRemoteNoApiKey: %v", err)
	}
	s, err := r.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.AllowRemoteNoApiKey == nil || !*s.AllowRemoteNoApiKey {
		t.Errorf("allowRemoteNoApiKey did not round-trip as true: %v", s.AllowRemoteNoApiKey)
	}

	// The stored blob must use VansRouter's exact key, since that is what the
	// other dashboard reads.
	var raw string
	if err := r.db.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatalf("read stored blob: %v", err)
	}
	if !strings.Contains(raw, `"allowRemoteNoApiKey":true`) {
		t.Errorf("stored blob is missing VansRouter's key: %s", raw)
	}

	// Absent must stay absent so the default (loopback-only) applies.
	off := false
	if err := r.SetRequireAPIKey(&off); err != nil {
		t.Fatalf("SetRequireAPIKey: %v", err)
	}
	if _, err := r.db.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		`{"rtkEnabled": true}`); err != nil {
		t.Fatalf("re-seed settings: %v", err)
	}
	s, err = r.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings after re-seed: %v", err)
	}
	if s.AllowRemoteNoApiKey != nil {
		t.Errorf("an absent allowRemoteNoApiKey must stay nil, got %v", *s.AllowRemoteNoApiKey)
	}
}
