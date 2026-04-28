package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateMigrationFilesRejectsGap(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "001_initial.sql")
	mustWrite(t, dir, "003_gap.sql")

	report, err := ValidateMigrationFiles(dir)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if report == nil || report.Valid {
		t.Fatal("expected invalid migration report")
	}
}

func TestValidateMigrationFilesAcceptsSequentialFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "001_initial.sql")
	mustWrite(t, dir, "002_more.sql")

	report, err := ValidateMigrationFiles(dir)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if report == nil || !report.Valid {
		t.Fatal("expected valid migration report")
	}
}

func mustWrite(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("-- test\n"), 0o600); err != nil {
		t.Fatalf("write migration: %v", err)
	}
}
