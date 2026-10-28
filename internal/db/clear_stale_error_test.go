package db

import (
	"encoding/json"
	"testing"
)

// seedStaleOpenrouter reproduces the exact blob the OpenRouter account carried on
// 2026-10-28: a chat 402 from 2026-09-10 plus a live cooldown, so the test also
// proves the cooldown survives.
func seedStaleOpenrouter(t *testing.T, repo *Repo) {
	t.Helper()
	blob := `{"apiKey":"redacted","errorCode":402,
		"testStatus":"unavailable",
		"lastError":"[402]: {\"error\":{\"message\":\"Insufficient credits.\"}}",
		"lastErrorAt":"2026-09-10T14:46:33.774Z",
		"rateLimitedUntil":"2026-10-28T06:00:00Z",
		"backoffLevel":3,
		"modelLock_gpt-4o":"2026-10-28T06:00:00Z"}`
	_, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt)
		 VALUES ('conn-or', 'openrouter', 'api_key', 'awd', 1, ?, datetime('now'), datetime('now'))`,
		blob,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func readConnData(t *testing.T, repo *Repo, id string) map[string]any {
	t.Helper()
	var raw string
	if err := repo.RawDB().QueryRow("SELECT data FROM providerConnections WHERE id = ?", id).Scan(&raw); err != nil {
		t.Fatalf("read data: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// A passing probe must erase the stale failure markers, or every page refresh
// redraws a healthy account as quota_exhausted.
func TestClearConnectionStaleError(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()
	seedStaleOpenrouter(t, repo)

	if err := repo.ClearConnectionStaleError("conn-or"); err != nil {
		t.Fatalf("ClearConnectionStaleError: %v", err)
	}

	m := readConnData(t, repo, "conn-or")
	for _, k := range []string{"errorCode", "lastError", "lastErrorAt"} {
		if v, ok := m[k]; ok && v != nil {
			t.Errorf("%s = %v, want null", k, v)
		}
	}
	if m["testStatus"] != "active" {
		t.Errorf("testStatus = %v, want active", m["testStatus"])
	}
	// The credential is untouched.
	if m["apiKey"] != "redacted" {
		t.Errorf("apiKey = %v, want redacted", m["apiKey"])
	}

	// Routing state must survive: this function clears stale errors, it does not
	// un-park an account.
	if m["rateLimitedUntil"] != "2026-10-28T06:00:00Z" {
		t.Errorf("rateLimitedUntil = %v, want preserved", m["rateLimitedUntil"])
	}
	if m["modelLock_gpt-4o"] != "2026-10-28T06:00:00Z" {
		t.Errorf("modelLock_gpt-4o = %v, want preserved", m["modelLock_gpt-4o"])
	}
	if lvl, ok := m["backoffLevel"].(float64); !ok || lvl != 3 {
		t.Errorf("backoffLevel = %v, want 3", m["backoffLevel"])
	}
}

// Idempotent: the batch endpoint calls this for every row, so a second pass over
// an already-clean blob must not error or change the verdict.
func TestClearConnectionStaleErrorIdempotent(t *testing.T) {
	repo, cleanup := setupHealthConnTestDB(t)
	defer cleanup()
	seedStaleOpenrouter(t, repo)

	for i := 0; i < 2; i++ {
		if err := repo.ClearConnectionStaleError("conn-or"); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	if got := readConnData(t, repo, "conn-or")["testStatus"]; got != "active" {
		t.Errorf("testStatus = %v, want active", got)
	}
}
