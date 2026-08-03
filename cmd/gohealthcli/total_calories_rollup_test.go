package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestTotalCaloriesDailyRollupSyncsAndExportsThroughPublicCLI(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	page := string(readTestFixture(t, "googlehealth_total_calories_daily_rollup.json"))
	testRuntime, requests := withDailyRollupFetchFake(t, testRuntime, "connect-access-secret", "total-calories", map[string]string{
		"2026-01-01/2026-01-02/": page,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "total-calories", "--rollup", "daily",
		"--from", "2026-01-01", "--to", "2026-01-02", "--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var syncResult map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &syncResult); err != nil {
		t.Fatalf("sync stdout is not JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, syncResult, "status", "sync_completed")
	assertJSONString(t, syncResult, "endpoint_family", "dailyRollUp")
	assertJSONNumber(t, syncResult, "rollups_seen", 1)
	assertJSONNumber(t, syncResult, "rollups_new", 1)
	if len(*requests) != 1 {
		t.Fatalf("Provider request count = %d, want 1", len(*requests))
	}

	stdout.Reset()
	stderr.Reset()
	code = runWithRuntime([]string{
		"export", "--config", configPath, "--db", archivePath,
		"total-calories-rollups", "--stdout", "--format", "csv",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("export exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	want := "provider_name,connection_id,rollup_kind,window_start_utc,window_end_utc,civil_date,kcal_sum\n" +
		"googlehealth,googlehealth:111111256096816351,dailyRollUp,,,2026-01-01,2142.5\n"
	if stdout.String() != want {
		t.Fatalf("export output =\n%s\nwant:\n%s", stdout.String(), want)
	}
}

func TestTotalCaloriesRollupCorrectionAndDedupe(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	page := func(kcal string) string {
		return fmt.Sprintf(`{"rollupDataPoints":[{"totalCalories":{"kcalSum":%s},"civilStartTime":{"date":{"year":2026,"month":1,"day":1}},"civilEndTime":{"date":{"year":2026,"month":1,"day":2}}}]}`, kcal)
	}
	runSync := func(kcal string) map[string]any {
		var requests *[]googlehealth.RawRequest
		testRuntime, requests = withDailyRollupFetchFake(t, testRuntime, "connect-access-secret", "total-calories", map[string]string{
			"2026-01-01/2026-01-02/": page(kcal),
		})
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		code := runWithRuntime([]string{
			"sync", "--config", configPath, "--db", archivePath,
			"--types", "total-calories", "--rollup", "daily",
			"--from", "2026-01-01", "--to", "2026-01-02", "--json",
		}, stdout, stderr, testRuntime)
		if code != 0 {
			t.Fatalf("sync exit code = %d\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
		}
		if len(*requests) != 1 {
			t.Fatalf("Provider request count = %d, want 1", len(*requests))
		}
		var result map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("decode sync output: %v", err)
		}
		return result
	}

	first := runSync("2000.0")
	assertJSONNumber(t, first, "rollups_new", 1)
	unchanged := runSync("2000.0")
	assertJSONNumber(t, unchanged, "rollups_new", 0)
	assertJSONNumber(t, unchanged, "rollups_updated", 0)
	corrected := runSync("2050.5")
	assertJSONNumber(t, corrected, "rollups_updated", 1)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)

	db := openArchiveForTest(t, archivePath)
	defer db.Close()
	var rawJSON string
	if err := db.QueryRowContext(context.Background(), `SELECT raw_json FROM rollups WHERE data_type = 'total-calories'`).Scan(&rawJSON); err != nil {
		t.Fatalf("query corrected Rollup: %v", err)
	}
	if !strings.Contains(rawJSON, `"kcalSum":2050.5`) {
		t.Fatalf("raw_json = %s, want corrected kcalSum", rawJSON)
	}
}

func TestTotalCaloriesRollupCursorsRemainModeIndependent(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime, _ = withDailyRollupFetchFake(t, testRuntime, "connect-access-secret", "total-calories", map[string]string{
		"2026-01-01/2026-01-02/": `{"rollupDataPoints":[]}`,
	})
	runTotalCaloriesSync(t, configPath, archivePath, testRuntime, "daily", "2026-01-01", "2026-01-02")

	testRuntime, _ = withWindowRollupFetchFake(t, testRuntime, "connect-access-secret", "total-calories", map[string]string{
		"2026-01-01T00:00:00Z/2026-01-02T00:00:00Z/3600s/": `{"rollupDataPoints":[]}`,
	})
	runTotalCaloriesSync(t, configPath, archivePath, testRuntime, "hourly", "2026-01-01", "2026-01-02")

	db := openArchiveForTest(t, archivePath)
	defer db.Close()
	rows, err := db.QueryContext(context.Background(), `SELECT rollup_kind, cursor_time FROM sync_cursors WHERE data_type = 'total-calories' ORDER BY rollup_kind`)
	if err != nil {
		t.Fatalf("query cursors: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var kind, cursor string
		if err := rows.Scan(&kind, &cursor); err != nil {
			t.Fatalf("scan cursor: %v", err)
		}
		got[kind] = cursor
	}
	want := map[string]string{"daily": "2026-01-02", "hourly": "2026-01-02T00:00:00Z"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("cursors = %v, want %v", got, want)
	}
}

func runTotalCaloriesSync(t *testing.T, configPath, archivePath string, runtime runtimeAdapters, rollup, from, to string) {
	t.Helper()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "total-calories", "--rollup", rollup,
		"--from", from, "--to", to, "--json",
	}, stdout, stderr, runtime)
	if code != 0 {
		t.Fatalf("%s sync exit code = %d\nstderr: %s\nstdout: %s", rollup, code, stderr.String(), stdout.String())
	}
}

func TestTotalCaloriesRawPathsRemainRejectedBeforeIO(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"sync", "--types", "total-calories", "--from", "2026-01-01", "--to", "2026-01-02", "--json"},
		{"raw", "data-type", "total-calories", "--from", "2026-01-01", "--to", "2026-01-02"},
	} {
		args := args
		t.Run(strings.Join(args[:2], "-"), func(t *testing.T) {
			t.Parallel()
			providerRequests := 0
			runtime := runtimeAdapters{fetchRawProvider: func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
				providerRequests++
				return nil, nil
			}}
			configPath := filepath.Join(t.TempDir(), "missing-config.toml")
			archivePath := filepath.Join(t.TempDir(), "missing-archive.sqlite")
			args = append(args, "--config", configPath, "--db", archivePath)
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			if code := runWithRuntime(args, stdout, stderr, runtime); code == 0 {
				t.Fatalf("exit code = 0, want raw-path rejection\nstdout: %s", stdout.String())
			}
			if providerRequests != 0 {
				t.Fatalf("Provider requests = %d, want 0", providerRequests)
			}
		})
	}
}

func TestTotalCaloriesRollupExportFormatsAreDeterministic(t *testing.T) {
	t.Parallel()
	configPath, archivePath, _ := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	db := openArchiveForTest(t, archivePath)
	for _, row := range []struct {
		kind, start, end, civil, raw string
	}{
		{kind: "window=6h", start: "2026-01-02T00:00:00Z", end: "2026-01-02T06:00:00Z", raw: `{"totalCalories":{"kcalSum":100}}`},
		{kind: "dailyRollUp", civil: "2026-01-01", raw: `{"totalCalories":{"kcalSum":2142.5}}`},
		{kind: "hourly", start: "2026-01-01T00:00:00Z", end: "2026-01-01T01:00:00Z", raw: `{"totalCalories":{"kcalSum":0.0}}`},
	} {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO rollups (
			provider_name, connection_id, data_type, rollup_kind,
			window_start_utc, window_end_utc, civil_date, raw_json, inserted_at, updated_at
		) VALUES ('googlehealth', 'googlehealth:111111256096816351', 'total-calories', ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, '2026-01-03T00:00:00Z', '2026-01-03T00:00:00Z')`,
			row.kind, row.start, row.end, row.civil, row.raw); err != nil {
			t.Fatalf("insert Rollup: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	wantCSV := "provider_name,connection_id,rollup_kind,window_start_utc,window_end_utc,civil_date,kcal_sum\n" +
		"googlehealth,googlehealth:111111256096816351,dailyRollUp,,,2026-01-01,2142.5\n" +
		"googlehealth,googlehealth:111111256096816351,hourly,2026-01-01T00:00:00Z,2026-01-01T01:00:00Z,,0.0\n" +
		"googlehealth,googlehealth:111111256096816351,window=6h,2026-01-02T00:00:00Z,2026-01-02T06:00:00Z,,100\n"
	wantJSONL := "{\"provider_name\":\"googlehealth\",\"connection_id\":\"googlehealth:111111256096816351\",\"rollup_kind\":\"dailyRollUp\",\"window_start_utc\":\"\",\"window_end_utc\":\"\",\"civil_date\":\"2026-01-01\",\"kcal_sum\":\"2142.5\"}\n" +
		"{\"provider_name\":\"googlehealth\",\"connection_id\":\"googlehealth:111111256096816351\",\"rollup_kind\":\"hourly\",\"window_start_utc\":\"2026-01-01T00:00:00Z\",\"window_end_utc\":\"2026-01-01T01:00:00Z\",\"civil_date\":\"\",\"kcal_sum\":\"0.0\"}\n" +
		"{\"provider_name\":\"googlehealth\",\"connection_id\":\"googlehealth:111111256096816351\",\"rollup_kind\":\"window=6h\",\"window_start_utc\":\"2026-01-02T00:00:00Z\",\"window_end_utc\":\"2026-01-02T06:00:00Z\",\"civil_date\":\"\",\"kcal_sum\":\"100\"}\n"
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "csv", args: []string{"--format", "csv"}, want: wantCSV},
		{name: "plain", args: []string{"--plain"}, want: wantCSV},
		{name: "jsonl", args: []string{"--format", "jsonl"}, want: wantJSONL},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			args := []string{"export", "--config", configPath, "--db", archivePath, "total-calories-rollups", "--stdout"}
			args = append(args, test.args...)
			if code := run(args, stdout, stderr); code != 0 {
				t.Fatalf("export exit code = %d\nstderr: %s", code, stderr.String())
			}
			if stdout.String() != test.want {
				t.Fatalf("output =\n%s\nwant:\n%s", stdout.String(), test.want)
			}
		})
	}
}

func TestTotalCaloriesRollupViewMigrationUpgradesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	archivePath := filepath.Join(t.TempDir(), "legacy", "archive.sqlite")
	createLegacyArchive(t, archivePath, 26)
	lifecycle := healthArchiveLifecycle{path: archivePath}
	if err := lifecycle.Migrate(context.Background()); err != nil {
		t.Fatalf("upgrade v26 Health Archive: %v", err)
	}
	if err := lifecycle.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	assertArchiveUserVersion(t, archivePath, currentSchemaVersion)
	db := openArchiveForTest(t, archivePath)
	defer db.Close()
	var viewCount, migrationCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sqlite_master WHERE type = 'view' AND name = 'total_calories_rollups'`).Scan(&viewCount); err != nil {
		t.Fatalf("query view: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM schema_migrations WHERE version = 27 AND name = 'add_total_calories_rollups_view'`).Scan(&migrationCount); err != nil {
		t.Fatalf("query migration: %v", err)
	}
	if viewCount != 1 || migrationCount != 1 {
		t.Fatalf("view/migration counts = (%d, %d), want (1, 1)", viewCount, migrationCount)
	}
}

func TestTotalCaloriesPhysicalWindowLargerThanProviderMaximumFailsBeforeIO(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	providerRequests := 0
	testRuntime.fetchRawProvider = func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
		providerRequests++
		return nil, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "total-calories", "--rollup", "window=360h",
		"--from", "2026-01-01", "--to", "2026-02-01", "--json",
	}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatalf("sync exit code = 0, want preflight failure\nstdout: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "exceeds Google Health's 14-day maximum") {
		t.Fatalf("stdout = %s, want 14-day maximum error", stdout.String())
	}
	if providerRequests != 0 {
		t.Fatalf("Provider requests = %d, want 0", providerRequests)
	}
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
}
