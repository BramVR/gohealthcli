package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
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
	ID           int64
	DataPointID  int64
	Kind         string
	SHA256       string
	PathRelative string
	AbsolutePath string
	ByteSize     int64
	FetchedAt    string
}

func attachmentRootDirForArchive(archivePath string) string {
	return archivePath + ".attachments"
}

// collectAttachmentOrphans opens the attachment store read-only and
// walks it for integrity violations. Returns nil if no orphans exist
// (so the doctor result's attachments block stays omitempty), or a
// populated report otherwise. The slices inside the report are
// initialised to empty so JSON encoding is `[]` not `null` when only
// one orphan kind is present. Pure reporting — never mutates state.
func collectAttachmentOrphans(ctx context.Context, archivePath string) (*doctorAttachmentReport, error) {
	store, err := openAttachmentStoreReadOnly(archivePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	report := &doctorAttachmentReport{
		OrphanRows:  []doctorOrphanRow{},
		OrphanFiles: []doctorOrphanFile{},
	}
	if err := store.Walk(ctx, func(o attachmentOrphan) error {
		switch o.Kind {
		case attachmentOrphanRowMissingFile:
			report.OrphanRows = append(report.OrphanRows, doctorOrphanRow{
				SHA256:       o.SHA256,
				PathRelative: o.PathRelative,
				DataPointID:  o.DataPointID,
			})
		case attachmentOrphanFileMissingRow:
			report.OrphanFiles = append(report.OrphanFiles, doctorOrphanFile{
				AbsolutePath: o.AbsolutePath,
			})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(report.OrphanRows) == 0 && len(report.OrphanFiles) == 0 {
		return nil, nil
	}
	return report, nil
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

// openAttachmentStoreReadOnly opens the store without taking the
// write lock on the SQLite handle. Walk and the doctor integration
// use this so integrity checks can run against read-only copies.
func openAttachmentStoreReadOnly(archivePath string) (*attachmentStore, error) {
	return openAttachmentStoreMode(archivePath, readOnlyArchive)
}

func openAttachmentStoreMode(archivePath string, mode healthArchiveOpenMode) (*attachmentStore, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(context.Background(), mode)
	if err != nil {
		return nil, err
	}
	root := attachmentRootDirForArchive(archivePath)
	if mode == writeArchive {
		if err := ensureOwnerOnlyDir(root); err != nil {
			_ = handle.db.Close()
			return nil, fmt.Errorf("ensure attachment root: %w", err)
		}
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
func (store *attachmentStore) Store(ctx context.Context, dataPointID int64, kind string, payload []byte, fetchedAt string) (attachmentRecord, error) {
	if kind == "" {
		return attachmentRecord{}, errors.New("attachment kind must not be empty")
	}
	hash := sha256.Sum256(payload)
	hashHex := hex.EncodeToString(hash[:])
	ext := attachmentFileExtension(kind)
	subdir := filepath.Join(store.rootDir, kind, hashHex[:2])
	// path_relative is stored with forward slashes so an archive moved
	// between POSIX and Windows resolves consistently. The on-disk path
	// is built from filepath.FromSlash at the seam.
	pathRelative := path.Join(kind, hashHex[:2], hashHex+ext)
	absolutePath := filepath.Join(store.rootDir, filepath.FromSlash(pathRelative))

	if existing, found, err := store.findExisting(ctx, dataPointID, hashHex); err != nil {
		return attachmentRecord{}, err
	} else if found {
		existing.AbsolutePath = filepath.Join(store.rootDir, filepath.FromSlash(existing.PathRelative))
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

	result, err := store.db.ExecContext(ctx, `INSERT INTO data_point_attachments (
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

func (store *attachmentStore) findExisting(ctx context.Context, dataPointID int64, hashHex string) (attachmentRecord, bool, error) {
	var record attachmentRecord
	err := store.db.QueryRowContext(ctx, `SELECT id, data_point_id, kind, sha256, path_relative, byte_size, fetched_at
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
func (store *attachmentStore) Walk(ctx context.Context, fn func(attachmentOrphan) error) error {
	knownPaths := map[string]struct{}{}

	// Row-side: read every attachment row, check the sidecar exists.
	// The deferred Close (instead of per-return manual closes) is safe to
	// hold through the file-side walk below: that walk is pure filesystem
	// and never touches store.db, so no connection-pool contention.
	rows, err := store.db.QueryContext(ctx, `SELECT data_point_id, sha256, path_relative FROM data_point_attachments`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var dataPointID int64
		var sha, pathRelative string
		if err := rows.Scan(&dataPointID, &sha, &pathRelative); err != nil {
			return err
		}
		abs, containErr := resolveContainedPath(store.rootDir, pathRelative)
		if containErr != nil {
			// A path that escapes the attachment root is an integrity
			// violation: never stat it, and never let it claim a knownPaths
			// key. Report it as a row whose sidecar is unresolvable.
			if cbErr := fn(attachmentOrphan{
				Kind:         attachmentOrphanRowMissingFile,
				SHA256:       sha,
				AbsolutePath: store.rootDir,
				PathRelative: pathRelative,
				DataPointID:  dataPointID,
			}); cbErr != nil {
				return cbErr
			}
			continue
		}
		knownPaths[abs] = struct{}{}
		if _, statErr := os.Stat(abs); errors.Is(statErr, os.ErrNotExist) {
			if cbErr := fn(attachmentOrphan{
				Kind:         attachmentOrphanRowMissingFile,
				SHA256:       sha,
				AbsolutePath: abs,
				PathRelative: pathRelative,
				DataPointID:  dataPointID,
			}); cbErr != nil {
				return cbErr
			}
		}
	}
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

// resolveContainedPath joins a stored path_relative onto rootDir and
// rejects anything that escapes the attachment root. A foreign or
// tampered archive (see docs/security.md threat model: "archive
// contents written by earlier runs") could carry an absolute path, a
// "../" traversal, or any value that — after filepath.Join+Clean — no
// longer sits under rootDir. Walk owns the live containment rule
// through this helper. path_relative is stored with forward slashes;
// FromSlash maps it to the OS separator before joining (matching
// Store).
func resolveContainedPath(rootDir, pathRelative string) (string, error) {
	osRelative := filepath.FromSlash(pathRelative)
	if filepath.IsAbs(osRelative) {
		return "", fmt.Errorf("attachment path_relative %q is absolute", pathRelative)
	}
	// Scan segments on the OS-native path so a "..\\" segment is caught on
	// Windows too, not only the forward-slash "../" form.
	for _, segment := range strings.Split(osRelative, string(filepath.Separator)) {
		if segment == ".." {
			return "", fmt.Errorf("attachment path_relative %q contains a parent (\"..\") segment", pathRelative)
		}
	}
	// Clean rootDir before the boundary comparison: it derives from the
	// archive path (e.g. `--db a/../archive.sqlite`) and may carry internal
	// dot segments that filepath.Join collapses, which would otherwise make
	// the prefix check reject legitimate in-root attachments.
	cleanRoot := filepath.Clean(rootDir)
	abs := filepath.Join(cleanRoot, osRelative)
	if abs != cleanRoot && !strings.HasPrefix(abs, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("attachment path_relative %q escapes attachment root", pathRelative)
	}
	return abs, nil
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
		// Tighten BOTH the per-kind dir and its child shard subdir.
		// MkdirAll may have left a pre-existing intermediate `<kind>`
		// dir with looser perms; re-chmod walks up one level.
		if err := os.Chmod(subdir, 0o700); err != nil {
			return fmt.Errorf("chmod attachment subdir: %w", err)
		}
		if err := os.Chmod(filepath.Dir(subdir), 0o700); err != nil {
			return fmt.Errorf("chmod attachment kind dir: %w", err)
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
