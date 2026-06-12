package main

import (
	"context"
	"strings"
	"testing"
)

// TestSearchableTextViewReturnsRowsFromAllFourSources is the slice
// tracer for #110. The view UNIONs categorical text from four sources
// (paired devices, data source JSON, current profile, exercise labels)
// and tags each row with a `kind` discriminator. The view's value is
// that an LLM (or a user) can run one LIKE query against one column
// instead of juggling four underlying paths.
func TestSearchableTextViewReturnsRowsFromAllFourSources(t *testing.T) {
	t.Parallel()
	_, archivePath, _ := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	// Seed one paired-devices snapshot + one profile snapshot.
	snapshots, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open snapshot archive: %v", err)
	}
	connection, err := readCurrentConnection(context.Background(), snapshots.db)
	if err != nil {
		snapshots.Close()
		t.Fatalf("read current Connection: %v", err)
	}
	if _, err := snapshots.Insert(context.Background(), connection, "paired-devices", `{"pairedDevices":[
		{"name":"users/111111256096816351/pairedDevices/2978855095","deviceType":"WATCH","deviceVersion":"Pixel Watch 2"},
		{"name":"users/111111256096816351/pairedDevices/1122334455","deviceType":"TRACKER","deviceVersion":"Fitbit Charge 5"}
	]}`, "2026-06-08T00:00:00Z"); err != nil {
		snapshots.Close()
		t.Fatalf("Insert paired-devices: %v", err)
	}
	if _, err := snapshots.Insert(context.Background(), connection, "profile", `{"firstName":"Bram","lastName":"Van Rompuy"}`, "2026-06-08T00:00:00Z"); err != nil {
		snapshots.Close()
		t.Fatalf("Insert profile: %v", err)
	}
	snapshots.Close()

	// Seed one exercise Data Point (for exercise_type) and one source-app
	// Data Point (for data_source kind).
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "exercise",
		resourceName: "users/me/dataTypes/exercise/dataPoints/run-1",
		recordKind:   "session",
		startUTC:     "2026-06-08T17:00:00Z",
		endUTC:       "2026-06-08T17:30:00Z",
		startCivil:   "2026-06-08T18:00:00",
		endCivil:     "2026-06-08T18:30:00",
		civilDate:    "2026-06-08",
		dataSource:   `{"platform":"FITBIT","device":{"displayName":"Pixel Watch 4"},"applicationName":"Strava"}`,
		rawJSON:      `{"exercise":{"exerciseType":"RUNNING","displayName":"Morning run"}}`,
	})

	db := openArchiveForTest(t, archivePath)

	rows, err := db.QueryContext(context.Background(), `SELECT kind, text FROM searchable_text ORDER BY kind, text`)
	if err != nil {
		t.Fatalf("query searchable_text: %v", err)
	}
	defer rows.Close()
	seen := map[string][]string{}
	for rows.Next() {
		var kind, text string
		if err := rows.Scan(&kind, &text); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen[kind] = append(seen[kind], text)
	}
	for _, kind := range []string{"device", "data_source", "profile", "exercise_type"} {
		if len(seen[kind]) == 0 {
			t.Errorf("kind=%q produced 0 rows; want at least one", kind)
		}
	}
	// Sanity-check: the device kind must contain at least one of the
	// seeded device versions.
	deviceText := strings.Join(seen["device"], " | ")
	if !strings.Contains(deviceText, "Pixel Watch 2") {
		t.Errorf("device rows missing 'Pixel Watch 2'; got %q", deviceText)
	}
	profileText := strings.Join(seen["profile"], " | ")
	if !strings.Contains(profileText, "Bram") {
		t.Errorf("profile rows missing first name; got %q", profileText)
	}
	exerciseText := strings.Join(seen["exercise_type"], " | ")
	if !strings.Contains(exerciseText, "RUNNING") {
		t.Errorf("exercise_type rows missing 'RUNNING'; got %q", exerciseText)
	}
}

// TestSearchableTextLIKENeedleAnswersAcrossKinds pins the intended
// query shape: a single LIKE against `text` returns hits from any
// underlying source without the caller knowing which.
func TestSearchableTextLIKENeedleAnswersAcrossKinds(t *testing.T) {
	t.Parallel()
	_, archivePath, _ := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	snapshots, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open snapshots: %v", err)
	}
	connection, err := readCurrentConnection(context.Background(), snapshots.db)
	if err != nil {
		snapshots.Close()
		t.Fatalf("read current Connection: %v", err)
	}
	// "Pixel" appears in both a paired device version and a data source's device display name.
	if _, err := snapshots.Insert(context.Background(), connection, "paired-devices", `{"pairedDevices":[{"name":"users/111111256096816351/pairedDevices/2978855095","deviceType":"WATCH","deviceVersion":"Pixel Watch 2"}]}`, "2026-06-08T00:00:00Z"); err != nil {
		snapshots.Close()
		t.Fatalf("Insert paired-devices: %v", err)
	}
	snapshots.Close()
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "exercise",
		resourceName: "users/me/dataTypes/exercise/dataPoints/run-pixel",
		recordKind:   "session",
		startUTC:     "2026-06-08T17:00:00Z",
		endUTC:       "2026-06-08T17:30:00Z",
		startCivil:   "2026-06-08T18:00:00",
		endCivil:     "2026-06-08T18:30:00",
		civilDate:    "2026-06-08",
		dataSource:   `{"platform":"FITBIT","device":{"displayName":"Pixel Watch 4"},"applicationName":"Fit"}`,
		rawJSON:      `{"exercise":{"exerciseType":"RUNNING"}}`,
	})

	db := openArchiveForTest(t, archivePath)
	rows, err := db.QueryContext(context.Background(), `SELECT DISTINCT kind FROM searchable_text WHERE text LIKE '%Pixel%' ORDER BY kind`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatalf("scan: %v", err)
		}
		kinds = append(kinds, kind)
	}
	if len(kinds) < 2 {
		t.Fatalf("Pixel needle matched only %d kind(s) (%v); want hits from at least 2 different kinds (device + data_source)", len(kinds), kinds)
	}
}
