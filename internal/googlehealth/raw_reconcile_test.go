package googlehealth

import (
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestRawReconcileUsesSyncRequestBuilderForTimestampAndCivilDateRanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dataType string
		from     string
		to       string
	}{
		{
			name:     "timestamp",
			dataType: "steps",
			from:     "2026-01-01T00:00:00Z",
			to:       "2026-01-02T00:00:00Z",
		},
		{
			name:     "civil date",
			dataType: "daily-resting-heart-rate",
			from:     "2026-01-01",
			to:       "2026-01-02",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rawDescription, err := DescribeRawRequest(RawRequestOptions{
				Target:               []string{"data-type", test.dataType, "reconcile"},
				SourceFamily:         "wearable",
				SourceFamilyProvided: true,
				From:                 test.from,
				To:                   test.to,
				ResolvedAt:           time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("DescribeRawRequest: %v", err)
			}
			syncDescription, err := (Ingestion{}).DescribePlan(IngestionRequest{
				DataType:     test.dataType,
				From:         test.from,
				To:           test.to,
				SourceFamily: "wearable",
			})
			if err != nil {
				t.Fatalf("DescribePlan: %v", err)
			}

			if !reflect.DeepEqual(rawDescription.Request, syncDescription.Request) {
				t.Fatalf("raw request = %+v, sync request = %+v", rawDescription.Request, syncDescription.Request)
			}
			if rawDescription.PageSize != syncDescription.PagePolicy.PageSize {
				t.Fatalf("raw page size = %d, sync page size = %d", rawDescription.PageSize, syncDescription.PagePolicy.PageSize)
			}
			parsed, err := url.Parse(rawDescription.Request.URL)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Query().Get("dataSourceFamily") != googleHealthWearableSourceFamilyFilterName {
				t.Fatalf("dataSourceFamily = %q", parsed.Query().Get("dataSourceFamily"))
			}
		})
	}
}
