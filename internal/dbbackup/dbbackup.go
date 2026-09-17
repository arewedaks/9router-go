// Package dbbackup exports and restores the whole router database as a single
// JSON document, matching the backup format the Next.js reference exposes at
// /api/settings/database.
//
// Two properties of the reference are load-bearing and are preserved here:
//
//   - Import is a full replace inside one transaction, not a merge. A backup is
//     meant to restore a known-good state, so anything absent from the file has
//     to go away; otherwise a half-populated database would linger behind the
//     restored one and the result would match neither.
//   - The whole thing is one transaction. A backup that fails halfway through
//     must leave the database exactly as it was, since the alternative is a
//     database with no provider connections and half the settings.
//
// The payload deliberately carries real tokens for every connection, so a
// backup file is as sensitive as the database itself. Handlers gate access
// accordingly.
package dbbackup

import (
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

// FormatVersion identifies the layout of the document. It is written so a future
// importer can recognise an older file rather than silently mis-reading it.
const FormatVersion = 1

// Document is the backup file: every table needed to rebuild a working router.
//
// Rows are carried as maps rather than typed structs on purpose. The tables
// store their variable parts in a JSON `data` column, and the set of keys in
// there grows as providers are added; a typed struct would silently drop any
// field it did not know about, which is exactly the kind of loss a backup is
// supposed to prevent.
type Document struct {
	FormatVersion int    `json:"formatVersion"`
	ExportedAt    string `json:"exportedAt"`
	App           string `json:"app"`

	Settings            map[string]any   `json:"settings"`
	ProviderConnections []map[string]any `json:"providerConnections"`
	ProviderNodes       []map[string]any `json:"providerNodes"`
	ProxyPools          []map[string]any `json:"proxyPools"`
	ProxyPoolFitness    []map[string]any `json:"proxyPoolFitness"`
	ApiKeys             []map[string]any `json:"apiKeys"`
	Combos              []map[string]any `json:"combos"`

	// Key/value scopes, mirroring the reference's shape so the same file stays
	// roughly readable to anyone who has seen a VansRouter backup.
	ModelAliases []map[string]any `json:"modelAliases"`
	CustomModels []map[string]any `json:"customModels"`
}

// tableSpec describes one table's rows: the columns to read, and how to write
// them back. Keeping this as data rather than hand-written loops makes the
// export and import sides impossible to drift apart.
type tableSpec struct {
	name string
	// columns are the canonical columns. Some are optional because the SQLite
	// file is shared with the Next.js dashboard and may predate a column: an
	// older file simply will not have it, and reading it would fail the whole
	// export for one absent field.
	columns []string
	// optional lists columns that may not exist. They are dropped from the read
	// when absent, and defaulted on import so the insert still lines up.
	optional map[string]bool
	// jsonFields are columns stored as JSON text; they are decoded on export so
	// the file is readable, and re-encoded on import.
	jsonFields map[string]bool
	// boolFields are INTEGER 0/1 columns surfaced as JSON booleans.
	boolFields map[string]bool
	// target is where the exported rows are placed.
	target func(*Document) *[]map[string]any
}

var tables = []tableSpec{
	{
		name:       "providerConnections",
		columns:    []string{"id", "provider", "authType", "name", "email", "priority", "isActive", "data", "createdAt", "updatedAt"},
		jsonFields: map[string]bool{"data": true},
		boolFields: map[string]bool{"isActive": true},
		target:     func(d *Document) *[]map[string]any { return &d.ProviderConnections },
	},
	{
		name:       "providerNodes",
		columns:    []string{"id", "type", "name", "data", "createdAt", "updatedAt"},
		jsonFields: map[string]bool{"data": true},
		target:     func(d *Document) *[]map[string]any { return &d.ProviderNodes },
	},
	{
		name:       "proxyPools",
		columns:    []string{"id", "isActive", "testStatus", "data", "createdAt", "updatedAt"},
		jsonFields: map[string]bool{"data": true},
		boolFields: map[string]bool{"isActive": true},
		target:     func(d *Document) *[]map[string]any { return &d.ProxyPools },
	},
	{
		name:    "proxyPoolFitness",
		columns: []string{"poolId", "scope", "until", "reason", "createdAt", "updatedAt"},
		target:  func(d *Document) *[]map[string]any { return &d.ProxyPoolFitness },
	},
	{
		name:     "apiKeys",
		columns:  []string{"id", "key", "name", "machineId", "isActive", "createdAt", "allowedProviders", "allowedCombos", "allowedKinds"},
		optional: map[string]bool{"allowedProviders": true, "allowedCombos": true, "allowedKinds": true},
		// allowedProviders/allowedCombos/allowedKinds hold JSON arrays and do not
		// exist in the reference schema — dropping them would quietly widen a
		// scoped key back to full access after a restore.
		jsonFields: map[string]bool{"allowedProviders": true, "allowedCombos": true, "allowedKinds": true},
		boolFields: map[string]bool{"isActive": true},
		target:     func(d *Document) *[]map[string]any { return &d.ApiKeys },
	},
	{
		name:       "combos",
		columns:    []string{"id", "name", "kind", "models", "createdAt", "updatedAt", "context_length"},
		optional:   map[string]bool{"context_length": true},
		jsonFields: map[string]bool{"models": true},
		target:     func(d *Document) *[]map[string]any { return &d.Combos },
	},
}

// Export reads every table into a Document. Settings include the password hash,
// which is intentional: a restore that left the old password in place would
// produce a machine whose credentials do not match the backup.
func Export(db *sql.DB) (*Document, error) {
	doc := &Document{
		FormatVersion: FormatVersion,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		App:           "9router-go",
	}

	var err error
	if doc.Settings, err = exportSettings(db); err != nil {
		return nil, fmt.Errorf("export settings: %w", err)
	}

	for _, t := range tables {
		rows, err := exportTable(db, t)
		if err != nil {
			return nil, fmt.Errorf("export %s: %w", t.name, err)
		}
		*t.target(doc) = rows
	}

	if doc.ModelAliases, err = exportKVScope(db, "modelAliases"); err != nil {
		return nil, fmt.Errorf("export modelAliases: %w", err)
	}
	if doc.CustomModels, err = exportKVScope(db, "customModels"); err != nil {
		return nil, fmt.Errorf("export customModels: %w", err)
	}

	return doc, nil
}

func exportSettings(db *sql.DB) (map[string]any, error) {
	var raw string
	err := db.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func exportTable(db *sql.DB, t tableSpec) ([]map[string]any, error) {
	// A table the file does not have is not an error: an older database simply
	// never created it, and the export should still capture everything that
	// exists rather than refusing to produce a backup at all.
	present, err := tableExists(db, t.name)
	if err != nil {
		return nil, err
	}
	if !present {
		return []map[string]any{}, nil
	}

	cols := make([]string, 0, len(t.columns))
	for _, c := range t.columns {
		if t.optional[c] {
			ok, err := columnExists(db, t.name, c)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		cols = append(cols, c)
	}

	q := "SELECT " + strings.Join(cols, ", ") + " FROM " + t.name
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		rec := make(map[string]any, len(cols))
		for i, col := range cols {
			rec[col] = convertOut(holders[i], t, col)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// tableExists reports whether a table is present in the main schema.
func tableExists(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0, err
}

// columnExists reports whether a table has a named column.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	// PRAGMA does not accept a bound parameter for the table name, but the names
	// here are compile-time constants from tableSpec, never user input.
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   sql.NullString
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// convertOut turns a scanned column into its JSON representation.
//
// The driver is not consistent about the Go type it hands back: TEXT columns
// arrive as []byte, but INTEGER columns arrive as int64. Both are handled
// explicitly, because assuming []byte everywhere silently skipped the boolean
// conversion and emitted `1` where the file should say `true`.
func convertOut(v any, t tableSpec, col string) any {
	if t.boolFields[col] {
		switch n := v.(type) {
		case int64:
			return n != 0
		case []byte:
			return string(n) == "1"
		case string:
			return n == "1"
		case bool:
			return n
		}
	}
	switch val := v.(type) {
	case []byte:
		return decodeMaybeJSON(string(val), t.jsonFields[col])
	case string:
		return decodeMaybeJSON(val, t.jsonFields[col])
	default:
		return v
	}
}

func decodeMaybeJSON(s string, isJSON bool) any {
	if isJSON {
		return decodeJSON(s)
	}
	return s
}

func decodeJSON(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		// Keep the original text rather than losing it: an unparseable cell is
		// still data the operator may want back.
		return s
	}
	return v
}

func exportKVScope(db *sql.DB, scope string) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT key, value FROM kv WHERE scope = ?`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"key": k, "value": decodeJSON(v)})
	}
	return out, rows.Err()
}

// Import replaces the database contents with the document's, in a single
// transaction. A nil return means every row was written and committed.
func Import(db *sql.DB, doc *Document) error {
	if doc == nil {
		return fmt.Errorf("invalid database payload")
	}
	if doc.FormatVersion > FormatVersion {
		return fmt.Errorf("backup was written by a newer version (format %d, this build understands %d)",
			doc.FormatVersion, FormatVersion)
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	// Rollback is a no-op after a successful Commit.
	defer tx.Rollback()

	// Clear first. Deleting is scoped to the tables a backup covers so that
	// derived data (usage history, request details, the model cache) survives;
	// it is not part of the backup and wiping it would lose real history.
	// Clearing by named table rather than looping over every table keeps this
	// scoped: usage history and the model cache are not part of a backup, and
	// deleting them would destroy real records. Tables an older file never
	// created are skipped.
	deletes := []struct{ table, query string }{
		{"settings", `DELETE FROM settings`},
		{"providerConnections", `DELETE FROM providerConnections`},
		{"providerNodes", `DELETE FROM providerNodes`},
		{"proxyPools", `DELETE FROM proxyPools`},
		{"proxyPoolFitness", `DELETE FROM proxyPoolFitness`},
		{"apiKeys", `DELETE FROM apiKeys`},
		{"combos", `DELETE FROM combos`},
		{"kv", `DELETE FROM kv WHERE scope IN ('modelAliases', 'customModels')`},
	}
	for _, d := range deletes {
		if d.table != "kv" {
			present, err := tableExistsTx(tx, d.table)
			if err != nil {
				return err
			}
			if !present {
				continue
			}
		}
		if _, err := tx.Exec(d.query); err != nil {
			return fmt.Errorf("clear %s: %w", d.table, err)
		}
	}

	if len(doc.Settings) > 0 {
		encoded, err := json.Marshal(doc.Settings)
		if err != nil {
			return fmt.Errorf("encode settings: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO settings(id, data) VALUES(1, ?)`, string(encoded)); err != nil {
			return fmt.Errorf("import settings: %w", err)
		}
	}

	for _, t := range tables {
		if err := importTable(tx, t, *t.target(doc)); err != nil {
			return fmt.Errorf("import %s: %w", t.name, err)
		}
	}

	if err := importKVScope(tx, "modelAliases", doc.ModelAliases); err != nil {
		return fmt.Errorf("import modelAliases: %w", err)
	}
	if err := importKVScope(tx, "customModels", doc.CustomModels); err != nil {
		return fmt.Errorf("import customModels: %w", err)
	}

	return tx.Commit()
}

func importTable(tx *sql.Tx, t tableSpec, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}

	// Bind only the columns the target file actually has. A backup taken from a
	// database that includes an optional column, restored into one that does
	// not, must not fail the whole import over a field it could simply ignore.
	cols := make([]string, 0, len(t.columns))
	for _, c := range t.columns {
		if !t.optional[c] {
			cols = append(cols, c)
			continue
		}
		ok, err := columnExistsTx(tx, t.name, c)
		if err != nil {
			return err
		}
		if ok {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
	q := "INSERT OR REPLACE INTO " + t.name + " (" + strings.Join(cols, ", ") + ") VALUES (" + placeholders + ")"

	stmt, err := tx.Prepare(q)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for i, rec := range rows {
		vals := make([]any, len(cols))
		for j, col := range cols {
			v, err := convertIn(rec[col], t, col)
			if err != nil {
				return fmt.Errorf("row %d column %s: %w", i, col, err)
			}
			vals[j] = v
		}
		if _, err := stmt.Exec(vals...); err != nil {
			return fmt.Errorf("row %d: %w", i, err)
		}
	}
	return nil
}

// tableExistsTx is tableExists against an open transaction.
func tableExistsTx(tx *sql.Tx, name string) (bool, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0, err
}

// columnExistsTx is columnExists against an open transaction, so the check sees
// the same schema the writes will.
func columnExistsTx(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   sql.NullString
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// convertIn returns the value to bind for a column, re-encoding JSON text and
// turning booleans back into 0/1.
func convertIn(v any, t tableSpec, col string) (any, error) {
	if v == nil {
		return nil, nil
	}
	if t.jsonFields[col] {
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return string(encoded), nil
	}
	if b, ok := v.(bool); ok {
		if b {
			return 1, nil
		}
		return 0, nil
	}
	return v, nil
}

func importKVScope(tx *sql.Tx, scope string, entries []map[string]any) error {
	if len(entries) == 0 {
		return nil
	}
	for i, e := range entries {
		key, _ := e["key"].(string)
		if key == "" {
			return fmt.Errorf("row %d: missing key", i)
		}
		encoded, err := json.Marshal(e["value"])
		if err != nil {
			return fmt.Errorf("row %d: %w", i, err)
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO kv(scope, key, value) VALUES(?, ?, ?)`,
			scope, key, string(encoded)); err != nil {
			return err
		}
	}
	return nil
}

// Summary counts what a document contains, for display before an import is
// allowed to overwrite anything.
type Summary struct {
	ProviderConnections int `json:"providerConnections"`
	ProviderNodes       int `json:"providerNodes"`
	ProxyPools          int `json:"proxyPools"`
	ApiKeys             int `json:"apiKeys"`
	Combos              int `json:"combos"`
	CustomModels        int `json:"customModels"`
}

// Summarize describes a document without touching the database.
func Summarize(doc *Document) Summary {
	if doc == nil {
		return Summary{}
	}
	return Summary{
		ProviderConnections: len(doc.ProviderConnections),
		ProviderNodes:       len(doc.ProviderNodes),
		ProxyPools:          len(doc.ProxyPools),
		ApiKeys:             len(doc.ApiKeys),
		Combos:              len(doc.Combos),
		CustomModels:        len(doc.CustomModels),
	}
}
