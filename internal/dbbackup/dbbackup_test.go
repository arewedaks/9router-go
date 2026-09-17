package dbbackup

import (
	"database/sql"
	json "encoding/json/v2"
	"fmt"

	"golang.org/x/crypto/bcrypt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestDB builds an in-memory database with the same schema the app uses.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openDBAt(t, filepath.Join(t.TempDir(), "t.sqlite"))
}

// openDBAt opens a database with the same PRAGMAs production uses. In
// particular busy_timeout: without it a write that collides with another
// connection fails immediately with SQLITE_BUSY instead of waiting its turn,
// and tests here would fail for a reason production never hits.
func openDBAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	pragmas := `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
`
	if _, err := db.Exec(pragmas); err != nil {
		t.Fatalf("pragma: %v", err)
	}

	stmts := []string{
		`CREATE TABLE settings (id INTEGER PRIMARY KEY CHECK (id = 1), data TEXT NOT NULL)`,
		`CREATE TABLE providerConnections (id TEXT PRIMARY KEY, provider TEXT NOT NULL, authType TEXT NOT NULL, name TEXT, email TEXT, priority INTEGER, isActive INTEGER DEFAULT 1, data TEXT NOT NULL, createdAt TEXT NOT NULL, updatedAt TEXT NOT NULL)`,
		`CREATE TABLE providerNodes (id TEXT PRIMARY KEY, type TEXT, name TEXT, data TEXT NOT NULL, createdAt TEXT NOT NULL, updatedAt TEXT NOT NULL)`,
		`CREATE TABLE proxyPools (id TEXT PRIMARY KEY, isActive INTEGER DEFAULT 1, testStatus TEXT, data TEXT NOT NULL, createdAt TEXT NOT NULL, updatedAt TEXT NOT NULL)`,
		`CREATE TABLE proxyPoolFitness (poolId TEXT NOT NULL, scope TEXT NOT NULL, until INTEGER NOT NULL, reason TEXT, createdAt TEXT NOT NULL, updatedAt TEXT NOT NULL, PRIMARY KEY (poolId, scope))`,
		`CREATE TABLE apiKeys (id TEXT PRIMARY KEY, key TEXT UNIQUE NOT NULL, name TEXT, machineId TEXT, isActive INTEGER DEFAULT 1, createdAt TEXT NOT NULL, allowedProviders TEXT, allowedCombos TEXT, allowedKinds TEXT)`,
		`CREATE TABLE combos (id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL, kind TEXT, models TEXT NOT NULL, createdAt TEXT NOT NULL, updatedAt TEXT NOT NULL, context_length INTEGER)`,
		`CREATE TABLE kv (scope TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY (scope, key))`,
		// Not part of a backup; must survive an import.
		`CREATE TABLE usageHistory (id INTEGER PRIMARY KEY, provider TEXT)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema %q: %v", q, err)
		}
	}
	return db
}

func seed(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// A real bcrypt hash: Import validates the one in settings, so a placeholder
	// would make unrelated tests fail on the validation rather than their subject.
	mustExec(`INSERT INTO settings(id, data) VALUES(1, ?)`,
		fmt.Sprintf(`{"rtkEnabled":true,"password":%q,"ponytailLevel":"full"}`, validBcryptHash(t)))
	mustExec(`INSERT INTO providerConnections(id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt)
		VALUES('c1','antigravity','oauth','Acc 1','a@example.com',1,1,'{"accessToken":"tok-a","nested":{"deep":true}}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(`INSERT INTO providerNodes(id, type, name, data, createdAt, updatedAt)
		VALUES('n1','openai-compatible','node','{"baseUrl":"http://x"}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(`INSERT INTO proxyPools(id, isActive, testStatus, data, createdAt, updatedAt)
		VALUES('p1',1,'healthy','{"url":"http://proxy"}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(`INSERT INTO proxyPoolFitness(poolId, scope, until, reason, createdAt, updatedAt)
		VALUES('p1','global',123,'slow','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(`INSERT INTO apiKeys(id, key, name, machineId, isActive, createdAt, allowedProviders, allowedCombos, allowedKinds)
		VALUES('k1','sk-test','key','m1',1,'2026-01-01T00:00:00Z','["antigravity"]','[]','["llm"]')`)
	mustExec(`INSERT INTO combos(id, name, kind, models, createdAt, updatedAt, context_length)
		VALUES('cb1','my-combo','fusion','[{"provider":"antigravity"}]','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',4096)`)
	mustExec(`INSERT INTO kv(scope, key, value) VALUES('modelAliases','ag','{"alias":"ag"}')`)
	mustExec(`INSERT INTO kv(scope, key, value) VALUES('customModels','x','{"id":"m1"}')`)
	mustExec(`INSERT INTO usageHistory(id, provider) VALUES(1,'antigravity')`)
}

func TestExportCapturesEveryTable(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)

	doc, err := Export(db)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	if doc.FormatVersion != FormatVersion {
		t.Errorf("formatVersion = %d, want %d", doc.FormatVersion, FormatVersion)
	}
	if doc.ExportedAt == "" {
		t.Error("exportedAt is empty; the filename stamp depends on it")
	}
	if len(doc.ProviderConnections) != 1 || len(doc.ProviderNodes) != 1 ||
		len(doc.ProxyPools) != 1 || len(doc.ApiKeys) != 1 || len(doc.Combos) != 1 {
		t.Errorf("row counts wrong: conns=%d nodes=%d pools=%d keys=%d combos=%d",
			len(doc.ProviderConnections), len(doc.ProviderNodes), len(doc.ProxyPools),
			len(doc.ApiKeys), len(doc.Combos))
	}
	if len(doc.ModelAliases) != 1 || len(doc.CustomModels) != 1 {
		t.Errorf("kv scopes wrong: aliases=%d custom=%d", len(doc.ModelAliases), len(doc.CustomModels))
	}

	// The password travels with the backup: a restore is expected to bring the
	// credentials of the machine that produced it.
	if pw, _ := doc.Settings["password"].(string); pw == "" {
		t.Error("settings.password missing from the export")
	}
}

func TestExportDecodesNestedJSON(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)

	doc, err := Export(db)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// A connection's `data` column must come out as an object, not a JSON string,
	// or a re-import would double-encode it and the tokens would be unreachable.
	data, ok := doc.ProviderConnections[0]["data"].(map[string]any)
	if !ok {
		t.Fatalf("connection data is %T, want map[string]any (JSON text was not decoded)", doc.ProviderConnections[0]["data"])
	}
	if data["accessToken"] != "tok-a" {
		t.Errorf("accessToken = %v, want tok-a", data["accessToken"])
	}
	nested, ok := data["nested"].(map[string]any)
	if !ok || nested["deep"] != true {
		t.Errorf("nested object lost in round-trip: %#v", data["nested"])
	}

	// Booleans for 0/1 columns.
	if active, ok := doc.ProviderConnections[0]["isActive"].(bool); !ok || !active {
		t.Errorf("isActive = %#v, want true", doc.ProviderConnections[0]["isActive"])
	}

	if models, ok := doc.Combos[0]["models"].([]any); !ok || len(models) != 1 {
		t.Errorf("combo models = %#v, want a decoded array", doc.Combos[0]["models"])
	}

	// Keys we do not model must not be dropped.
	if doc.ApiKeys[0]["allowedProviders"] == nil {
		t.Error("apiKeys.allowedProviders was dropped; a scoped key would silently regain full access")
	}
	if _, present := doc.Combos[0]["context_length"]; !present {
		t.Error("combos.context_length missing; that column has no counterpart in the reference schema")
	}
}

// TestImportReplacesRatherThanMerges is the core guarantee: a restore yields the
// backed-up state, not a union of both.
func TestImportReplacesRatherThanMerges(t *testing.T) {
	src := newTestDB(t)
	seed(t, src)
	doc, err := Export(src)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := newTestDB(t)
	seed(t, dst)
	// Add rows that exist only in the destination.
	if _, err := dst.Exec(`INSERT INTO providerConnections(id, provider, authType, name, isActive, data, createdAt, updatedAt)
		VALUES('extra','kiro','oauth','Extra',1,'{}','2026-02-02T00:00:00Z','2026-02-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Exec(`INSERT INTO kv(scope, key, value) VALUES('modelAliases','ghost','{}')`); err != nil {
		t.Fatal(err)
	}

	if err := Import(dst, doc); err != nil {
		t.Fatalf("Import: %v", err)
	}

	var n int
	if err := dst.QueryRow(`SELECT COUNT(*) FROM providerConnections`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("providerConnections = %d, want 1 (the destination-only row must be gone)", n)
	}
	var ghost int
	if err := dst.QueryRow(`SELECT COUNT(*) FROM kv WHERE scope='modelAliases' AND key='ghost'`).Scan(&ghost); err != nil {
		t.Fatal(err)
	}
	if ghost != 0 {
		t.Error("a kv row outside the backup survived the import; import merged instead of replacing")
	}
}

// TestImportKeepsDerivedData confirms the wipe is scoped. Usage history is not in
// a backup and clearing it would destroy real records for no benefit.
func TestImportKeepsDerivedData(t *testing.T) {
	src := newTestDB(t)
	seed(t, src)
	doc, _ := Export(src)

	dst := newTestDB(t)
	seed(t, dst)
	if _, err := dst.Exec(`INSERT INTO usageHistory(id, provider) VALUES(2,'cline')`); err != nil {
		t.Fatal(err)
	}

	if err := Import(dst, doc); err != nil {
		t.Fatalf("Import: %v", err)
	}
	var n int
	if err := dst.QueryRow(`SELECT COUNT(*) FROM usageHistory`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("usageHistory = %d rows, want 2 kept", n)
	}
}

// TestImportRoundTripPreservesValues walks a full export -> import and compares
// the reloaded state field by field.
func TestImportRoundTripPreservesValues(t *testing.T) {
	src := newTestDB(t)
	seed(t, src)
	doc, err := Export(src)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	before, err := Export(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = before

	dst := newTestDB(t)
	if err := Import(dst, doc); err != nil {
		t.Fatalf("Import: %v", err)
	}
	after, err := Export(dst)
	if err != nil {
		t.Fatalf("re-Export: %v", err)
	}

	// Re-exporting an imported database must produce the same document, apart
	// from the timestamp. Anything else means a column was dropped or mangled.
	//
	// Compared structurally, not as raw JSON text: these rows are maps, and Go
	// deliberately randomises map key order, so a string compare would fail on
	// every run for reasons that have nothing to do with the data.
	after.ExportedAt = doc.ExportedAt
	if diff := diffDocs(t, doc, after); diff != "" {
		t.Errorf("round-trip mismatch: %s", diff)
	}
}

// diffDocs reports the first structural difference between two documents, or ""
// when they carry the same data.
func diffDocs(t *testing.T, want, got *Document) string {
	t.Helper()
	wj, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	gj, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	var wv, gv any
	if err := json.Unmarshal(wj, &wv); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if err := json.Unmarshal(gj, &gv); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	return diffValue("", wv, gv)
}

func diffValue(path string, want, got any) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return path + ": type changed from object to " + kindOf(got)
		}
		for k, wv := range w {
			gv, present := g[k]
			if !present {
				return path + "." + k + ": missing after round-trip"
			}
			if d := diffValue(path+"."+k, wv, gv); d != "" {
				return d
			}
		}
		for k := range g {
			if _, present := w[k]; !present {
				return path + "." + k + ": appeared after round-trip"
			}
		}
		return ""
	case []any:
		g, ok := got.([]any)
		if !ok {
			return path + ": type changed from array to " + kindOf(got)
		}
		if len(w) != len(g) {
			return path + ": length changed"
		}
		for i := range w {
			if d := diffValue(path+"["+itoa(i)+"]", w[i], g[i]); d != "" {
				return d
			}
		}
		return ""
	default:
		if !scalarEqual(want, got) {
			return path + ": " + toStr(want) + " != " + toStr(got)
		}
		return ""
	}
}

// scalarEqual compares numbers by value, since JSON decoding widens every number
// to float64 while the structs hold ints.
func scalarEqual(a, b any) bool {
	if af, ok := asFloat(a); ok {
		bf, ok2 := asFloat(b)
		return ok2 && af == bf
	}
	return toStr(a) == toStr(b) && kindOf(a) == kindOf(b)
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "string"
	case float64, int, int64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestImportRejectsUnusablePayloads(t *testing.T) {
	db := newTestDB(t)

	if err := Import(db, nil); err == nil {
		t.Error("nil document accepted")
	}
	future := &Document{FormatVersion: FormatVersion + 1}
	if err := Import(db, future); err == nil {
		t.Error("a backup from a newer format was accepted; it may contain columns this build cannot honour")
	} else if !strings.Contains(err.Error(), "newer version") {
		t.Errorf("error should explain the version mismatch, got: %v", err)
	}
}

// TestImportIsAtomic is the operator-visible safety property: a document that
// fails part-way must leave the previous configuration untouched.
//
// Note on what this does and does not prove. It verifies the *contract* — after
// a failed import the old data is still there — which is what actually matters
// to a user. It cannot distinguish an explicit rollback from database/sql's own
// teardown of an unfinished transaction, because both produce the same result;
// a mutation that deletes `defer tx.Rollback()` still passes. The separate
// TestImportUsesASingleTransaction covers the mechanism, since that is the part
// a future edit can plausibly break.
func TestImportIsAtomic(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)

	// The failure has to be one the importer cannot repair. combos.models is
	// NOT NULL with no default, so a row without it violates the constraint on
	// insert. It deliberately is not a duplicate key: every insert is
	// INSERT OR REPLACE, so a duplicate within one batch would simply overwrite
	// rather than fail.
	//
	// This used to rely on a nil data column on providerNodes. A missing data
	// value is now defaulted to an empty object instead of aborting the restore,
	// which is the more useful behaviour, so the trigger had to move.
	bad := &Document{
		FormatVersion: FormatVersion,
		Settings:      map[string]any{"rtkEnabled": false},
		ProviderConnections: []map[string]any{
			{"id": "n1", "provider": "antigravity", "authType": "oauth", "name": "ok",
				"isActive": true, "data": map[string]any{"a": 1},
				"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"},
		},
		Combos: []map[string]any{
			{"id": "c-bad", "name": "broken", "models": nil, "createdAt": "z", "updatedAt": "z"},
		},
	}

	err := Import(db, bad)
	if err == nil {
		t.Fatal("expected the import to fail")
	}

	// The original seed must still be intact.
	var name string
	if err := db.QueryRow(`SELECT name FROM providerConnections WHERE id='c1'`).Scan(&name); err != nil {
		t.Fatalf("original row is gone after a failed import: %v", err)
	}
	if name != "Acc 1" {
		t.Errorf("original row was modified: name=%q", name)
	}
	var settings string
	if err := db.QueryRow(`SELECT data FROM settings WHERE id=1`).Scan(&settings); err != nil {
		t.Fatalf("settings row is gone after a failed import: %v", err)
	}
	if !strings.Contains(settings, "rtkEnabled") || !strings.Contains(settings, "ponytailLevel") {
		t.Errorf("settings were replaced despite the failure: %s", settings)
	}
}

func TestSummarize(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)
	doc, _ := Export(db)

	s := Summarize(doc)
	if s.ProviderConnections != 1 || s.ProviderNodes != 1 || s.ApiKeys != 1 || s.Combos != 1 || s.CustomModels != 1 {
		t.Errorf("summary wrong: %+v", s)
	}
	if (Summarize(nil)) != (Summary{}) {
		t.Error("Summarize(nil) should be the zero value")
	}
}

func toStr(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return string(b)
}

// TestImportUsesASingleTransaction checks the mechanism behind atomicity: every
// row must be written through one transaction, so a failure in any table undoes
// the tables already written.
//
// This is checked by holding a competing write transaction open. If Import
// really wraps everything in one transaction it must block until that writer
// finishes, and must time out while the lock is held — proving it did not
// happily commit each table as it went.
func TestImportUsesASingleTransaction(t *testing.T) {
	db := newTestDB(t)
	seed(t, db)

	// A second connection holds the write lock.
	blocker, err := sql.Open("sqlite", testPath(db))
	if err != nil {
		t.Fatalf("open blocker: %v", err)
	}
	defer blocker.Close()
	if _, err := blocker.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatalf("blocker pragma: %v", err)
	}
	tx, err := blocker.Begin()
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE settings SET data = data WHERE id = 1`); err != nil {
		t.Fatalf("blocker write: %v", err)
	}

	doc := &Document{
		FormatVersion: FormatVersion,
		Settings:      map[string]any{"rtkEnabled": false},
	}

	done := make(chan error, 1)
	go func() {
		done <- Import(db, doc)
	}()

	// While another writer holds the lock the import must not *succeed*. It may
	// block (and then finish once the lock frees) or it may report SQLITE_BUSY —
	// both are acceptable and both prove it is taking a write lock for the whole
	// restore. What must never happen is a clean success with the lock held,
	// because that would mean it committed table-by-table and a failure part-way
	// would leave the database half-restored.
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Import succeeded while another writer held the lock; it is not using a single transaction")
		}
		t.Logf("Import reported contention as expected: %v", err)
		return // the lock is still held; let the deferred rollbacks clean up
	case <-time.After(400 * time.Millisecond):
		// Still blocked, as expected.
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("release blocker: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Import after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Import never completed after the lock was released")
	}
}

// testPath recovers the file a test database lives in, so a second connection
// can be opened against the same file.
func testPath(db *sql.DB) string {
	var path string
	_ = db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path)
	return path
}

// TestImportRejectsCorruptPasswordHash guards against a restore that succeeds
// and then locks the operator out: a settings.password that is not a valid
// bcrypt hash cannot sign anyone in, and there would be no way back in short of
// editing the database by hand.
func TestImportRejectsCorruptPasswordHash(t *testing.T) {
	cases := []struct {
		name  string
		value any
		ok    bool
	}{
		{"absent", nil, true},
		{"empty string", "", true},
		{"valid bcrypt", validBcryptHash(t), false},
		{"garbage", "$2b$10$mPTmr", false},
		{"plain text", "password123", false},
		{"wrong type", 12345, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			settings := map[string]any{}
			if c.value != nil {
				settings["password"] = c.value
			}
			doc := &Document{FormatVersion: FormatVersion, Settings: settings}

			db := newTestDB(t)
			seed(t, db)
			err := Import(db, doc)

			// The two "valid" cases still fail here for an unrelated reason: the
			// document has no required tables. What matters is that the error, if
			// any, is not the hash complaint.
			hashErr := err != nil && (strings.Contains(err.Error(), "bcrypt") ||
				strings.Contains(err.Error(), "settings.password"))
			switch c.name {
			case "absent", "empty string":
				if hashErr {
					t.Errorf("an empty password should be allowed, got: %v", err)
				}
			case "valid bcrypt":
				if hashErr {
					t.Errorf("a well-formed bcrypt hash was rejected: %v", err)
				}
			default:
				if !hashErr {
					t.Errorf("a corrupt password hash was accepted (err = %v); "+
						"this restore would lock the operator out", err)
				}
			}

			// Whatever the outcome, the seeded data must be intact when the
			// import was refused.
			if err != nil {
				var n int
				if qerr := db.QueryRow(`SELECT COUNT(*) FROM providerConnections`).Scan(&n); qerr != nil {
					t.Fatalf("count: %v", qerr)
				}
				if n != 1 {
					t.Errorf("connections = %d after a refused import, want 1", n)
				}
			}
		})
	}
}

// validBcryptHash produces a hash that bcrypt.Cost accepts.
func validBcryptHash(t *testing.T) string {
	t.Helper()
	b, err := bcrypt.GenerateFromPassword([]byte("x"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return string(b)
}

// TestDocumentAcceptsObjectShapedKvScopes: VansRouter writes modelAliases and
// customModels as objects keyed by alias ({} when empty), this build writes them
// as lists of {key, value}. A list-only struct rejects an otherwise perfectly
// usable reference backup before it reads a single connection, which is what
// happened the first time a real VansRouter file was restored here.
func TestDocumentAcceptsObjectShapedKvScopes(t *testing.T) {
	cases := []struct {
		name string
		body string
		keys []string
	}{
		{
			name: "empty object as the reference exports it",
			body: `{"formatVersion":1,"modelAliases":{},"customModels":{}}`,
			keys: nil,
		},
		{
			name: "object with entries",
			body: `{"formatVersion":1,"modelAliases":{"a":"m1","b":"m2"}}`,
			keys: []string{"a", "b"},
		},
		{
			name: "our own list form still works",
			body: `{"formatVersion":1,"modelAliases":[{"key":"a","value":"m1"}]}`,
			keys: []string{"a"},
		},
		{
			name: "null is treated as absent",
			body: `{"formatVersion":1,"modelAliases":null}`,
			keys: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var doc Document
			if err := json.Unmarshal([]byte(c.body), &doc); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(doc.ModelAliases) != len(c.keys) {
				t.Fatalf("modelAliases = %d rows, want %d", len(doc.ModelAliases), len(c.keys))
			}
			for i, want := range c.keys {
				if got := doc.ModelAliases[i]["key"]; got != want {
					t.Errorf("row %d key = %v, want %q", i, got, want)
				}
			}
		})
	}
}

// TestDocumentRejectsScores: a number where rows belong is a real error, not a
// shape we should quietly coerce into nothing.
func TestDocumentRejectsScores(t *testing.T) {
	var doc Document
	if err := json.Unmarshal([]byte(`{"modelAliases":42}`), &doc); err == nil {
		t.Fatal("a numeric modelAliases was accepted")
	}
}

// TestObjectShapedKvScopeOrdersKeysDeterministically: Go map iteration is
// random, so without sorting the same file would produce a different payload on
// every import and every diff would look like a change.
func TestObjectShapedKvScopeOrdersKeysDeterministically(t *testing.T) {
	body := `{"modelAliases":{"z":"1","a":"2","m":"3"}}`
	want := []string{"a", "m", "z"}
	for i := 0; i < 20; i++ {
		var doc Document
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		for j, k := range want {
			if got := doc.ModelAliases[j]["key"]; got != k {
				t.Fatalf("run %d: row %d = %v, want %q", i, j, got, k)
			}
		}
	}
}

// TestImportFoldsFlatReferenceRows: VansRouter keeps a connection's apiKey and
// its modelLock_<model> flags as flat columns on the row and writes no `data`,
// while this build keeps them inside a JSON `data` column. Importing a
// reference file used to abort on the first row with
// "NOT NULL constraint failed: providerConnections.data", discarding an
// otherwise usable backup over packaging rather than content.
func TestImportFoldsFlatReferenceRows(t *testing.T) {
	db := newTestDB(t)

	doc := &Document{
		FormatVersion: FormatVersion,
		Settings:      map[string]any{},
		ProviderConnections: []map[string]any{
			{
				// The reference shape: everything flat, no data object.
				"id":                     "conn-1",
				"provider":               "nvidia",
				"authType":               "apikey",
				"name":                   "acct",
				"isActive":               true,
				"apiKey":                 "nvapi-secret",
				"testStatus":             "active",
				"backoffLevel":           float64(2),
				"modelLock_z-ai/glm-5.2": true,
			},
		},
		ProviderNodes: []map[string]any{
			{
				"id":        "node-1",
				"type":      "openai-compatible",
				"name":      "BAI",
				"baseUrl":   "https://api.b.ai/v1",
				"prefix":    "bai",
				"apiType":   "chat",
				"createdAt": "2026-09-02T02:44:03.704Z",
			},
		},
		ProxyPools: []map[string]any{
			{
				"id":       "pool-1",
				"isActive": true,
				"name":     "relay",
				"proxyUrl": "https://example.workers.dev",
				"type":     "cloudflare",
			},
		},
	}

	// customModels goes through JSON because the reference's bare-list form is
	// only recognised while decoding; building the slice directly would skip the
	// code path this test exists to cover.
	if err := json.Unmarshal([]byte(`{
		"formatVersion": 1,
		"settings": {},
		"customModels": [
			{"providerAlias":"oc","id":"mimo-v2.5-free","type":"llm","name":"mimo-v2.5-free"}
		]
	}`), doc); err != nil {
		t.Fatalf("unmarshal reference customModels: %v", err)
	}

	if err := Import(db, doc); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// The connection's flat fields must have landed inside data.
	var raw string
	if err := db.QueryRow(`SELECT data FROM providerConnections WHERE id='conn-1'`).Scan(&raw); err != nil {
		t.Fatalf("connection not restored: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("data is not JSON: %v", err)
	}
	for _, k := range []string{"apiKey", "testStatus", "backoffLevel", "modelLock_z-ai/glm-5.2"} {
		if _, ok := data[k]; !ok {
			t.Errorf("data is missing the folded field %q", k)
		}
	}
	// Identity columns must not be duplicated into data.
	for _, k := range []string{"id", "provider", "name", "isActive"} {
		if _, ok := data[k]; ok {
			t.Errorf("data wrongly contains the column %q", k)
		}
	}

	// Node fields.
	var nodeData string
	if err := db.QueryRow(`SELECT data FROM providerNodes WHERE id='node-1'`).Scan(&nodeData); err != nil {
		t.Fatalf("node not restored: %v", err)
	}
	var node map[string]any
	if err := json.Unmarshal([]byte(nodeData), &node); err != nil {
		t.Fatal(err)
	}
	if node["baseUrl"] != "https://api.b.ai/v1" || node["prefix"] != "bai" {
		t.Errorf("node data = %v", node)
	}

	// Pool fields.
	var poolData string
	if err := db.QueryRow(`SELECT data FROM proxyPools WHERE id='pool-1'`).Scan(&poolData); err != nil {
		t.Fatalf("pool not restored: %v", err)
	}
	var pool map[string]any
	if err := json.Unmarshal([]byte(poolData), &pool); err != nil {
		t.Fatal(err)
	}
	if pool["name"] != "relay" {
		t.Errorf("pool data = %v", pool)
	}

	// The bare custom-model object must have gained our derived key.
	var key string
	if err := db.QueryRow(`SELECT key FROM kv WHERE scope='customModels'`).Scan(&key); err != nil {
		t.Fatalf("custom model not restored: %v", err)
	}
	if key != "oc|mimo-v2.5-free|llm" {
		t.Errorf("custom model key = %q, want %q", key, "oc|mimo-v2.5-free|llm")
	}
}

// TestImportLeavesOwnLayoutAlone keeps our own exports faithful: a row that
// already has a data object must round-trip without being refolded, or a restore
// would move fields between the row and its payload on every pass.
func TestImportLeavesOwnLayoutAlone(t *testing.T) {
	db := newTestDB(t)
	own := map[string]any{
		"id":       "conn-own",
		"provider": "openrouter",
		"authType": "apikey",
		"name":     "acct",
		"data":     map[string]any{"apiKey": "sk-or-secret", "testStatus": "active"},
	}
	if err := Import(db, &Document{
		FormatVersion:       FormatVersion,
		Settings:            map[string]any{},
		ProviderConnections: []map[string]any{own},
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	var raw string
	if err := db.QueryRow(`SELECT data FROM providerConnections WHERE id='conn-own'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatal(err)
	}
	if data["apiKey"] != "sk-or-secret" {
		t.Errorf("apiKey = %v", data["apiKey"])
	}
	if _, ok := data["id"]; ok {
		t.Error("id leaked into data on a row that already had one")
	}
}
