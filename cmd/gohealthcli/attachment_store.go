package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// attachmentStore is the Data Point Attachment Store from PRD #93 and
// ADR-0009. Binary payloads (today: TCX route bytes) live in
// content-addressed sidecar files next to the SQLite archive, and the
// data_point_attachments table indexes them by sha256. The store keeps
// the bytes outside SQLite so a multi-megabyte TCX doesn't bloat the
// archive file or its backup footprint.
type attachmentStore struct {
	db          *sql.DB
	archivePath string
	rootDir     string
}

type attachmentRecord struct {
	ID            int64
	DataPointID   int64
	Kind          string
	SHA256        string
	PathRelative  string
	AbsolutePath  string
	ByteSize      int64
	FetchedAt     string
}

func attachmentRootDirForArchive(archivePath string) string {
	return archivePath + ".attachments"
}

// inspectAttachmentRoot is the doctor probe: verify the attachment
// root exists and (on POSIX) is owner-only. Returns the path + the
// observed octal mode, or an error if either fails.
func inspectAttachmentRoot(archivePath string) (string, string, error) {
	root := attachmentRootDirForArchive(archivePath)
	info, err := os.Stat(root)
	if err != nil {
		return root, "", fmt.Errorf("attachment root %s missing: %w", root, err)
	}
	if !info.IsDir() {
		return root, "", fmt.Errorf("attachment root %s is not a directory", root)
	}
	mode := fmt.Sprintf("%04o", info.Mode().Perm())
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o700 {
		return root, mode, fmt.Errorf("attachment root %s mode %s, want 0700", root, mode)
	}
	return root, mode, nil
}

func openAttachmentStore(archivePath string) (*attachmentStore, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(writeArchive)
	if err != nil {
		return nil, err
	}
	root := attachmentRootDirForArchive(archivePath)
	if err := ensureOwnerOnlyDir(root); err != nil {
		_ = handle.db.Close()
		return nil, fmt.Errorf("ensure attachment root: %w", err)
	}
	return &attachmentStore{db: handle.db, archivePath: archivePath, rootDir: root}, nil
}

func (store *attachmentStore) Close() error {
	return store.db.Close()
}

// Store writes the bytes to a content-addressed sidecar file and
// inserts a row in data_point_attachments. Idempotent: if a row with
// the same (data_point_id, sha256) already exists, the existing row +
// file are returned unchanged.
func (store *attachmentStore) Store(dataPointID int64, kind string, payload []byte, fetchedAt string) (attachmentRecord, error) {
	if kind == "" {
		return attachmentRecord{}, errors.New("attachment kind must not be empty")
	}
	hash := sha256.Sum256(payload)
	hashHex := hex.EncodeToString(hash[:])
	ext := attachmentFileExtension(kind)
	subdir := filepath.Join(store.rootDir, kind, hashHex[:2])
	pathRelative := filepath.Join(kind, hashHex[:2], hashHex+ext)
	absolutePath := filepath.Join(store.rootDir, pathRelative)

	if existing, found, err := store.findExisting(dataPointID, hashHex); err != nil {
		return attachmentRecord{}, err
	} else if found {
		existing.AbsolutePath = filepath.Join(store.rootDir, existing.PathRelative)
		// Re-write the sidecar bytes only if the file is missing — a
		// previous Store could have left the row without the file (e.g.,
		// disk corruption); the byte content is content-addressed so
		// re-write is safe.
		if _, statErr := os.Stat(existing.AbsolutePath); errors.Is(statErr, os.ErrNotExist) {
			if err := writeSidecarFile(subdir, existing.AbsolutePath, payload); err != nil {
				return attachmentRecord{}, err
			}
		}
		return existing, nil
	}

	if err := writeSidecarFile(subdir, absolutePath, payload); err != nil {
		return attachmentRecord{}, err
	}

	result, err := store.db.Exec(`INSERT INTO data_point_attachments (
		data_point_id, kind, sha256, path_relative, byte_size, fetched_at
	) VALUES (?, ?, ?, ?, ?, ?)`,
		dataPointID, kind, hashHex, pathRelative, int64(len(payload)), fetchedAt)
	if err != nil {
		return attachmentRecord{}, fmt.Errorf("insert data_point_attachment: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return attachmentRecord{}, err
	}
	return attachmentRecord{
		ID:           id,
		DataPointID:  dataPointID,
		Kind:         kind,
		SHA256:       hashHex,
		PathRelative: pathRelative,
		AbsolutePath: absolutePath,
		ByteSize:     int64(len(payload)),
		FetchedAt:    fetchedAt,
	}, nil
}

// Resolve returns the absolute path for a given sha256. Returns a
// clear error when the hash isn't in data_point_attachments, so doctor
// can format a useful diagnostic.
func (store *attachmentStore) Resolve(hashHex string) (string, error) {
	var pathRelative string
	err := store.db.QueryRow(`SELECT path_relative FROM data_point_attachments WHERE sha256 = ? LIMIT 1`, hashHex).Scan(&pathRelative)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("attachment %s not found", hashHex)
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(store.rootDir, pathRelative), nil
}

func (store *attachmentStore) findExisting(dataPointID int64, hashHex string) (attachmentRecord, bool, error) {
	var record attachmentRecord
	err := store.db.QueryRow(`SELECT id, data_point_id, kind, sha256, path_relative, byte_size, fetched_at
		FROM data_point_attachments
		WHERE data_point_id = ? AND sha256 = ? LIMIT 1`, dataPointID, hashHex).Scan(
		&record.ID, &record.DataPointID, &record.Kind, &record.SHA256,
		&record.PathRelative, &record.ByteSize, &record.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return attachmentRecord{}, false, nil
	}
	if err != nil {
		return attachmentRecord{}, false, err
	}
	return record, true, nil
}

// attachmentOrphanKind discriminates the two integrity-violation modes
// Walk reports: a row whose sidecar file is missing, or a sidecar file
// whose row is missing.
type attachmentOrphanKind string

const (
	attachmentOrphanRowMissingFile attachmentOrphanKind = "row_missing_file"
	attachmentOrphanFileMissingRow attachmentOrphanKind = "file_missing_row"
)

// attachmentOrphan is one finding from Walk. SHA256 is populated for
// row-side orphans; AbsolutePath is populated for file-side orphans.
type attachmentOrphan struct {
	Kind         attachmentOrphanKind
	SHA256       string
	AbsolutePath string
	PathRelative string
	DataPointID  int64
}

// Walk yields every integrity violation in the attachment store: rows
// whose sidecar file is missing, and sidecar files whose row is
// missing. v1 does not prune; the seam exists so doctor (#108) can
// surface diagnostics without re-deriving the path layout.
func (store *attachmentStore) Walk(fn func(attachmentOrphan) error) error {
	knownPaths := map[string]struct{}{}

	// Row-side: read every attachment row, check the sidecar exists.
	rows, err := store.db.Query(`SELECT data_point_id, sha256, path_relative FROM data_point_attachments`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var dataPointID int64
		var sha, pathRelative string
		if err := rows.Scan(&dataPointID, &sha, &pathRelative); err != nil {
			rows.Close()
			return err
		}
		abs := filepath.Join(store.rootDir, pathRelative)
		knownPaths[abs] = struct{}{}
		if _, statErr := os.Stat(abs); errors.Is(statErr, os.ErrNotExist) {
			if cbErr := fn(attachmentOrphan{
				Kind:         attachmentOrphanRowMissingFile,
				SHA256:       sha,
				AbsolutePath: abs,
				PathRelative: pathRelative,
				DataPointID:  dataPointID,
			}); cbErr != nil {
				rows.Close()
				return cbErr
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// File-side: walk the sidecar tree, flag any file that wasn't claimed by a row.
	if _, err := os.Stat(store.rootDir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.Walk(store.rootDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if _, ok := knownPaths[path]; ok {
			return nil
		}
		return fn(attachmentOrphan{
			Kind:         attachmentOrphanFileMissingRow,
			AbsolutePath: path,
		})
	})
}

func attachmentFileExtension(kind string) string {
	switch kind {
	case "tcx":
		return ".tcx"
	default:
		return ".bin"
	}
}

func writeSidecarFile(subdir, absolutePath string, payload []byte) error {
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		return fmt.Errorf("create attachment subdir: %w", err)
	}
	if usesPOSIXPermissions() {
		if err := os.Chmod(subdir, 0o700); err != nil {
			return fmt.Errorf("chmod attachment subdir: %w", err)
		}
	}
	if err := os.WriteFile(absolutePath, payload, 0o600); err != nil {
		return fmt.Errorf("write sidecar: %w", err)
	}
	if usesPOSIXPermissions() {
		if err := os.Chmod(absolutePath, 0o600); err != nil {
			return fmt.Errorf("chmod sidecar: %w", err)
		}
	}
	return nil
}
