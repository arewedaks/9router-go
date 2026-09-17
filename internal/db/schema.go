package db

import (
	"database/sql"
	"fmt"
)

// EnsureSchema creates the tables 9Router needs if they are not present yet.
//
// Why this exists: the Go proxy was originally a companion that expected the
// Next.js dashboard to have migrated the SQLite file first. On a fresh install
// that assumption fails and every write (settings, connections, keys) errors
// with "no such table". CREATE TABLE IF NOT EXISTS makes startup idempotent: an
// existing Next.js-migrated database is untouched, and a fresh one becomes
// usable without a separate migration step.
//
// The statements mirror the canonical schema shared with the dashboard (see
// internal/dbtest.SchemaStatements) so both implementations agree on shape.
func EnsureSchema(database *sql.DB) error {
	for _, stmt := range SchemaStatements() {
		if _, err := database.Exec(stmt); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	return nil
}

// SchemaStatements returns the canonical CREATE TABLE statements. It is exported
// so test helpers build the same schema production does; duplicating the list
// is how the test copy drifted from reality in the first place.
func SchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS apiKeys (
			id TEXT PRIMARY KEY,
			key TEXT UNIQUE NOT NULL,
			name TEXT,
			machineId TEXT,
			isActive INTEGER DEFAULT 1,
			createdAt TEXT NOT NULL,
			allowedProviders TEXT,
			allowedCombos TEXT,
			allowedKinds TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS providerConnections (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			authType TEXT NOT NULL,
			name TEXT,
			email TEXT,
			priority INTEGER,
			isActive INTEGER DEFAULT 1,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS kv (
			scope TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (scope, key)
		)`,
		`CREATE TABLE IF NOT EXISTS combos (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			kind TEXT,
			models TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL,
			context_length INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			data TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS providerNodes (
			id TEXT PRIMARY KEY,
			type TEXT,
			name TEXT,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS cachedProviderModels (
			providerId TEXT NOT NULL,
			modelId TEXT NOT NULL,
			kind TEXT DEFAULT 'llm',
			ownedBy TEXT NOT NULL,
			capabilities TEXT,
			updatedAt INTEGER NOT NULL,
			PRIMARY KEY (providerId, modelId)
		)`,
		`CREATE TABLE IF NOT EXISTS proxyPools (
			id TEXT PRIMARY KEY,
			isActive INTEGER DEFAULT 1,
			testStatus TEXT,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS proxyPoolFitness (
			poolId TEXT NOT NULL,
			scope TEXT NOT NULL,
			until INTEGER NOT NULL,
			reason TEXT,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL,
			PRIMARY KEY (poolId, scope)
		)`,
		// Usage tables. The Next.js dashboard used to be the only thing that
		// created these; without them a Go-only install silently fails every
		// usage write with "no such table". The column list mirrors the
		// upstream DDL so an existing database is left untouched.
		`CREATE TABLE IF NOT EXISTS usageHistory (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT NOT NULL,
			provider TEXT,
			model TEXT,
			connectionId TEXT,
			apiKey TEXT,
			endpoint TEXT,
			promptTokens INTEGER DEFAULT 0,
			completionTokens INTEGER DEFAULT 0,
			cost REAL DEFAULT 0,
			status TEXT,
			tokens TEXT,
			meta TEXT,
			apiKeyName TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS usageDaily (
			dateKey TEXT PRIMARY KEY,
			data TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS requestDetails (
			id TEXT PRIMARY KEY,
			timestamp TEXT NOT NULL,
			provider TEXT,
			model TEXT,
			connectionId TEXT,
			status TEXT,
			data TEXT NOT NULL,
			apiKey TEXT,
			apiKeyName TEXT
		)`,
	}
}

// EnsureSchema on the global connection, for callers that do not hold the *sql.DB.
func EnsureGlobalSchema() error {
	conn, err := GetConnection()
	if err != nil {
		return err
	}
	return EnsureSchema(conn)
}
