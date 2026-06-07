package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthArchiveLifecycleOpensReadOnlyAndWriteHandlesThroughOnePath(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	createLegacyV4Archive(t, archivePath)

	lifecycle := healthArchiveLifecycle{path: archivePath}
	readOnly, err := lifecycle.Open(readOnlyArchive)
	if err != nil {
		t.Fatalf("open read-only Health Archive: %v", err)
	}
	defer readOnly.Close()
	if readOnly.schemaVersion != currentSchemaVersion {
		t.Fatalf("read-only schema version = %d, want %d", readOnly.schemaVersion, currentSchemaVersion)
	}
	if _, err := readOnly.db.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (99, 'readonly_probe', '2026-06-07T00:00:00Z')`); err == nil {
		t.Fatal("read-only write error = nil, want SQLite read-only failure")
	}

	write, err := lifecycle.Open(writeArchive)
	if err != nil {
		t.Fatalf("open writable Health Archive: %v", err)
	}
	defer write.Close()
	if write.schemaVersion != currentSchemaVersion {
		t.Fatalf("write schema version = %d, want %d", write.schemaVersion, currentSchemaVersion)
	}
	if _, err := write.db.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (99, 'write_probe', '2026-06-07T00:00:00Z')`); err != nil {
		t.Fatalf("writable insert: %v", err)
	}
}

func TestHealthArchiveLifecycleReportsInspectedSchemaVersion(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	createLegacyV4Archive(t, archivePath)
	setArchiveUserVersion(t, archivePath, currentSchemaVersion+1)

	archive, err := (healthArchiveLifecycle{path: archivePath}).Inspect(false)
	if err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("inspect error = %v, want schema version failure", err)
	}
	if archive.schemaVersion != currentSchemaVersion+1 {
		t.Fatalf("inspected schema version = %d, want %d", archive.schemaVersion, currentSchemaVersion+1)
	}
}
