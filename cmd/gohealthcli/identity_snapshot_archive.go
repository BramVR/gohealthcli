package main

import (
	"database/sql"
	"errors"
	"fmt"
)

// identitySnapshotArchive is the dedicated read/write surface for
// kind-tagged Identity Snapshots (PRD #93 §"Identity Snapshot Archive
// lifted out of Connection API"). It exists as its own module because
// four callers — profile, settings, devices, irn-profile — write here;
// keeping the write seam inside healthArchiveConnectionAPI was the
// adapter-of-one shape that the architecture review on the PRD flagged.
//
// Reads/writes flow through the renamed identity_snapshots table; rows
// inserted via this module carry the requested snapshot_kind, while
// rows pre-dating #97 keep their migration-default kind='profile'.
type identitySnapshotArchive struct {
	db *sql.DB
}

// writeIdentitySnapshotHandoff closes the supplied Connection API (which
// holds the SQLite write handle), opens an identitySnapshotArchive
// against the same file, writes one kind-tagged snapshot, and closes
// the archive. Centralizing the close-then-open dance here keeps the
// double-close cognitive load off the profile/settings/devices/irn
// command paths.
func writeIdentitySnapshotHandoff(connectionArchive healthArchiveConnectionAPI, archivePath string, connection archivedConnection, kind, rawJSON, fetchedAt string) (int64, error) {
	if err := connectionArchive.Close(); err != nil {
		return 0, fmt.Errorf("close Connection API before identity snapshot handoff: %w", err)
	}
	snapshots, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		return 0, err
	}
	defer snapshots.Close()
	return snapshots.Insert(connection, kind, rawJSON, fetchedAt)
}

func openIdentitySnapshotArchive(archivePath string) (*identitySnapshotArchive, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(writeArchive)
	if err != nil {
		return nil, err
	}
	return &identitySnapshotArchive{db: handle.db}, nil
}

func (archive *identitySnapshotArchive) Close() error {
	return archive.db.Close()
}

// Insert appends a kind-tagged snapshot. kind is validated by call sites
// (CONTEXT.md term: profile | settings | paired-devices | irn-profile);
// the archive only enforces non-empty so an invalid CLI surface can't
// silently produce kind-empty rows.
func (archive *identitySnapshotArchive) Insert(connection archivedConnection, kind, rawJSON, fetchedAt string) (int64, error) {
	if kind == "" {
		return 0, errors.New("identity snapshot kind must not be empty")
	}
	result, err := archive.db.Exec(`INSERT INTO identity_snapshots (
		provider_name,
		connection_id,
		snapshot_kind,
		raw_json,
		fetched_at
	) VALUES (?, ?, ?, ?, ?)`, connection.providerName, connection.id, kind, rawJSON, fetchedAt)
	if err != nil {
		return 0, fmt.Errorf("insert Identity Snapshot: %w", err)
	}
	return result.LastInsertId()
}
