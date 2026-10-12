package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// JailbreakDoc is the agents.md-style injection document: one system prompt
// prepended to every request routed through a provider whose toggle is on.
// geminiSafety additionally switches the Gemini-native safety filters off via
// the official safetySettings field, from the same switch.
type JailbreakDoc struct {
	Prompt       string `json:"prompt"`
	GeminiSafety bool   `json:"geminiSafety"`
}

const (
	jailbreakScope      = "jailbreak"
	jailbreakDocKey     = "default"
	jailbreakProvPrefix = "provider:"
)

// GetJailbreakDoc returns the saved document, or zero values when none was
// saved yet — no doc means no injection anywhere.
func (r *Repo) GetJailbreakDoc() (*JailbreakDoc, error) {
	var raw string
	err := r.db.QueryRow(
		`SELECT value FROM kv WHERE scope = ? AND key = ? LIMIT 1`,
		jailbreakScope, jailbreakDocKey,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return &JailbreakDoc{}, nil
	}
	if err != nil {
		return nil, err
	}
	var d JailbreakDoc
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, fmt.Errorf("parse jailbreak doc: %w", err)
	}
	return &d, nil
}

func (r *Repo) SaveJailbreakDoc(d *JailbreakDoc) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		jailbreakScope, jailbreakDocKey, string(raw),
	)
	return err
}

// JailbreakProviderEnabled reads one provider's toggle. A missing entry means
// off: injection is opt-in per provider, never a side effect of existing.
func (r *Repo) JailbreakProviderEnabled(providerID string) (bool, error) {
	var raw string
	err := r.db.QueryRow(
		`SELECT value FROM kv WHERE scope = ? AND key = ? LIMIT 1`,
		jailbreakScope, jailbreakProvPrefix+providerID,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var s struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return false, fmt.Errorf("parse jailbreak toggle for %s: %w", providerID, err)
	}
	return s.Enabled, nil
}

// SetJailbreakProvider flips one provider's toggle. Off deletes the entry
// rather than storing false, so the stored state stays exact instead of
// accumulating rows for every provider ever touched.
func (r *Repo) SetJailbreakProvider(providerID string, enabled bool) error {
	if !enabled {
		_, err := r.db.Exec(`DELETE FROM kv WHERE scope = ? AND key = ?`,
			jailbreakScope, jailbreakProvPrefix+providerID)
		return err
	}
	raw, _ := json.Marshal(struct {
		Enabled bool `json:"enabled"`
	}{true})
	_, err := r.db.Exec(
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		jailbreakScope, jailbreakProvPrefix+providerID, string(raw),
	)
	return err
}

// ListJailbreakProviders returns the providers whose toggles are on.
func (r *Repo) ListJailbreakProviders() (map[string]bool, error) {
	rows, err := r.db.Query(
		`SELECT key, value FROM kv WHERE scope = ? AND key LIKE ?`,
		jailbreakScope, jailbreakProvPrefix+"%",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k, raw string
		if err := rows.Scan(&k, &raw); err != nil {
			return nil, err
		}
		var s struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			continue // a malformed row means off, not a broken list
		}
		if s.Enabled {
			out[k[len(jailbreakProvPrefix):]] = true
		}
	}
	return out, rows.Err()
}
