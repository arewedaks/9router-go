package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// JailbreakDoc is the agents.md-style injection document: one system prompt
// prepended to every request routed through a model whose toggle is on.
// geminiSafety additionally switches the Gemini-native safety filters off via
// the official safetySettings field, from the same switch.
type JailbreakDoc struct {
	ID           string `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	Prompt       string `json:"prompt"`
	GeminiSafety bool   `json:"geminiSafety"`
	// Source is the .md file the prompt was uploaded from, kept for display so
	// the operator can tell an uploaded agents.md from something typed inline.
	Source string `json:"source,omitempty"`
}

// JailbreakModelConfig holds the binding between a model and a preset.
type JailbreakModelConfig struct {
	Enabled  bool   `json:"enabled"`
	PresetID string `json:"presetId,omitempty"`
}

const (
	jailbreakScope       = "jailbreak"
	jailbreakDocKey      = "default"
	jailbreakDocPrefix   = "doc:"
	jailbreakModelPrefix = "model:"
)

// GetJailbreakDoc returns the default saved document, or zero values when none was
// saved yet — no doc means no injection anywhere.
func (r *Repo) GetJailbreakDoc() (*JailbreakDoc, error) {
	return r.GetJailbreakPreset("default")
}

func (r *Repo) SaveJailbreakDoc(d *JailbreakDoc) error {
	if d.ID == "" {
		d.ID = "default"
	}
	return r.SaveJailbreakPreset(d)
}

// GetJailbreakPreset loads a preset by ID. "default" checks both doc:default and default.
func (r *Repo) GetJailbreakPreset(id string) (*JailbreakDoc, error) {
	if id == "" || id == "default" {
		doc, err := r.getJailbreakDocByKey(jailbreakDocKey)
		if err == nil && doc.Prompt != "" {
			doc.ID = "default"
			if doc.Name == "" {
				doc.Name = "Default"
			}
			return doc, nil
		}
		doc, err = r.getJailbreakDocByKey(jailbreakDocPrefix + "default")
		if err == nil && doc.Prompt != "" {
			doc.ID = "default"
			if doc.Name == "" {
				doc.Name = "Default"
			}
			return doc, nil
		}
		return &JailbreakDoc{ID: "default", Name: "Default"}, nil
	}

	doc, err := r.getJailbreakDocByKey(jailbreakDocPrefix + id)
	if err != nil {
		return nil, err
	}
	doc.ID = id
	return doc, nil
}

func (r *Repo) getJailbreakDocByKey(key string) (*JailbreakDoc, error) {
	var raw string
	err := r.db.QueryRow(
		`SELECT value FROM kv WHERE scope = ? AND key = ? LIMIT 1`,
		jailbreakScope, key,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return &JailbreakDoc{}, nil
	}
	if err != nil {
		return nil, err
	}
	var d JailbreakDoc
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, fmt.Errorf("parse jailbreak doc %s: %w", key, err)
	}
	return &d, nil
}

// SaveJailbreakPreset saves or updates a preset.
func (r *Repo) SaveJailbreakPreset(d *JailbreakDoc) error {
	if d == nil {
		return fmt.Errorf("nil jailbreak doc")
	}
	id := d.ID
	if id == "" {
		id = "default"
		d.ID = id
	}
	if d.Name == "" {
		if d.Source != "" {
			d.Name = d.Source
		} else {
			d.Name = id
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	key := jailbreakDocPrefix + id
	if id == "default" {
		key = jailbreakDocKey
	}
	_, err = r.db.Exec(
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		jailbreakScope, key, string(raw),
	)
	return err
}

// DeleteJailbreakPreset removes a preset by ID and unbinds every model armed to it.
func (r *Repo) DeleteJailbreakPreset(id string) error {
	if id == "" {
		return nil
	}
	keys := []string{jailbreakDocPrefix + id}
	if id == "default" {
		keys = append(keys, jailbreakDocKey)
	}
	for _, k := range keys {
		if _, err := r.db.Exec(`DELETE FROM kv WHERE scope = ? AND key = ?`, jailbreakScope, k); err != nil {
			return err
		}
	}

	// Automatically unbind every model armed to this preset.
	rows, err := r.db.Query(`SELECT key, value FROM kv WHERE scope = ? AND key LIKE ?`,
		jailbreakScope, jailbreakModelPrefix+"%")
	if err != nil {
		return err
	}
	defer rows.Close()

	var toDelete []string
	for rows.Next() {
		var k, raw string
		if err := rows.Scan(&k, &raw); err != nil {
			continue
		}
		var cfg JailbreakModelConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			continue
		}
		targetPreset := cfg.PresetID
		if targetPreset == "" {
			targetPreset = "default"
		}
		if targetPreset == id {
			toDelete = append(toDelete, k)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range toDelete {
		if _, err := r.db.Exec(`DELETE FROM kv WHERE scope = ? AND key = ?`, jailbreakScope, k); err != nil {
			return err
		}
	}

	return nil
}

// ListJailbreakPresets returns all available presets.
func (r *Repo) ListJailbreakPresets() ([]*JailbreakDoc, error) {
	rows, err := r.db.Query(
		`SELECT key, value FROM kv WHERE scope = ? AND (key LIKE ? OR key = ?) ORDER BY key ASC`,
		jailbreakScope, jailbreakDocPrefix+"%", jailbreakDocKey,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var presets []*JailbreakDoc
	seenDefault := false

	for rows.Next() {
		var k, raw string
		if err := rows.Scan(&k, &raw); err != nil {
			return nil, err
		}
		var d JailbreakDoc
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			continue
		}
		if k == jailbreakDocKey || k == jailbreakDocPrefix+"default" {
			if seenDefault {
				continue
			}
			seenDefault = true
			d.ID = "default"
			if d.Name == "" {
				d.Name = "Default"
			}
		} else {
			d.ID = k[len(jailbreakDocPrefix):]
			if d.Name == "" {
				if d.Source != "" {
					d.Name = d.Source
				} else {
					d.Name = d.ID
				}
			}
		}
		presets = append(presets, &d)
	}
	return presets, rows.Err()
}

// JailbreakModelEnabled reads one model's toggle. The key is the model id as
// it appears in /v1/models and as the client sends it. A missing entry means
// off: injection is opt-in per model, never a side effect of existing.
func (r *Repo) JailbreakModelEnabled(modelID string) (bool, error) {
	cfg, err := r.GetJailbreakModelConfig(modelID)
	if err != nil {
		return false, err
	}
	return cfg.Enabled, nil
}

// GetJailbreakModelConfig reads one model's configuration.
func (r *Repo) GetJailbreakModelConfig(modelID string) (JailbreakModelConfig, error) {
	var raw string
	err := r.db.QueryRow(
		`SELECT value FROM kv WHERE scope = ? AND key = ? LIMIT 1`,
		jailbreakScope, jailbreakModelPrefix+modelID,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return JailbreakModelConfig{Enabled: false}, nil
	}
	if err != nil {
		return JailbreakModelConfig{}, err
	}
	var cfg JailbreakModelConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return JailbreakModelConfig{}, fmt.Errorf("parse jailbreak config for %s: %w", modelID, err)
	}
	if cfg.Enabled && cfg.PresetID == "" {
		cfg.PresetID = "default"
	}
	return cfg, nil
}

// SetJailbreakModel flips one model's toggle, defaulting preset to "default".
func (r *Repo) SetJailbreakModel(modelID string, enabled bool) error {
	return r.SetJailbreakModelPreset(modelID, "default", enabled)
}

// SetJailbreakModelPreset binds a preset to a model.
func (r *Repo) SetJailbreakModelPreset(modelID string, presetID string, enabled bool) error {
	if !enabled || presetID == "" || presetID == "off" {
		_, err := r.db.Exec(`DELETE FROM kv WHERE scope = ? AND key = ?`,
			jailbreakScope, jailbreakModelPrefix+modelID)
		return err
	}
	cfg := JailbreakModelConfig{
		Enabled:  true,
		PresetID: presetID,
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		jailbreakScope, jailbreakModelPrefix+modelID, string(raw),
	)
	return err
}

// ListJailbreakModels returns the models whose toggles are on.
func (r *Repo) ListJailbreakModels() (map[string]bool, error) {
	cfgs, err := r.ListJailbreakModelConfigs()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(cfgs))
	for k, c := range cfgs {
		if c.Enabled {
			out[k] = true
		}
	}
	return out, nil
}

// ListJailbreakModelConfigs returns all model configurations. Any models bound to
// a preset that no longer exists are cleaned up and unbound to off.
func (r *Repo) ListJailbreakModelConfigs() (map[string]JailbreakModelConfig, error) {
	validPresets := map[string]bool{}
	pRows, pErr := r.db.Query(
		`SELECT key FROM kv WHERE scope = ? AND (key LIKE ? OR key = ?)`,
		jailbreakScope, jailbreakDocPrefix+"%", jailbreakDocKey,
	)
	if pErr == nil {
		for pRows.Next() {
			var pk string
			if pRows.Scan(&pk) == nil {
				if pk == jailbreakDocKey || pk == jailbreakDocPrefix+"default" {
					validPresets["default"] = true
				} else if strings.HasPrefix(pk, jailbreakDocPrefix) {
					validPresets[pk[len(jailbreakDocPrefix):]] = true
				}
			}
		}
		pRows.Close()
	}

	rows, err := r.db.Query(
		`SELECT key, value FROM kv WHERE scope = ? AND key LIKE ?`,
		jailbreakScope, jailbreakModelPrefix+"%",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]JailbreakModelConfig{}
	var orphaned []string
	for rows.Next() {
		var k, raw string
		if err := rows.Scan(&k, &raw); err != nil {
			continue
		}
		var cfg JailbreakModelConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			continue // malformed row means off
		}
		if cfg.Enabled {
			if cfg.PresetID == "" {
				cfg.PresetID = "default"
			}
			if len(validPresets) > 0 && !validPresets[cfg.PresetID] {
				orphaned = append(orphaned, k)
				continue
			}
			out[k[len(jailbreakModelPrefix):]] = cfg
		}
	}
	for _, k := range orphaned {
		_, _ = r.db.Exec(`DELETE FROM kv WHERE scope = ? AND key = ?`, jailbreakScope, k)
	}
	return out, rows.Err()
}

// GetJailbreakDocForModel returns the resolved preset document for a model.
// Returns nil if model is disabled or no document is configured.
func (r *Repo) GetJailbreakDocForModel(modelID string) (*JailbreakDoc, error) {
	cfg, err := r.GetJailbreakModelConfig(modelID)
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	presetID := cfg.PresetID
	if presetID == "" {
		presetID = "default"
	}
	doc, err := r.GetJailbreakPreset(presetID)
	if err != nil {
		return nil, err
	}
	if doc == nil || doc.Prompt == "" {
		return nil, nil
	}
	return doc, nil
}
