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
	"bytes"
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
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

	// Key/value scopes. VansRouter stores these two scopes as an object keyed by
	// alias, while this build stores a list of {key, value} rows. KVScopeRows
	// accepts either so a backup made by the Next.js dashboard can be restored
	// here, which is the whole point of keeping the file shape close to the
	// reference in the first place.
	ModelAliases KVScopeRows `json:"modelAliases"`
	CustomModels KVScopeRows `json:"customModels"`
}

// KVScopeRows is a list of kv rows that also decodes the reference dashboard's
// object form ({"<key>": <value>}).
//
// Ignoring the difference would be worse than it looks: VansRouter exports an
// empty modelAlaises as {}, an object, and a struct expecting a list rejects the
// whole file with an unmarshal error before any of the connections it came for
// are even considered.
type KVScopeRows []map[string]any

// kvKeyForModelRow builds this build's kv key for a bare custom-model object.
//
// VansRouter exports customModels as a list of the model objects themselves,
// while this build keys each one as "<providerAlias>|<id>|<type>" and stores the
// object as its value. The key is derivable from the object, so a reference file
// needs no rewriting -- it just needs the key built before the insert.
func kvKeyForModelRow(row map[string]any) (string, bool) {
	alias, _ := row["providerAlias"].(string)
	id, _ := row["id"].(string)
	kind, _ := row["type"].(string)
	if alias == "" || id == "" || kind == "" {
		return "", false
	}
	return alias + "|" + id + "|" + kind, true
}

// UnmarshalJSON accepts a list of {key, value} rows, or an object whose members
// become those rows.
func (k *KVScopeRows) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*k = nil
		return nil
	}
	switch trimmed[0] {
	case '[':
		var rows []map[string]any
		if err := json.Unmarshal(data, &rows); err != nil {
			return err
		}
		// A list is either our own [{key, value}] rows or the reference's bare
		// list of custom-model objects. Tell them apart by whether a key is
		// present, and derive one for the bare form so both import identically.
		for i, row := range rows {
			if key, _ := row["key"].(string); key != "" {
				continue
			}
			derived, ok := kvKeyForModelRow(row)
			if !ok {
				return fmt.Errorf("row %d: entry has neither a key nor a providerAlias/id/type to derive one from", i)
			}
			rows[i] = map[string]any{"key": derived, "value": row}
		}
		*k = rows
		return nil
	case '{':
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			return err
		}
		// Sort the keys so a round trip is deterministic; map order is random
		// and an unstable file would make every diff look like a change.
		keys := make([]string, 0, len(obj))
		for key := range obj {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		rows := make([]map[string]any, 0, len(keys))
		for _, key := range keys {
			rows = append(rows, map[string]any{"key": key, "value": obj[key]})
		}
		*k = rows
		return nil
	default:
		return fmt.Errorf("expected a list or an object of key/value rows, got %s", trimmed[:1])
	}
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
	// normalize adapts a row from another dashboard's file into this one's
	// shape. It runs before the row is written, and may return the row
	// unchanged. It exists because "the same table" does not mean "the same
	// layout": VansRouter keeps a connection's apiKey and model locks as flat
	// columns on the row, while this build keeps them inside a JSON data column.
	normalize func(row map[string]any) (map[string]any, error)
}

var tables = []tableSpec{
	{
		name:       "providerConnections",
		columns:    []string{"id", "provider", "authType", "name", "email", "priority", "isActive", "data", "createdAt", "updatedAt"},
		jsonFields: map[string]bool{"data": true},
		boolFields: map[string]bool{"isActive": true},
		target:     func(d *Document) *[]map[string]any { return &d.ProviderConnections },
		normalize:  foldRow("providerConnections"),
	},
	{
		name:       "providerNodes",
		columns:    []string{"id", "type", "name", "data", "createdAt", "updatedAt"},
		jsonFields: map[string]bool{"data": true},
		target:     func(d *Document) *[]map[string]any { return &d.ProviderNodes },
		normalize:  foldRow("providerNodes"),
	},
	{
		name:       "proxyPools",
		columns:    []string{"id", "isActive", "testStatus", "data", "createdAt", "updatedAt"},
		jsonFields: map[string]bool{"data": true},
		boolFields: map[string]bool{"isActive": true},
		target:     func(d *Document) *[]map[string]any { return &d.ProxyPools },
		normalize:  foldRow("proxyPools"),
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
	// Validate the password hash before anything is written. A corrupt or
	// hand-edited value would restore cleanly and then lock the operator out of
	// the dashboard, with no way back in except editing the database by hand.
	if err := ValidatePasswordHash(doc.Settings); err != nil {
		return err
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

// flatColumnsByTable names, per table, the columns that are read straight off a
// row. Anything else in the row belongs to that row's JSON data payload.
//
// Both dashboards share these tables but split them differently: here the row
// holds identity, ordering and status, and the provider-specific remainder
// (apiKey, oauth tokens, baseUrl, prefix, modelLock_<model> flags, ...) lives in
// a JSON `data` column. VansRouter keeps those values as flat columns and writes
// no `data` at all. Folding the flat form into `data` is what makes a reference
// backup restorable here instead of failing on the first row with
// "NOT NULL constraint failed: <table>.data".
var flatColumnsByTable = map[string]map[string]bool{
	"providerConnections": setOf(
		"id", "provider", "authType", "name", "email",
		"priority", "isActive", "createdAt", "updatedAt"),
	"providerNodes": setOf(
		"id", "type", "name", "createdAt", "updatedAt"),
	"proxyPools": setOf(
		"id", "isActive", "testStatus", "createdAt", "updatedAt"),
}

func setOf(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// foldFlatRow rewrites a row so its non-column fields live in `data`.
//
// A row that already carries a data object is in our own layout and is returned
// unchanged, so our own exports round-trip byte for byte. id is NOT NULL in all
// three tables, so a row without one is reported by name rather than as an
// opaque constraint failure.
// foldRow binds a table's normalizer so it can be attached to a tableSpec.
func foldRow(table string) func(map[string]any) (map[string]any, error) {
	return func(row map[string]any) (map[string]any, error) {
		return foldFlatRow(table, row)
	}
}

func foldFlatRow(table string, row map[string]any) (map[string]any, error) {
	if row == nil {
		return row, nil
	}
	if existing, ok := row["data"]; ok && existing != nil {
		if _, isMap := existing.(map[string]any); isMap {
			return row, nil
		}
	}
	flat := flatColumnsByTable[table]
	if flat == nil {
		return row, nil
	}

	if id, _ := row["id"].(string); strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%s row has no id", table)
	}

	data := map[string]any{}
	out := make(map[string]any, len(row)+1)
	for k, v := range row {
		switch {
		case k == "data":
			// Replaced below with the collected payload.
		case flat[k]:
			out[k] = v
		default:
			data[k] = v
		}
	}
	out["data"] = data
	return out, nil
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
		if t.normalize != nil {
			normalized, err := t.normalize(rec)
			if err != nil {
				return fmt.Errorf("row %d: %w", i, err)
			}
			rec = normalized
		}
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
// notNullDefault supplies a value for a NOT NULL column the file omitted.
//
// A backup is not obliged to carry every column: VansRouter's own rows happen to
// set createdAt/updatedAt, but nothing promises it, and a hand-trimmed file or a
// future format may not. Inserting NULL would abort the whole restore on a
// constraint the operator cannot act on, so a sensible default is used instead.
func notNullDefault(col string) (any, bool) {
	switch col {
	case "createdAt", "updatedAt":
		return time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), true
	case "data":
		// Every table's data column is a JSON object; "{}" preserves the row
		// without inventing fields the file never had.
		return "{}", true
	}
	return nil, false
}

func convertIn(v any, t tableSpec, col string) (any, error) {
	if v == nil {
		if def, ok := notNullDefault(col); ok {
			return def, nil
		}
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

// validatePasswordHash rejects a settings.password that is not a well-formed
// bcrypt hash.
//
// An absent or empty hash is allowed: that is how a dashboard with no password
// is represented, and restoring such a backup intentionally returns the router
// to the default-password state.
func ValidatePasswordHash(settings map[string]any) error {
	if settings == nil {
		return nil
	}
	raw, present := settings["password"]
	if !present || raw == nil {
		return nil
	}
	hash, ok := raw.(string)
	if !ok {
		return fmt.Errorf("backup settings.password is %T, want a string", raw)
	}
	if hash == "" {
		return nil
	}
	if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		return fmt.Errorf("backup settings.password is not a valid bcrypt hash: %w", err)
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
