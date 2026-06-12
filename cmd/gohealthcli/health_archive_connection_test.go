package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHealthArchiveConnectionAPIManagesConnectionIdentityMetadataAndProfileSnapshots(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		t.Fatalf("open connection API: %v", err)
	}
	defer archive.Close()

	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	token := oauthTokenResponse{
		tokenType: "Bearer",
		scopes:    []string{googleHealthActivityReadonlyScope, googleHealthProfileReadonlyScope},
		expiresAt: now.Add(time.Hour),
		rawTokenMaterialObject: map[string]any{
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
		},
	}
	identity := googleIdentity{
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
		rawJSON:            `{"healthUserId":"111111256096816351","legacyUserId":"A1B2C3"}`,
	}
	connectionID := "googlehealth:" + identity.healthUserID

	if err := archive.EnsureSameGoogleIdentity(context.Background(), identity.healthUserID); err != nil {
		t.Fatalf("ensure empty identity: %v", err)
	}
	if err := archive.UpsertConnection(context.Background(), connectionID, identity, token, now); err != nil {
		t.Fatalf("upsert connection: %v", err)
	}
	if err := archive.EnsureSameGoogleIdentity(context.Background(), "222222222222222222"); err == nil {
		t.Fatal("ensure different identity error = nil, want refusal")
	}

	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("current connection: %v", err)
	}
	if connection.ID != connectionID || connection.GoogleHealthUserID != identity.healthUserID || connection.LegacyFitbitUserID != "A1B2C3" {
		t.Fatalf("connection = %+v, want archived identity", connection)
	}
	if strings.Contains(connection.TokenMetadataJSON, "access-secret") || strings.Contains(connection.TokenMetadataJSON, "refresh-secret") {
		t.Fatalf("token metadata leaked token material: %s", connection.TokenMetadataJSON)
	}

	count, tokenStatus, err := archive.InspectConnectionTokenMetadata()
	if err != nil {
		t.Fatalf("inspect token metadata: %v", err)
	}
	if count != 1 || tokenStatus != "metadata_present" {
		t.Fatalf("token metadata inspection = (%d, %q), want metadata_present for one Connection", count, tokenStatus)
	}

	refreshed := googleIdentity{
		healthUserID:       identity.healthUserID,
		legacyFitbitUserID: "Z9Y8X7",
		rawJSON:            `{"healthUserId":"111111256096816351","legacyUserId":"Z9Y8X7","refreshed":true}`,
	}
	if err := archive.RefreshConnectionIdentity(context.Background(), connection, refreshed, now.Add(time.Minute)); err != nil {
		t.Fatalf("refresh identity: %v", err)
	}
	connection, err = archive.CurrentConnection()
	if err != nil {
		t.Fatalf("current connection after refresh: %v", err)
	}
	if connection.LegacyFitbitUserID != "Z9Y8X7" {
		t.Fatalf("legacyFitbitUserID = %q, want refreshed", connection.LegacyFitbitUserID)
	}
	// Profile-snapshot insertion has moved to identitySnapshotArchive in
	// slice B of #97; coverage lives in identity_snapshot_archive_test.go.
}
