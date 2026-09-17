package db

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"9router/proxy/internal/models"
)

// GetAllProviderConnections retrieves all provider connections ordered by priority and update time.
func (r *Repo) GetAllProviderConnections() ([]*models.ProviderConnection, error) {
	query := `SELECT id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt
		FROM providerConnections
		ORDER BY CASE WHEN priority IS NULL THEN 999999 ELSE priority END ASC, updatedAt DESC`

	rows, err := r.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query provider connections: %w", err)
	}
	defer rows.Close()

	var connections []*models.ProviderConnection
	for rows.Next() {
		var conn models.ProviderConnection
		err := rows.Scan(
			&conn.ID, &conn.Provider, &conn.AuthType, &conn.Name, &conn.Email,
			&conn.Priority, &conn.IsActive, &conn.Data, &conn.CreatedAt, &conn.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan provider connection: %w", err)
		}
		connections = append(connections, &conn)
	}

	return connections, rows.Err()
}

// UpsertProviderConnection creates or updates a provider connection.
func (r *Repo) UpsertProviderConnection(conn *models.ProviderConnection) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if conn.CreatedAt == "" {
		conn.CreatedAt = now
	}
	conn.UpdatedAt = now

	query := `INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			provider = excluded.provider,
			authType = excluded.authType,
			name = excluded.name,
			email = excluded.email,
			priority = excluded.priority,
			isActive = excluded.isActive,
			data = excluded.data,
			updatedAt = excluded.updatedAt`

	_, err := r.db.Exec(query,
		conn.ID, conn.Provider, conn.AuthType, conn.Name, conn.Email,
		conn.Priority, conn.IsActive, conn.Data, conn.CreatedAt, conn.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert provider connection: %w", err)
	}
	return nil
}

// ToggleProviderConnection flips the isActive state of a provider connection.
func (r *Repo) ToggleProviderConnection(id string) (int, error) {
	var current int
	err := r.db.QueryRow("SELECT isActive FROM providerConnections WHERE id = ?", id).Scan(&current)
	if err != nil {
		return 0, fmt.Errorf("lookup provider connection: %w", err)
	}

	newVal := 0
	if current == 0 {
		newVal = 1
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = r.db.Exec("UPDATE providerConnections SET isActive = ?, updatedAt = ? WHERE id = ?", newVal, now, id)
	if err != nil {
		return 0, fmt.Errorf("toggle provider connection: %w", err)
	}
	return newVal, nil
}

// DeleteProviderConnection removes a provider connection by ID.
func (r *Repo) DeleteProviderConnection(id string) error {
	_, err := r.db.Exec("DELETE FROM providerConnections WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete provider connection: %w", err)
	}
	return nil
}

// ListConnectionsByProvider returns every connection belonging to one provider.
func (r *Repo) ListConnectionsByProvider(provider string) ([]*models.ProviderConnection, error) {
	all, err := r.GetAllProviderConnections()
	if err != nil {
		return nil, err
	}
	var out []*models.ProviderConnection
	for _, c := range all {
		if c.Provider == provider {
			out = append(out, c)
		}
	}
	return out, nil
}

// SetProviderActive toggles every connection of a provider at once, mirroring
// the upstream "Enable All" / "Disable All" controls.
func (r *Repo) SetProviderActive(provider string, active int) (int, error) {
	res, err := r.db.Exec(
		"UPDATE providerConnections SET isActive = ?, updatedAt = ? WHERE provider = ?",
		active, time.Now().UTC().Format(time.RFC3339), provider,
	)
	if err != nil {
		return 0, fmt.Errorf("set provider active: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CachedModel is one entry of the provider model cache surfaced on the
// provider detail page (upstream "Available Models" section).
type CachedModel struct {
	ModelID      string `json:"modelId"`
	Kind         string `json:"kind"`
	OwnedBy      string `json:"ownedBy"`
	Capabilities string `json:"capabilities,omitempty"`
	// DisplayName is the upstream's friendly label for the model (e.g. "V4.1
	// Flash" for `deepseek-v4.1-flash`), when it differs from the id. It is
	// persisted inside the otherwise-unused `capabilities` column as
	// `{"name":"..."}` so no schema migration is needed.
	DisplayName string `json:"displayName,omitempty"`
	// IsFree is computed per request by the provider handler (it depends on
	// provider free-tier membership, which the Repo does not know). It is not
	// stored.
	IsFree bool `json:"isFree"`
}

// modelCachePayload is the JSON blob stored in cachedProviderModels.capabilities.
// The column is free-form and had no other writer, so reusing it keeps the DB
// schema identical to the Next.js build (which is why no migration is needed).
type modelCachePayload struct {
	Name string `json:"name,omitempty"`
}

// decodeModelCacheName extracts the stored display name from the capabilities
// column, tolerating legacy/plain values (which simply yield "").
func decodeModelCacheName(capabilities string) string {
	capabilities = strings.TrimSpace(capabilities)
	if capabilities == "" || !strings.HasPrefix(capabilities, "{") {
		return ""
	}
	var payload modelCachePayload
	if err := json.Unmarshal([]byte(capabilities), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Name)
}

// ListCachedModels returns cached models for any of the given provider keys
// (canonical ID plus its short aliases), matching upstream behaviour where the
// cache is keyed by whichever identifier the caller used.
func (r *Repo) ListCachedModels(keys ...string) ([]CachedModel, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	placeholders := ""
	args := make([]any, 0, len(keys))
	for i, k := range keys {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, k)
	}

	q := "SELECT modelId, COALESCE(kind,'llm'), COALESCE(ownedBy,''), COALESCE(capabilities,'') " +
		"FROM cachedProviderModels WHERE providerId IN (" + placeholders + ") ORDER BY modelId"
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query cached models: %w", err)
	}
	defer rows.Close()

	var out []CachedModel
	for rows.Next() {
		var m CachedModel
		if err := rows.Scan(&m.ModelID, &m.Kind, &m.OwnedBy, &m.Capabilities); err != nil {
			return nil, fmt.Errorf("scan cached model: %w", err)
		}
		m.DisplayName = decodeModelCacheName(m.Capabilities)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ResolveModelCacheKey picks the identifier already used to store this
// provider's models in cachedProviderModels. The Next.js build keys the cache
// by short alias ("ag"), so we must write new models under the same key or
// they would never be read back. Falls back to the canonical ID when the
// provider has no aliases registered.
func (r *Repo) ResolveModelCacheKey(canonical string, aliasCandidates []string) string {
	keys := append([]string{canonical}, aliasCandidates...)
	for _, k := range keys {
		var n int
		if err := r.db.QueryRow(
			"SELECT COUNT(*) FROM cachedProviderModels WHERE providerId = ?", k,
		).Scan(&n); err == nil && n > 0 {
			return k
		}
	}
	// No existing rows: use the first alias when available, since that is
	// what the Next.js model refresh job will query.
	if len(aliasCandidates) > 0 {
		return aliasCandidates[0]
	}
	return canonical
}

// AddCachedModel inserts a user-defined model into the provider model cache,
// mirroring the upstream "Add Custom Model" action on
// /dashboard/providers/[id].
func (r *Repo) AddCachedModel(providerKey, modelID, kind, ownedBy string) error {
	return r.AddCachedModelWithName(providerKey, modelID, kind, ownedBy, "")
}

// AddCachedModelWithName is AddCachedModel plus the upstream display name.
// The name is stored in the free-form capabilities column (see CachedModel),
// and an empty name deliberately does NOT clobber a previously stored one, so
// a re-import that drops the label never erases it.
func (r *Repo) AddCachedModelWithName(providerKey, modelID, kind, ownedBy, displayName string) error {
	if modelID == "" {
		return fmt.Errorf("model ID is required")
	}
	if kind == "" {
		kind = "llm"
	}
	if ownedBy == "" {
		ownedBy = "custom"
	}
	payload := ""
	if name := strings.TrimSpace(displayName); name != "" {
		if encoded, err := json.Marshal(modelCachePayload{Name: name}); err == nil {
			payload = string(encoded)
		}
	}
	_, err := r.db.Exec(
		`INSERT INTO cachedProviderModels (providerId, modelId, kind, ownedBy, capabilities, updatedAt)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(providerId, modelId) DO UPDATE SET
			kind = excluded.kind,
			ownedBy = excluded.ownedBy,
			capabilities = CASE WHEN excluded.capabilities IN ('', '{}') THEN capabilities ELSE excluded.capabilities END,
			updatedAt = excluded.updatedAt`,
		providerKey, modelID, kind, ownedBy, payload, time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("add cached model: %w", err)
	}
	return nil
}

// RemoveCachedModel deletes a model from the provider model cache, mirroring
// the upstream "Remove model" action.
func (r *Repo) RemoveCachedModel(providerKey, modelID string) error {
	_, err := r.db.Exec(
		"DELETE FROM cachedProviderModels WHERE providerId = ? AND modelId = ?",
		providerKey, modelID,
	)
	if err != nil {
		return fmt.Errorf("remove cached model: %w", err)
	}
	return nil
}

// GetAllProviderNodes retrieves all configured provider nodes for name resolution.
func (r *Repo) GetAllProviderNodes() ([]*models.ProviderNode, error) {
	rows, err := r.db.Query("SELECT id, type, name, data, createdAt, updatedAt FROM providerNodes")
	if err != nil {
		return nil, fmt.Errorf("query provider nodes: %w", err)
	}
	defer rows.Close()

	var nodes []*models.ProviderNode
	for rows.Next() {
		var n models.ProviderNode
		if err := rows.Scan(&n.ID, &n.Type, &n.Name, &n.Data, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan provider node: %w", err)
		}
		nodes = append(nodes, &n)
	}
	return nodes, rows.Err()
}

// CreateProviderNode inserts a provider node row. The caller owns the id, name
// and data blob (JSON), matching how upstream stores compatible endpoints: the
// node's prefix lives inside `data`, not in its own column. Returns an error if
// the id already exists rather than overwriting an unrelated node.
func (r *Repo) CreateProviderNode(n *models.ProviderNode) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if n.CreatedAt == "" {
		n.CreatedAt = now
	}
	n.UpdatedAt = now

	_, err := r.db.Exec(
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		n.ID, n.Type, n.Name, n.Data, n.CreatedAt, n.UpdatedAt,
	)
	return err
}

// UpsertCombo creates or updates a combo.
func (r *Repo) UpsertCombo(c *models.Combo) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if c.CreatedAt == "" {
		c.CreatedAt = now
	}
	c.UpdatedAt = now

	query := `INSERT INTO combos (id, name, kind, models, createdAt, updatedAt)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			kind = excluded.kind,
			models = excluded.models,
			updatedAt = excluded.updatedAt`

	_, err := r.db.Exec(query, c.ID, c.Name, c.Kind, c.Models, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert combo: %w", err)
	}
	return nil
}

// DeleteCombo removes a combo by ID.
func (r *Repo) DeleteCombo(id string) error {
	_, err := r.db.Exec("DELETE FROM combos WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete combo: %w", err)
	}
	return nil
}

// GetAllApiKeys retrieves all API keys.
func (r *Repo) GetAllApiKeys() ([]*models.APIKey, error) {
	query := `SELECT id, key, name, machineId, isActive, createdAt FROM apiKeys ORDER BY createdAt DESC`
	rows, err := r.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query api keys: %w", err)
	}
	defer rows.Close()

	var keys []*models.APIKey
	for rows.Next() {
		var k models.APIKey
		err := rows.Scan(&k.ID, &k.Key, &k.Name, &k.MachineID, &k.IsActive, &k.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

// CreateApiKeyWithDetails creates a new API key record.
func (r *Repo) CreateApiKeyWithDetails(id, key, name string) (*models.APIKey, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`INSERT INTO apiKeys (id, key, name, isActive, createdAt) VALUES (?, ?, ?, 1, ?)`,
		id, key, name, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}

	return &models.APIKey{
		ID:        id,
		Key:       key,
		Name:      &name,
		IsActive:  1,
		CreatedAt: now,
	}, nil
}

// ToggleApiKey flips the active state of an API key.
func (r *Repo) ToggleApiKey(id string) (int, error) {
	var current int
	err := r.db.QueryRow("SELECT isActive FROM apiKeys WHERE id = ?", id).Scan(&current)
	if err != nil {
		return 0, fmt.Errorf("lookup api key: %w", err)
	}

	newVal := 0
	if current == 0 {
		newVal = 1
	}

	_, err = r.db.Exec("UPDATE apiKeys SET isActive = ? WHERE id = ?", newVal, id)
	if err != nil {
		return 0, fmt.Errorf("toggle api key: %w", err)
	}
	return newVal, nil
}

// DeleteApiKey removes an API key by ID.
func (r *Repo) DeleteApiKey(id string) error {
	_, err := r.db.Exec("DELETE FROM apiKeys WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	return nil
}

// SaveSettingsData persists the updated SettingsData to the settings table.
func (r *Repo) SaveSettingsData(s *SettingsData) error {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	_, err = r.db.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, string(b))
	if err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	return nil
}
