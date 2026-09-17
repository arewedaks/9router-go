package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestSchemaStatementsCoverEveryUsedTable guards a whole class of bug that was
// real: EnsureSchema listed fewer tables and columns than the code used, so a
// fresh install (which relies on EnsureSchema) could not store provider
// connections' scoped keys, combo context lengths, or proxy pools, while a
// database migrated by the Next.js dashboard had them all.
//
// It is a static check on purpose. Comparing against a live production database
// would only work on the machine that has one; scanning the source for the
// tables and columns the code actually touches works everywhere.
func TestSchemaStatementsCoverEveryUsedTable(t *testing.T) {
	schema := strings.Join(SchemaStatements(), "\n")

	// Every table the repository queries must be creatable by EnsureSchema.
	usedTables := []string{
		"apiKeys", "providerConnections", "kv", "combos", "settings",
		"providerNodes", "cachedProviderModels", "proxyPools", "proxyPoolFitness",
	}
	for _, tb := range usedTables {
		if !strings.Contains(schema, "TABLE IF NOT EXISTS "+tb+" ") &&
			!strings.Contains(schema, "TABLE IF NOT EXISTS "+tb+"(") {
			t.Errorf("EnsureSchema does not create the %s table, which the code queries;\n"+
				"a fresh install would fail with \"no such table\"", tb)
		}
	}

	// Columns the code names explicitly. If one is missing from the schema, the
	// matching query works only on a database that was migrated elsewhere.
	usedColumns := map[string][]string{
		"apiKeys":              {"allowedProviders", "allowedCombos", "allowedKinds"},
		"combos":               {"context_length"},
		"cachedProviderModels": {"kind", "ownedBy", "capabilities", "updatedAt"},
		"providerConnections":  {"priority", "isActive", "data"},
		"proxyPools":           {"isActive", "testStatus", "data"},
		"proxyPoolFitness":     {"poolId", "scope", "until", "reason"},
	}
	for table, cols := range usedColumns {
		body := tableBody(schema, table)
		if body == "" {
			t.Errorf("could not find the definition of %s in the schema", table)
			continue
		}
		for _, col := range cols {
			// Match the column as a leading identifier so `data` does not match
			// inside `database`.
			if !containsColumn(body, col) {
				t.Errorf("%s is missing column %q; queries naming it fail on a fresh install", table, col)
			}
		}
	}
}

// tableBody extracts the text between the parentheses of a CREATE TABLE.
func tableBody(schema, table string) string {
	idx := strings.Index(schema, "TABLE IF NOT EXISTS "+table+" (")
	if idx < 0 {
		return ""
	}
	rest := schema[idx:]
	open := strings.Index(rest, "(")
	depth := 0
	for i := open; i < len(rest); i++ {
		switch rest[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				// Count the parentheses in this body too: a nested
				// PRIMARY KEY (a, b) closes and reopens before the table does,
				// and returning at the first outer-depth zero would cut the body
				// short, hiding every column declared after the key clause.
				return rest[open : i+1]
			}
		}
	}
	return ""
}

// containsColumn reports whether a column definition lists name as an identifier.
//
// A column either starts its own definition line, or appears inside a trailing
// PRIMARY KEY (...) clause — both are valid ways to declare it, and missing the
// second produced a false report that proxyPoolFitness had no poolId.
func containsColumn(body, name string) bool {
	for _, line := range strings.Split(body, ",") {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == name {
			return true
		}
		if strings.HasPrefix(strings.ToUpper(trimmed), "PRIMARY KEY") {
			open := strings.Index(trimmed, "(")
			close := strings.LastIndex(trimmed, ")")
			if open >= 0 && close > open {
				for _, part := range strings.Split(trimmed[open+1:close], ",") {
					if strings.TrimSpace(part) == name {
						return true
					}
				}
			}
		}
	}
	return false
}

// TestEnsureSchemaIsIdempotent: it runs on every startup, including against a
// database that already has the tables.
func TestEnsureSchemaIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	for i := 0; i < 3; i++ {
		if err := EnsureSchema(db); err != nil {
			t.Fatalf("EnsureSchema run %d: %v", i+1, err)
		}
	}
}
