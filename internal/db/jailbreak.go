package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// JailbreakDoc is the agents.md-style injection document: one system prompt
// prepended to every request routed through a model whose toggle is on.
// geminiSafety additionally switches the Gemini-native safety filters off via
// the official safetySettings field, from the same switch.
type JailbreakDoc struct {
	Prompt       string `json:"prompt"`
	GeminiSafety bool   `json:"geminiSafety"`
}

const (
	jailbreakScope     = "jailbreak"
	jailbreakDocKey    = "default"
	jailbreakModelPrefix = "model:"
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

// JailbreakModelEnabled reads one model's toggle. The key is the model id as
// it appears in /v1/models and as the client sends it. A missing entry means
// off: injection is opt-in per model, never a side effect of existing.
func (r *Repo) JailbreakModelEnabled(modelID string) (bool, error) {
	var raw string
	err := r.db.QueryRow(
		`SELECT value FROM kv WHERE scope = ? AND key = ? LIMIT 1`,
		jailbreakScope, jailbreakModelPrefix+modelID,
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
		return false, fmt.Errorf("parse jailbreak toggle for %s: %w", modelID, err)
	}
	return s.Enabled, nil
}

// SetJailbreakModel flips one model's toggle. Off deletes the entry rather
// than storing false, so the stored state stays exact instead of accumulating
// rows for every model ever touched.
func (r *Repo) SetJailbreakModel(modelID string, enabled bool) error {
	if !enabled {
		_, err := r.db.Exec(`DELETE FROM kv WHERE scope = ? AND key = ?`,
			jailbreakScope, jailbreakModelPrefix+modelID)
		return err
	}
	raw, _ := json.Marshal(struct {
		Enabled bool `json:"enabled"`
	}{true})
	_, err := r.db.Exec(
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		jailbreakScope, jailbreakModelPrefix+modelID, string(raw),
	)
	return err
}

// ListJailbreakModels returns the models whose toggles are on.
func (r *Repo) ListJailbreakModels() (map[string]bool, error) {
	rows, err := r.db.Query(
		`SELECT key, value FROM kv WHERE scope = ? AND key LIKE ?`,
		jailbreakScope, jailbreakModelPrefix+"%",
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
			out[k[len(jailbreakModelPrefix):]] = true
		}
	}
	return out, rows.Err()
}
