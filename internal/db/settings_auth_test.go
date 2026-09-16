package db

import (
	"path/filepath"
	"testing"

	"9router/proxy/internal/dbtest"
)

// newSettingsRepo opens a temporary database with the canonical schema.
func newSettingsRepo(t *testing.T) *Repo {
	t.Helper()
	database, err := OpenDatabase(filepath.Join(t.TempDir(), "settings.sqlite"))
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := dbtest.CreateTables(database); err != nil {
		t.Fatalf("CreateTables: %v", err)
	}
	return NewRepo(database)
}

func TestSettingsDefaultsRequireLoginUnset(t *testing.T) {
	repo := newSettingsRepo(t)

	s, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.PasswordHash != "" {
		t.Fatalf("PasswordHash = %q, want empty by default", s.PasswordHash)
	}
	// nil means "not configured"; the caller treats that as requireLogin = true.
	if s.RequireLogin != nil {
		t.Fatalf("RequireLogin = %v, want nil by default", *s.RequireLogin)
	}
}

func TestSetPasswordHashPersists(t *testing.T) {
	repo := newSettingsRepo(t)

	if err := repo.SetPasswordHash("$2a$10$abcdefghijklmnopqrstuv"); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}
	s, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.PasswordHash != "$2a$10$abcdefghijklmnopqrstuv" {
		t.Fatalf("PasswordHash = %q, want the stored hash", s.PasswordHash)
	}
}

func TestSetPasswordHashClearsWithEmptyString(t *testing.T) {
	repo := newSettingsRepo(t)

	if err := repo.SetPasswordHash("$2a$10$somehash"); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}
	// Clearing models "reset to default": the empty hash makes VerifyPassword
	// fall back to the built-in default password.
	if err := repo.SetPasswordHash(""); err != nil {
		t.Fatalf("SetPasswordHash(\"\"): %v", err)
	}
	s, _ := repo.GetSettings()
	if s.PasswordHash != "" {
		t.Fatalf("PasswordHash = %q, want empty after reset", s.PasswordHash)
	}
}

func TestSetRequireLoginPersistsBoolPointer(t *testing.T) {
	repo := newSettingsRepo(t)

	no := false
	if err := repo.SetRequireLogin(&no); err != nil {
		t.Fatalf("SetRequireLogin: %v", err)
	}
	s, _ := repo.GetSettings()
	if s.RequireLogin == nil || *s.RequireLogin {
		t.Fatalf("RequireLogin = %v, want false", s.RequireLogin)
	}

	yes := true
	if err := repo.SetRequireLogin(&yes); err != nil {
		t.Fatalf("SetRequireLogin: %v", err)
	}
	s, _ = repo.GetSettings()
	if s.RequireLogin == nil || !*s.RequireLogin {
		t.Fatalf("RequireLogin = %v, want true", s.RequireLogin)
	}
}

// TestSettingsRoundTripPreservesUnrelatedFields guards against a
// read-modify-write clobbering settings the auth code never touches.
func TestSettingsRoundTripPreservesUnrelatedFields(t *testing.T) {
	repo := newSettingsRepo(t)

	base, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	base.HeadroomUrl = "http://example.test:9999"
	if err := repo.saveSettings(base); err != nil {
		t.Fatalf("saveSettings: %v", err)
	}

	if err := repo.SetPasswordHash("$2a$10$hash"); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}
	after, _ := repo.GetSettings()
	if after.HeadroomUrl != "http://example.test:9999" {
		t.Fatalf("HeadroomUrl = %q, want it preserved across a password write", after.HeadroomUrl)
	}
	if after.PasswordHash != "$2a$10$hash" {
		t.Fatalf("PasswordHash = %q, want the new hash", after.PasswordHash)
	}
}
