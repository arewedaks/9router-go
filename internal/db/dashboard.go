package db

import (
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"sort"
	"strings"
	"time"

	"9router/proxy/internal/models"
)

// GetAllProviderConnections retrieves all provider connections ordered by priority and update time.
func (r *Repo) GetAllProviderConnections() ([]*models.ProviderConnection, error) {
	query := `SELECT id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt,
		COALESCE(lastUsedAt, ''), COALESCE(consecutiveUseCount, 0)
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
			&conn.LastUsedAt, &conn.ConsecutiveUseCount,
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

// ListCachedModelsForDashboard returns the imported models for one provider,
// merging both stores an import can land in.
//
// AddCachedModel writes cachedProviderModels, but a database restored from the
// Next.js build carries its catalogue in kv scope customModels under
// "<provider>|<model>|<kind>". Only the first was read by the provider detail
// page, so a provider whose 72 models lived in customModels rendered an empty
// Models tab while /v1/models — which reads customModels — listed all 72.
//
// The merge lives here rather than in ListCachedModels because that one is also
// used by /v1/models, where widening the result would list the same model twice
// under a provider's canonical id and its alias.
func (r *Repo) ListCachedModelsForDashboard(keys ...string) ([]CachedModel, error) {
	out, err := r.ListCachedModels(keys...)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(out))
	for _, m := range out {
		seen[m.ModelID] = true
	}

	custom, err := r.customModelsFor(keys)
	if err != nil {
		return nil, err
	}
	for _, m := range custom {
		if seen[m.ModelID] {
			continue
		}
		seen[m.ModelID] = true
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModelID < out[j].ModelID })
	return out, nil
}

// customModelsFor returns the kv scope customModels rows belonging to any of
// the given provider keys, in the same shape as cachedProviderModels. The scope
// stores every provider's models in one table, so the provider prefix is part of
// the key and has to be matched here rather than with an IN clause.
func (r *Repo) customModelsFor(keys []string) ([]CachedModel, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	prefixes := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != "" {
			prefixes = append(prefixes, k+"|")
		}
	}
	if len(prefixes) == 0 {
		return nil, nil
	}

	q := "SELECT key, COALESCE(value,'') FROM kv WHERE scope = 'customModels'"
	rows, err := r.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("query custom models: %w", err)
	}
	defer rows.Close()

	var out []CachedModel
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, fmt.Errorf("scan custom model: %w", err)
		}
		matched := false
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		parts := strings.Split(key, "|")
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		m := CachedModel{ModelID: parts[1], Kind: "llm", OwnedBy: parts[0]}
		if len(parts) >= 3 && parts[2] != "" {
			m.Kind = parts[2]
		}
		if raw != "" {
			var cm CustomModel
			if err := json.Unmarshal([]byte(raw), &cm); err == nil {
				if cm.ID != "" {
					m.ModelID = cm.ID
				}
				if cm.Type != "" {
					m.Kind = cm.Type
				}
				m.DisplayName = cm.Name
			}
		}
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

// hiddenModelsScope is the kv scope holding (providerKey|modelID) entries for
// models an operator explicitly removed from a provider's page. The model
// cache alone is not enough to hide a model from /v1/models: the engine builds
// that list from the static registry, customModels, and enabledModels too, so
// a cache-only delete let removed models reappear.
const hiddenModelsScope = "hiddenModels"

// HideModel records that (providerKey, modelID) must not be advertised by
// /v1/models. Candidates cover the canonical id, the raw request key, and every
// registered alias so a later lookup by any spelling matches.
func (r *Repo) HideModel(providerKeys []string, modelID string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return fmt.Errorf("model ID is required")
	}
	for _, k := range dedupeStrings(providerKeys) {
		if k == "" {
			continue
		}
		if _, err := r.db.Exec(
			`INSERT INTO kv (scope, key, value) VALUES (?, ?, '1')
			 ON CONFLICT(scope, key) DO NOTHING`,
			hiddenModelsScope, k+"|"+modelID,
		); err != nil {
			return fmt.Errorf("hide model: %w", err)
		}
	}
	return nil
}

// UnhideModel clears a previous HideModel for the given provider keys.
func (r *Repo) UnhideModel(providerKeys []string, modelID string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return fmt.Errorf("model ID is required")
	}
	for _, k := range dedupeStrings(providerKeys) {
		if k == "" {
			continue
		}
		if _, err := r.db.Exec(
			"DELETE FROM kv WHERE scope = ? AND key = ?",
			hiddenModelsScope, k+"|"+modelID,
		); err != nil {
			return fmt.Errorf("unhide model: %w", err)
		}
	}
	return nil
}

// GetAllHiddenModels returns every hidden marker as a set keyed by
// "providerKey|modelID". The table only ever holds models an operator removed
// by hand, so it stays tiny and one full scan beats a query per provider key
// (which would otherwise fan out to hundreds of queries for unknown node ids).
func (r *Repo) GetAllHiddenModels() (map[string]bool, error) {
	out := map[string]bool{}
	rows, err := r.db.Query(
		"SELECT key FROM kv WHERE scope = ?", hiddenModelsScope,
	)
	if err != nil {
		return nil, fmt.Errorf("query hidden models: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan hidden model: %w", err)
		}
		out[key] = true
	}
	return out, rows.Err()
}

// GetHiddenModels returns the set of hidden models keyed by
// "providerKey|modelID" restricted to the provider keys supplied. Prefer
// GetAllHiddenModels for whole-list filtering.
func (r *Repo) GetHiddenModels(providerKeys []string) (map[string]bool, error) {
	out := map[string]bool{}
	keys := dedupeStrings(providerKeys)
	if len(keys) == 0 {
		return out, nil
	}
	all, err := r.GetAllHiddenModels()
	if err != nil {
		return nil, err
	}
	prefixes := make([]string, 0, len(keys))
	for _, k := range keys {
		prefixes = append(prefixes, k+"|")
	}
	for key := range all {
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				out[key] = true
				break
			}
		}
	}
	return out, nil
}

// dedupeStrings removes empty and duplicate entries while preserving order.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
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

// UpdateProviderNode rewrites a node's name and data blob, bumping updatedAt.
// The id and type are immutable: the id is the routing key connections reference
// and the type is baked into the id, so changing either would orphan the node.
func (r *Repo) UpdateProviderNode(id, name, data string) error {
	_, err := r.db.Exec(
		`UPDATE providerNodes SET name = ?, data = ?, updatedAt = ? WHERE id = ?`,
		name, data, time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

// DeleteProviderNode removes a node. It reports whether a row was actually
// deleted so the caller can answer 404 instead of a misleading 200.
func (r *Repo) DeleteProviderNode(id string) (bool, error) {
	res, err := r.db.Exec(`DELETE FROM providerNodes WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// CountProviderNodeConnections returns how many connections point at a node.
// Deleting a node that still has connections would leave them unreachable, so
// the delete handler refuses unless the caller explicitly cascades.
func (r *Repo) CountProviderNodeConnections(id string) (int, error) {
	var n int
	err := r.db.QueryRow(
		`SELECT COUNT(*) FROM providerConnections WHERE provider = ?`, id,
	).Scan(&n)
	return n, err
}

// DeleteConnectionsForProvider removes every connection of a provider.
func (r *Repo) DeleteConnectionsForProvider(provider string) (int, error) {
	res, err := r.db.Exec(`DELETE FROM providerConnections WHERE provider = ?`, provider)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}
func (r *Repo) UpsertCombo(c *models.Combo) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if c.CreatedAt == "" {
		c.CreatedAt = now
	}
	c.UpdatedAt = now

	query := `INSERT INTO combos (id, name, kind, models, strategy, createdAt, updatedAt)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			kind = excluded.kind,
			models = excluded.models,
			strategy = excluded.strategy,
			updatedAt = excluded.updatedAt`

	_, err := r.db.Exec(query, c.ID, c.Name, c.Kind, c.Models, c.Strategy, c.CreatedAt, c.UpdatedAt)
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
	query := `SELECT id, key, name, machineId, isActive, createdAt, allowedProviders, allowedCombos, allowedKinds FROM apiKeys ORDER BY createdAt DESC`
	rows, err := r.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query api keys: %w", err)
	}
	defer rows.Close()

	var keys []*models.APIKey
	for rows.Next() {
		var k models.APIKey
		var allowedProviders, allowedCombos, allowedKinds sql.NullString
		err := rows.Scan(&k.ID, &k.Key, &k.Name, &k.MachineID, &k.IsActive, &k.CreatedAt,
			&allowedProviders, &allowedCombos, &allowedKinds)
		if err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		k.AllowedProviders = decodeACLList(allowedProviders)
		k.AllowedCombos = decodeACLList(allowedCombos)
		k.AllowedKinds = decodeACLList(allowedKinds)
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

// UpdateApiKeyACL replaces a key's access lists.
//
// A nil slice clears the restriction (stored NULL = everything allowed); an
// empty non-nil slice stores `[]` (nothing allowed). The caller passes nil for
// any list it does not want to change.
func (r *Repo) UpdateApiKeyACL(id string, providers, combos, kinds []string) error {
	encode := func(v []string) any {
		if v == nil {
			return nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			// A list that cannot be encoded is stored as "nothing allowed"
			// rather than NULL, so a failed encode never silently widens access.
			return "[]"
		}
		return string(b)
	}
	_, err := r.db.Exec(
		"UPDATE apiKeys SET allowedProviders = ?, allowedCombos = ?, allowedKinds = ? WHERE id = ?",
		encode(providers), encode(combos), encode(kinds), id,
	)
	if err != nil {
		return fmt.Errorf("update api key acl: %w", err)
	}
	return nil
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
