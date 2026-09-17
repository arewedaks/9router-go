package dbtest

import (
	"database/sql"
	"fmt"

	"9router/proxy/internal/db"
)

// SchemaStatements returns all CREATE TABLE statements used by 9Router tests.
//
// The statements live in internal/db so tests and production build the same
// schema. They used to be duplicated here, and the copies drifted: tests were
// creating an apiKeys table without the allowedProviders/allowedCombos/
// allowedKinds columns and no proxyPools table at all, so a test could pass
// against a shape production does not have.
func SchemaStatements() []string {
	return db.SchemaStatements()
}

// CreateTables creates all tables from SchemaStatements in the given database.
func CreateTables(database *sql.DB) error {
	for _, stmt := range SchemaStatements() {
		if _, err := database.Exec(stmt); err != nil {
			return fmt.Errorf("create table: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}
