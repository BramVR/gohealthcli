package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

type routeIdentifier struct {
	text         string
	resourceName bool
}

func parseRouteIdentifier(text string) (routeIdentifier, error) {
	invalid := fmt.Errorf("route identifier %q must be an upstream_resource_name (users/<user>/dataTypes/<type>/dataPoints/<id>) or its trailing Provider ID", text)
	if text == "" || strings.IndexFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return routeIdentifier{}, invalid
	}
	if !strings.Contains(text, "/") {
		return routeIdentifier{text: text}, nil
	}
	segments := strings.Split(text, "/")
	if len(segments) != 6 || segments[0] != "users" || segments[2] != "dataTypes" || segments[4] != "dataPoints" {
		return routeIdentifier{}, invalid
	}
	for _, segment := range segments {
		if segment == "" {
			return routeIdentifier{}, invalid
		}
	}
	return routeIdentifier{text: text, resourceName: true}, nil
}

type archivedExerciseRoute struct {
	tcx []byte
}

type routeIdentifierError struct {
	message string
}

func (err *routeIdentifierError) Error() string { return err.message }

type routeNotArchivedError struct {
	identifier routeIdentifier
}

func (err *routeNotArchivedError) Error() string {
	return fmt.Sprintf("no archived route for exercise %q; grant the tcx scope with gohealthcli connect --add-scopes tcx, then re-sync that session's window with gohealthcli sync --types exercise --from YYYY-MM-DD", err.identifier.text)
}

func readArchivedExerciseRoute(ctx context.Context, archivePath string, identifier routeIdentifier) (route archivedExerciseRoute, err error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).openInspectionOnly(ctx)
	if err != nil {
		return archivedExerciseRoute{}, err
	}
	defer func() {
		if closeErr := handle.Close(); closeErr != nil {
			route = archivedExerciseRoute{}
			err = errors.Join(err, fmt.Errorf("close Health Archive: %w", closeErr))
		}
	}()
	dataPointID, err := resolveRouteExercise(ctx, handle.db, identifier)
	if err != nil {
		return archivedExerciseRoute{}, err
	}
	record, err := newestTCXAttachment(ctx, handle.db, dataPointID)
	if errors.Is(err, sql.ErrNoRows) {
		return archivedExerciseRoute{}, &routeNotArchivedError{identifier: identifier}
	}
	if err != nil {
		return archivedExerciseRoute{}, err
	}
	payload, err := readVerifiedAttachment(attachmentRootDirForArchive(archivePath), record)
	if err != nil {
		return archivedExerciseRoute{}, err
	}
	return archivedExerciseRoute{tcx: payload}, nil
}

func resolveRouteExercise(ctx context.Context, db *sql.DB, identifier routeIdentifier) (int64, error) {
	match := `upstream_resource_name = ?1 COLLATE BINARY`
	if !identifier.resourceName {
		match = `substr(upstream_resource_name, -(length(?1) + 1)) = ('/' || ?1) COLLATE BINARY`
	}
	var total, exercises int64
	var exerciseID sql.NullInt64
	var firstType sql.NullString
	err := db.QueryRowContext(ctx, `SELECT count(*),
		count(CASE WHEN data_type = 'exercise' THEN 1 END),
		min(CASE WHEN data_type = 'exercise' THEN id END),
		min(data_type)
		FROM data_points WHERE `+match, identifier.text).Scan(&total, &exercises, &exerciseID, &firstType)
	if err != nil {
		return 0, err
	}
	switch {
	case total == 0:
		return 0, &routeIdentifierError{message: fmt.Sprintf("no archived Data Point matches %q", identifier.text)}
	case exercises == 0:
		return 0, &routeIdentifierError{message: fmt.Sprintf("%q is a %s Data Point; route reads exercise Data Points only", identifier.text, firstType.String)}
	case total > 1:
		message := fmt.Sprintf("%q matches more than one archived Data Point", identifier.text)
		if !identifier.resourceName {
			message += "; pass the full upstream_resource_name"
		}
		return 0, &routeIdentifierError{message: message}
	}
	return exerciseID.Int64, nil
}

func newestTCXAttachment(ctx context.Context, db *sql.DB, dataPointID int64) (attachmentRecord, error) {
	var record attachmentRecord
	err := db.QueryRowContext(ctx, `SELECT id, data_point_id, kind, sha256, path_relative, byte_size, fetched_at
		FROM data_point_attachments
		WHERE data_point_id = ? AND kind = 'tcx'
		ORDER BY fetched_at DESC, id DESC LIMIT 1`, dataPointID).Scan(
		&record.ID, &record.DataPointID, &record.Kind, &record.SHA256,
		&record.PathRelative, &record.ByteSize, &record.FetchedAt)
	return record, err
}
