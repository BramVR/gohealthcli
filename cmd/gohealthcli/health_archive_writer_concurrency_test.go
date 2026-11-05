package main

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/BramVR/gohealthcli/internal/archived"
)

func TestConcurrentSyncWritersArchiveEachRecordOnce(t *testing.T) {
	t.Parallel()
	_, archivePath, _ := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	ctx := context.Background()

	const writerCount = 4
	writers := make([]healthArchiveWriter, writerCount)
	for i := range writers {
		writer, err := openHealthArchiveWriter(archivePath)
		if err != nil {
			t.Fatalf("open Health Archive writer %d: %v", i, err)
		}
		defer writer.Close()
		writers[i] = writer
	}
	connection, err := writers[0].CurrentConnection(ctx)
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}

	const recordCount = 30
	var points []archived.DataPoint
	var rollups []archived.Rollup
	for i := 0; i < recordCount; i++ {
		start := fmt.Sprintf("2026-06-09T%02d:%02d:00Z", i/60, i%60)
		points = append(points,
			archived.DataPoint{
				ProviderName:         connection.ProviderName,
				ConnectionID:         connection.ID,
				DataType:             "active-energy-burned",
				UpstreamResourceName: fmt.Sprintf("users/me/dataTypes/active-energy-burned/dataPoints/%d", i),
				RecordKind:           "interval",
				StartTimeUTC:         start,
				EndTimeUTC:           fmt.Sprintf("2026-06-09T%02d:%02d:59Z", i/60, i%60),
				DataSourceJSON:       `{"platform":"FITBIT"}`,
				RawJSON:              fmt.Sprintf(`{"activeEnergyBurned":{"kcal":%d}}`, i),
			},
			archived.DataPoint{
				ProviderName:   connection.ProviderName,
				ConnectionID:   connection.ID,
				DataType:       "oxygen-saturation",
				RecordKind:     "sample",
				StartTimeUTC:   start,
				DataSourceJSON: `{"platform":"FITBIT"}`,
				RawJSON:        fmt.Sprintf(`{"oxygenSaturation":{"percentage":%d}}`, 90+i%10),
			},
		)
		rollups = append(rollups, archived.Rollup{
			ProviderName:         connection.ProviderName,
			ConnectionID:         connection.ID,
			DataType:             "steps",
			RollupKind:           "dailyRollUp",
			CivilDate:            fmt.Sprintf("2026-05-%02d", i+1),
			TimezoneMetadataJSON: "{}",
			RawJSON:              fmt.Sprintf(`{"steps":{"countSum":"%d"}}`, i),
		})
	}

	for index, point := range points {
		raceWritersOnOneRecord(t, writers, fmt.Sprintf("Data Point %d (%s)", index, point.DataType), func(writer healthArchiveWriter) (string, error) {
			return writer.UpsertDataPoint(ctx, point, "2026-06-09T12:00:00Z")
		})
	}
	for index, rollup := range rollups {
		raceWritersOnOneRecord(t, writers, fmt.Sprintf("Rollup %d", index), func(writer healthArchiveWriter) (string, error) {
			return writer.UpsertRollup(ctx, rollup, "2026-06-09T12:00:00Z")
		})
	}

	snapshot, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot: %v", err)
	}
	if got := len(snapshot.DataPoints); got != len(points) {
		t.Errorf("archived %d Data Points, want %d (one per identity)", got, len(points))
	}
	if got := len(snapshot.Rollups); got != len(rollups) {
		t.Errorf("archived %d Rollups, want %d (one per identity)", got, len(rollups))
	}
	if err := ValidateHealthArchiveSnapshot(snapshot); err != nil {
		t.Fatalf("ValidateHealthArchiveSnapshot after concurrent sync writers: %v", err)
	}
}

// raceWritersOnOneRecord releases every writer at once on the same
// record, the interleaving two overlapping `sync` processes hit.
func raceWritersOnOneRecord(t *testing.T, writers []healthArchiveWriter, label string, upsert func(healthArchiveWriter) (string, error)) {
	t.Helper()
	statuses := make([]string, len(writers))
	errs := make([]error, len(writers))
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i, writer := range writers {
		wg.Add(1)
		go func(i int, writer healthArchiveWriter) {
			defer wg.Done()
			<-release
			statuses[i], errs[i] = upsert(writer)
		}(i, writer)
	}
	close(release)
	wg.Wait()
	newCount := 0
	for i := range writers {
		if errs[i] != nil {
			t.Fatalf("%s writer %d: %v", label, i, errs[i])
		}
		switch statuses[i] {
		case "new":
			newCount++
		case "unchanged":
		default:
			t.Fatalf("%s writer %d status = %q, want new or unchanged", label, i, statuses[i])
		}
	}
	if newCount != 1 {
		t.Errorf("%s: %d writers archived it as new, want exactly 1", label, newCount)
	}
}
