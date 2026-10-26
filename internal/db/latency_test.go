package db

import "testing"

func insertLatencyConn(t *testing.T, repo *Repo, latencyMs int64) {
	t.Helper()
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt, latencyMs)
		 VALUES ('conn-1', 'openai', 'api_key', 'a', 1, '{}', datetime('now'), datetime('now'), ?)`, latencyMs,
	); err != nil {
		t.Fatalf("insert connection: %v", err)
	}
}

// RecordConnectionLatency must both persist and average: one slow request moves
// the stored value without dominating it, and a fresh account starts from the
// first sample instead of averaging against zero.
func TestRecordConnectionLatencyAverages(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)
	insertLatencyConn(t, repo, 0)

	if err := repo.RecordConnectionLatency("conn-1", 400); err != nil {
		t.Fatalf("record first sample: %v", err)
	}
	if err := repo.RecordConnectionLatency("conn-1", 800); err != nil {
		t.Fatalf("record second sample: %v", err)
	}

	conns, err := repo.GetProviderConnections("openai", true)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("got %d connections, want 1", len(conns))
	}
	// (400*3 + 800) / 4 = 500
	if conns[0].LatencyMs != 500 {
		t.Fatalf("latencyMs = %d, want 500 (EWMA 3:1)", conns[0].LatencyMs)
	}
}

// Zero samples are noise (cancelled requests) and must not drag the average
// toward zero, and must not overwrite an existing measurement.
func TestRecordConnectionLatencyIgnoresEmptySamples(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)
	insertLatencyConn(t, repo, 300)

	if err := repo.RecordConnectionLatency("conn-1", 0); err != nil {
		t.Fatalf("record zero sample: %v", err)
	}
	conns, err := repo.GetProviderConnections("openai", true)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if conns[0].LatencyMs != 300 {
		t.Fatalf("latencyMs = %d, want 300 unchanged", conns[0].LatencyMs)
	}
}
