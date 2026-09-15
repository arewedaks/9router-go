package db

import (
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"9router/proxy/internal/models"
)

// UpdateProviderConnection updates a provider connection's name, priority, isActive, data, and updatedAt.
func (r *Repo) UpdateProviderConnection(id string, name string, priority int, isActive bool, data string) error {
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var nameVal any
	if name != "" {
		nameVal = name
	}
	_, err := r.db.Exec(
		`UPDATE providerConnections SET name = ?, priority = ?, isActive = ?, data = ?, updatedAt = ? WHERE id = ?`,
		nameVal, priority, activeInt, data, now, id,
	)
	if err != nil {
		return fmt.Errorf("update provider connection %s: %w", id, err)
	}
	return nil
}

// DeleteProviderConnection deletes a provider connection by its ID.
func (r *Repo) DeleteProviderConnection(id string) error {
	_, err := r.db.Exec(`DELETE FROM providerConnections WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete provider connection %s: %w", id, err)
	}
	return nil
}

// SetConnectionStatus updates the isActive status and updatedAt of a provider connection.
func (r *Repo) SetConnectionStatus(id string, isActive bool) error {
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`UPDATE providerConnections SET isActive = ?, updatedAt = ? WHERE id = ?`,
		activeInt, now, id,
	)
	if err != nil {
		return fmt.Errorf("set connection status %s: %w", id, err)
	}
	return nil
}

// SetConnectionPriority updates the priority and updatedAt of a provider connection.
func (r *Repo) SetConnectionPriority(id string, priority int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`UPDATE providerConnections SET priority = ?, updatedAt = ? WHERE id = ?`,
		priority, now, id,
	)
	if err != nil {
		return fmt.Errorf("set connection priority %s: %w", id, err)
	}
	return nil
}

// CreateCombo inserts a new combo routing configuration.
func (r *Repo) CreateCombo(id, name, kind, modelsJSON, strategy string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var kindVal any
	if kind != "" {
		kindVal = kind
	}
	_, err := r.db.Exec(
		`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, kindVal, modelsJSON, now, now,
	)
	if err != nil {
		return fmt.Errorf("create combo %s: %w", id, err)
	}
	if strategy != "" {
		// Update strategy if column exists in the schema
		_, _ = r.db.Exec(`UPDATE combos SET strategy = ? WHERE id = ?`, strategy, id)
	}
	return nil
}

// UpdateCombo updates an existing combo routing configuration.
func (r *Repo) UpdateCombo(id, name, kind, modelsJSON, strategy string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var kindVal any
	if kind != "" {
		kindVal = kind
	}
	_, err := r.db.Exec(
		`UPDATE combos SET name = ?, kind = ?, models = ?, updatedAt = ? WHERE id = ?`,
		name, kindVal, modelsJSON, now, id,
	)
	if err != nil {
		return fmt.Errorf("update combo %s: %w", id, err)
	}
	if strategy != "" {
		// Update strategy if column exists in the schema
		_, _ = r.db.Exec(`UPDATE combos SET strategy = ? WHERE id = ?`, strategy, id)
	}
	return nil
}

// DeleteCombo deletes a combo configuration by its ID.
func (r *Repo) DeleteCombo(id string) error {
	_, err := r.db.Exec(`DELETE FROM combos WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete combo %s: %w", id, err)
	}
	return nil
}

// GetApiKeys retrieves all client API keys ordered by createdAt DESC.
func (r *Repo) GetApiKeys() ([]*models.APIKey, error) {
	rows, err := r.db.Query(`SELECT id, key, name, machineId, isActive, createdAt FROM apiKeys ORDER BY createdAt DESC`)
	if err != nil {
		return nil, fmt.Errorf("get api keys: %w", err)
	}
	defer rows.Close()

	keys := make([]*models.APIKey, 0)
	for rows.Next() {
		var k models.APIKey
		if err := rows.Scan(&k.ID, &k.Key, &k.Name, &k.MachineID, &k.IsActive, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, &k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate api keys: %w", err)
	}
	return keys, nil
}

// CreateApiKey inserts a new client API key with isActive set to 1.
func (r *Repo) CreateApiKey(id, key, name, machineID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var nameVal, machineVal any
	if name != "" {
		nameVal = name
	}
	if machineID != "" {
		machineVal = machineID
	}
	_, err := r.db.Exec(
		`INSERT INTO apiKeys (id, key, name, machineId, isActive, createdAt) VALUES (?, ?, ?, ?, 1, ?)`,
		id, key, nameVal, machineVal, now,
	)
	if err != nil {
		return fmt.Errorf("create api key %s: %w", id, err)
	}
	return nil
}

// DeleteApiKey deletes a client API key by its ID.
func (r *Repo) DeleteApiKey(id string) error {
	_, err := r.db.Exec(`DELETE FROM apiKeys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete api key %s: %w", id, err)
	}
	return nil
}

// SetApiKeyStatus updates the isActive status of a client API key.
func (r *Repo) SetApiKeyStatus(id string, isActive bool) error {
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	_, err := r.db.Exec(`UPDATE apiKeys SET isActive = ? WHERE id = ?`, activeInt, id)
	if err != nil {
		return fmt.Errorf("set api key status %s: %w", id, err)
	}
	return nil
}

// GetKVScope retrieves all key-value pairs for a given scope.
func (r *Repo) GetKVScope(scope string) (map[string]string, error) {
	rows, err := r.db.Query(`SELECT key, value FROM kv WHERE scope = ?`, scope)
	if err != nil {
		return nil, fmt.Errorf("get kv scope %s: %w", scope, err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var key, val string
		if err := rows.Scan(&key, &val); err != nil {
			return nil, fmt.Errorf("scan kv scope %s: %w", scope, err)
		}
		result[key] = val
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate kv scope %s: %w", scope, err)
	}
	return result, nil
}

// SetKV inserts or updates a key-value pair for a given scope.
func (r *Repo) SetKV(scope, key, value string) error {
	_, err := r.db.Exec(
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?) ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		scope, key, value,
	)
	if err != nil {
		return fmt.Errorf("set kv [%s] %s: %w", scope, key, err)
	}
	return nil
}

// DeleteKV removes a key-value pair for a given scope.
func (r *Repo) DeleteKV(scope, key string) error {
	_, err := r.db.Exec(`DELETE FROM kv WHERE scope = ? AND key = ?`, scope, key)
	if err != nil {
		return fmt.Errorf("delete kv [%s] %s: %w", scope, key, err)
	}
	return nil
}

// GetSettingsRaw reads settings row id=1 data JSON into map[string]any.
// Returns an empty map if no settings row exists.
func (r *Repo) GetSettingsRaw() (map[string]any, error) {
	var rawData string
	err := r.db.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&rawData)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return make(map[string]any), nil
		}
		return nil, fmt.Errorf("get settings raw: %w", err)
	}
	if rawData == "" {
		return make(map[string]any), nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil {
		return nil, fmt.Errorf("unmarshal settings data: %w", err)
	}
	if raw == nil {
		raw = make(map[string]any)
	}
	return raw, nil
}

// UpdateSettingsRaw reads settings row id=1 data JSON into map[string]any,
// merges updates into it, re-marshals, and updates settings row id=1.
func (r *Repo) UpdateSettingsRaw(updates map[string]any) error {
	existing, err := r.GetSettingsRaw()
	if err != nil {
		existing = make(map[string]any)
	}
	for k, v := range updates {
		existing[k] = v
	}
	b, err := json.Marshal(existing)
	if err != nil {
		return fmt.Errorf("marshal settings data: %w", err)
	}
	_, err = r.db.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		string(b),
	)
	if err != nil {
		return fmt.Errorf("update settings raw: %w", err)
	}
	return nil
}
