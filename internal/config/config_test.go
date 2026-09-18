package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	// Clean env values we'll test to ensure test predictability
	origPort := os.Getenv("PORT")
	origDataDir := os.Getenv("DATA_DIR")
	origJwtSecret := os.Getenv("JWT_SECRET")
	origInitialPassword := os.Getenv("INITIAL_PASSWORD")
	origApiKeySecret := os.Getenv("API_KEY_SECRET")
	origMachineIDSalt := os.Getenv("MACHINE_ID_SALT")

	defer func() {
		os.Setenv("PORT", origPort)
		os.Setenv("DATA_DIR", origDataDir)
		os.Setenv("JWT_SECRET", origJwtSecret)
		os.Setenv("INITIAL_PASSWORD", origInitialPassword)
		os.Setenv("API_KEY_SECRET", origApiKeySecret)
		os.Setenv("MACHINE_ID_SALT", origMachineIDSalt)
	}()

	// Create temp directory for DATA_DIR testing
	tempDir, err := os.MkdirTemp("", "test_config_dir_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set test environment variables
	os.Setenv("PORT", "20129")
	os.Setenv("DATA_DIR", tempDir)
	os.Setenv("JWT_SECRET", "test-secret-value-123456")
	os.Setenv("INITIAL_PASSWORD", "custom-password")
	os.Setenv("API_KEY_SECRET", "custom-api-key-secret")
	os.Setenv("MACHINE_ID_SALT", "custom-salt")

	cfg := LoadConfig()

	if cfg.Port != 20129 {
		t.Errorf("expected port 20129, got %d", cfg.Port)
	}
	expectedDbPath := filepath.Join(tempDir, "db", "data.sqlite")
	if cfg.DatabasePath != expectedDbPath {
		t.Errorf("expected db path %s, got %s", expectedDbPath, cfg.DatabasePath)
	}
	if cfg.JWTSecret != "test-secret-value-123456" {
		t.Errorf("expected jwt secret, got %s", cfg.JWTSecret)
	}
	if cfg.InitialPassword != "custom-password" {
		t.Errorf("expected custom-password, got %s", cfg.InitialPassword)
	}
	if cfg.APIKeySecret != "custom-api-key-secret" {
		t.Errorf("expected custom-api-key-secret, got %s", cfg.APIKeySecret)
	}
	if cfg.MachineIDSalt != "custom-salt" {
		t.Errorf("expected custom-salt, got %s", cfg.MachineIDSalt)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	// Clean env values we'll test to ensure test predictability
	origPort := os.Getenv("PORT")
	origDataDir := os.Getenv("DATA_DIR")
	origJwtSecret := os.Getenv("JWT_SECRET")
	origInitialPassword := os.Getenv("INITIAL_PASSWORD")
	origApiKeySecret := os.Getenv("API_KEY_SECRET")
	origMachineIDSalt := os.Getenv("MACHINE_ID_SALT")

	defer func() {
		os.Setenv("PORT", origPort)
		os.Setenv("DATA_DIR", origDataDir)
		os.Setenv("JWT_SECRET", origJwtSecret)
		os.Setenv("INITIAL_PASSWORD", origInitialPassword)
		os.Setenv("API_KEY_SECRET", origApiKeySecret)
		os.Setenv("MACHINE_ID_SALT", origMachineIDSalt)
	}()

	// Clear out environment to test defaults
	os.Setenv("PORT", "")
	os.Setenv("JWT_SECRET", "")
	os.Setenv("INITIAL_PASSWORD", "")
	os.Setenv("API_KEY_SECRET", "")
	os.Setenv("MACHINE_ID_SALT", "")

	// Create temp directory for DATA_DIR testing
	tempDir, err := os.MkdirTemp("", "test_config_defaults_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	os.Setenv("DATA_DIR", tempDir)

	cfg := LoadConfig()

	if cfg.Port != 20128 { // Default port
		t.Errorf("expected default port 20128, got %d", cfg.Port)
	}
	if cfg.InitialPassword != "" {
		t.Errorf("expected no default password (operator must set INITIAL_PASSWORD), got %s", cfg.InitialPassword)
	}
	if cfg.APIKeySecret != "endpoint-proxy-api-key-secret" {
		t.Errorf("expected default api-key-secret, got %s", cfg.APIKeySecret)
	}
	if cfg.MachineIDSalt != "endpoint-proxy-salt" {
		t.Errorf("expected default salt, got %s", cfg.MachineIDSalt)
	}

	// Verify JWT secret is auto-generated and saved to file
	jwtSecretFile := filepath.Join(tempDir, "jwt-secret")
	if _, err := os.Stat(jwtSecretFile); os.IsNotExist(err) {
		t.Error("expected jwt-secret file to be created")
	}

	// Loading again should read the saved secret
	cfg2 := LoadConfig()
	if cfg2.JWTSecret != cfg.JWTSecret {
		t.Errorf("expected second load to return same jwt secret %s, got %s", cfg.JWTSecret, cfg2.JWTSecret)
	}
}

func TestLoadConfigInvalidPort(t *testing.T) {
	origPort := os.Getenv("PORT")
	defer os.Setenv("PORT", origPort)

	os.Setenv("PORT", "abc") // invalid number
	cfg := LoadConfig()
	if cfg.Port != 20128 {
		t.Errorf("expected fallback port 20128 for invalid port, got %d", cfg.Port)
	}

	os.Setenv("PORT", "-1") // negative port
	cfg2 := LoadConfig()
	if cfg2.Port != 20128 {
		t.Errorf("expected fallback port 20128 for negative port, got %d", cfg2.Port)
	}
}

// TestDBPathDefaultFollowsDataDir pins the contract the dashboard relies on:
// with no DB_PATH set, the database resolves to DATA_DIR/db/data.sqlite — the
// same file the Node.js dashboard writes. A regression here makes an existing
// installation start with an empty database and look like data loss.
func TestDBPathDefaultFollowsDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("DB_PATH", "")

	cfg := LoadConfig()
	want := filepath.Join(dir, "db", "data.sqlite")
	if cfg.DatabasePath != want {
		t.Fatalf("DatabasePath = %q, want %q", cfg.DatabasePath, want)
	}
}

// TestDBPathDirectoryWithCurrentLayout checks that pointing DB_PATH at a data
// directory finds the database under db/ rather than creating a new one.
func TestDBPathDirectoryWithCurrentLayout(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "db")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(nested, "data.sqlite")
	if err := os.WriteFile(existing, []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DB_PATH", dir)
	cfg := LoadConfig()

	if cfg.DatabasePath != existing {
		t.Fatalf("DatabasePath = %q, want %q", cfg.DatabasePath, existing)
	}
}

// TestDBPathDirectoryWithFlatLayout covers the older flat layout where the
// database sits directly in the directory.
func TestDBPathDirectoryWithFlatLayout(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "data.sqlite")
	if err := os.WriteFile(existing, []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DB_PATH", dir)
	cfg := LoadConfig()

	if cfg.DatabasePath != existing {
		t.Fatalf("DatabasePath = %q, want %q", cfg.DatabasePath, existing)
	}
}

// TestDBPathDirectoryWithoutDatabase ensures a directory holding no database
// still yields a usable path, with the file created on first use.
func TestDBPathDirectoryWithoutDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DB_PATH", dir)

	cfg := LoadConfig()
	want := filepath.Join(dir, "data.sqlite")
	if cfg.DatabasePath != want {
		t.Fatalf("DatabasePath = %q, want %q", cfg.DatabasePath, want)
	}
}

// TestFindDatabaseInDirPrefersCurrentLayout guards the search order. An
// installation can hold both layouts; the current one must win so an upgrade
// does not silently fall back to a stale file.
func TestFindDatabaseInDirPrefersCurrentLayout(t *testing.T) {
	dir := t.TempDir()

	flat := filepath.Join(dir, "data.sqlite")
	if err := os.WriteFile(flat, []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "db")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(nested, "data.sqlite")
	if err := os.WriteFile(current, []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok := findDatabaseInDir(dir)
	if !ok {
		t.Fatal("expected to find a database")
	}
	if got != current {
		t.Fatalf("findDatabaseInDir = %q, want the current layout %q", got, current)
	}
}

// TestFindDatabaseInDirIgnoresDirectories makes sure a directory named like a
// database file is not mistaken for one.
func TestFindDatabaseInDirIgnoresDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data.sqlite"), 0o700); err != nil {
		t.Fatal(err)
	}

	if got, ok := findDatabaseInDir(dir); ok {
		t.Fatalf("findDatabaseInDir = %q, want no match", got)
	}
}
