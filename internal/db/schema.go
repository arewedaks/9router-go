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
	statements := []string{
		`CREATE TABLE IF NOT EXISTS apiKeys (
			id TEXT PRIMARY KEY,
			key TEXT UNIQUE NOT NULL,
			name TEXT,
			machineId TEXT,
			isActive INTEGER DEFAULT 1,
			createdAt TEXT NOT NULL
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
			updatedAt TEXT NOT NULL
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
			payload TEXT NOT NULL,
			cachedAt TEXT NOT NULL,
			PRIMARY KEY (providerId, modelId)
		)`,
	}
	for _, stmt := range statements {
		if _, err := database.Exec(stmt); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	return nil
}

// EnsureSchema on the global connection, for callers that do not hold the *sql.DB.
func EnsureGlobalSchema() error {
	conn, err := GetConnection()
	if err != nil {
		return err
	}
	return EnsureSchema(conn)
}
