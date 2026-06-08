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

type identitySnapshotRecord struct {
	ID        int64
	Kind      string
	RawJSON   string
	FetchedAt string
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

func (archive *identitySnapshotArchive) CurrentConnection() (archivedConnection, error) {
	connection, err := readCurrentConnection(archive.db)
	if errors.Is(err, sql.ErrNoRows) {
		return archivedConnection{}, errors.New("no Connection found; run `gohealthcli connect` first")
	}
	return connection, err
}

// Insert appends a kind-tagged snapshot. kind is validated by call sites
// (CONTEXT.md term: profile | settings | paired-devices | irn-profile);
// the archive only enforces non-empty so an invalid CLI surface can't
// silently produce kind-empty rows.
func (archive *identitySnapshotArchive) Insert(connection archivedConnection, kind, rawJSON, fetchedAt string) (int64, error) {
	if kind == "" {
		return 0, errors.New("Identity Snapshot kind must not be empty")
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

// Latest returns the most recent snapshot of the given kind. The found
// boolean distinguishes "no row at all" from "row with empty payload";
// since the column is TEXT NOT NULL with no default empty, an empty
// snapshot is a real (degenerate) state worth surfacing.
func (archive *identitySnapshotArchive) Latest(connection archivedConnection, kind string) (identitySnapshotRecord, bool, error) {
	var record identitySnapshotRecord
	err := archive.db.QueryRow(`SELECT id, snapshot_kind, raw_json, fetched_at
		FROM identity_snapshots
		WHERE connection_id = ? AND snapshot_kind = ?
		ORDER BY id DESC
		LIMIT 1`, connection.id, kind).Scan(&record.ID, &record.Kind, &record.RawJSON, &record.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return identitySnapshotRecord{}, false, nil
	}
	if err != nil {
		return identitySnapshotRecord{}, false, err
	}
	return record, true, nil
}
