package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type healthArchiveConnectionAPI interface {
	Close() error
	EnsureSameGoogleIdentity(healthUserID string) error
	CurrentConnection() (archivedConnection, error)
	UpsertConnection(connectionID string, identity googleIdentity, token oauthTokenResponse, now time.Time) error
	UpdateConnectionTokenMetadata(connectionID string, token oauthTokenResponse, now time.Time) error
	RefreshConnectionIdentity(connection archivedConnection, identity googleIdentity, now time.Time) error
	InspectConnectionTokenMetadata() (int, string, error)
}

type sqliteHealthArchiveConnectionAPI struct {
	db *sql.DB
}

func openHealthArchiveConnectionAPI(archivePath string) (healthArchiveConnectionAPI, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(writeArchive)
	if err != nil {
		return nil, err
	}
	return &sqliteHealthArchiveConnectionAPI{db: handle.db}, nil
}

func (archive *sqliteHealthArchiveConnectionAPI) Close() error {
	return archive.db.Close()
}

func (archive *sqliteHealthArchiveConnectionAPI) EnsureSameGoogleIdentity(healthUserID string) error {
	return ensureSameArchiveIdentity(archive.db, healthUserID)
}

func (archive *sqliteHealthArchiveConnectionAPI) CurrentConnection() (archivedConnection, error) {
	return readCurrentConnection(archive.db)
}

func (archive *sqliteHealthArchiveConnectionAPI) UpsertConnection(connectionID string, identity googleIdentity, token oauthTokenResponse, now time.Time) error {
	return upsertConnection(archive.db, connectionID, identity, token, now)
}

func (archive *sqliteHealthArchiveConnectionAPI) UpdateConnectionTokenMetadata(connectionID string, token oauthTokenResponse, now time.Time) error {
	return updateConnectionTokenMetadata(archive.db, connectionID, token, now)
}

func (archive *sqliteHealthArchiveConnectionAPI) RefreshConnectionIdentity(connection archivedConnection, identity googleIdentity, now time.Time) error {
	return refreshConnectionIdentity(archive.db, connection, identity, now)
}

func (archive *sqliteHealthArchiveConnectionAPI) InspectConnectionTokenMetadata() (int, string, error) {
	return inspectConnectionTokenMetadata(archive.db)
}

func ensureSameArchiveIdentity(db *sql.DB, healthUserID string) error {
	rows, err := db.Query(`SELECT DISTINCT google_health_user_id FROM connections`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return err
		}
		if existing != healthUserID {
			return errors.New("Health Archive already belongs to a different Google Identity; use a new archive path")
		}
	}
	return rows.Err()
}

func readCurrentConnection(db *sql.DB) (archivedConnection, error) {
	rows, err := db.Query(`SELECT
		id,
		provider_name,
		google_health_user_id,
		legacy_fitbit_user_id,
		token_metadata_json
	FROM connections ORDER BY created_at, id LIMIT 2`)
	if err != nil {
		return archivedConnection{}, err
	}
	defer rows.Close()

	var connections []archivedConnection
	for rows.Next() {
		var connection archivedConnection
		var legacyFitbitUserID sql.NullString
		if err := rows.Scan(
			&connection.id,
			&connection.providerName,
			&connection.googleHealthUserID,
			&legacyFitbitUserID,
			&connection.tokenMetadataJSON,
		); err != nil {
			return archivedConnection{}, err
		}
		if legacyFitbitUserID.Valid {
			connection.legacyFitbitUserID = legacyFitbitUserID.String
		}
		connections = append(connections, connection)
	}
	if err := rows.Err(); err != nil {
		return archivedConnection{}, err
	}
	if len(connections) == 0 {
		return archivedConnection{}, sql.ErrNoRows
	}
	if len(connections) > 1 {
		return archivedConnection{}, errors.New("multiple Connections found; use a separate Health Archive for each Google Identity")
	}
	return connections[0], nil
}


func upsertConnection(db *sql.DB, connectionID string, identity googleIdentity, token oauthTokenResponse, now time.Time) error {
	metadataJSON, err := connectionTokenMetadataJSON(connectionID, token)
	if err != nil {
		return err
	}
	nowText := now.UTC().Format(time.RFC3339)
	_, err = db.Exec(`INSERT INTO connections (
		id,
		provider_name,
		google_health_user_id,
		legacy_fitbit_user_id,
		token_metadata_json,
		google_identity_json,
		created_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		legacy_fitbit_user_id = excluded.legacy_fitbit_user_id,
		token_metadata_json = excluded.token_metadata_json,
		google_identity_json = excluded.google_identity_json,
		updated_at = excluded.updated_at`,
		connectionID,
		"googlehealth",
		identity.healthUserID,
		identity.legacyFitbitUserID,
		string(metadataJSON),
		identity.rawJSON,
		nowText,
		nowText,
	)
	return err
}

func updateConnectionTokenMetadata(db *sql.DB, connectionID string, token oauthTokenResponse, now time.Time) error {
	metadataJSON, err := connectionTokenMetadataJSON(connectionID, token)
	if err != nil {
		return err
	}
	result, err := db.Exec(`UPDATE connections SET token_metadata_json = ?, updated_at = ? WHERE id = ?`,
		string(metadataJSON),
		now.UTC().Format(time.RFC3339),
		connectionID,
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected != 1 {
		return fmt.Errorf("Connection %s not found", connectionID)
	}
	return nil
}

func connectionTokenMetadataJSON(connectionID string, token oauthTokenResponse) ([]byte, error) {
	metadata := map[string]any{
		"credential_store_key": connectionID,
		"expires_at":           token.expiresAt.UTC().Format(time.RFC3339),
		"scopes":               token.scopes,
		"token_type":           token.tokenType,
	}
	if token.refreshTokenExpiresAt != nil {
		metadata["refresh_expires_at"] = token.refreshTokenExpiresAt.UTC().Format(time.RFC3339)
	}
	return json.Marshal(metadata)
}

func refreshConnectionIdentity(db *sql.DB, connection archivedConnection, identity googleIdentity, now time.Time) error {
	legacyFitbitUserID := connection.legacyFitbitUserID
	if identity.legacyFitbitUserID != "" {
		legacyFitbitUserID = identity.legacyFitbitUserID
	}
	_, err := db.Exec(`UPDATE connections SET
		google_health_user_id = ?,
		legacy_fitbit_user_id = ?,
		google_identity_json = ?,
		updated_at = ?
	WHERE id = ?`,
		identity.healthUserID,
		legacyFitbitUserID,
		identity.rawJSON,
		now.UTC().Format(time.RFC3339),
		connection.id,
	)
	return err
}

func inspectConnectionTokenMetadata(db *sql.DB) (int, string, error) {
	rows, err := db.Query(`SELECT id, token_metadata_json FROM connections ORDER BY id`)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var connectionID string
		var metadata string
		if err := rows.Scan(&connectionID, &metadata); err != nil {
			return 0, "", err
		}
		count++
		if err := validateTokenMetadata(metadata); err != nil {
			return 0, "", fmt.Errorf("Connection %s: %w", connectionID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	if count == 0 {
		return 0, "not_connected", nil
	}
	return count, "metadata_present", nil
}
