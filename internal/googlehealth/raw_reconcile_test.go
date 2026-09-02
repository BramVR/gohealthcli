package googlehealth

import (
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
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

func TestRawReconcilePagingUsesSharedDataPointReadBuilder(t *testing.T) {
	t.Parallel()

	options := RawRequestOptions{
		Target:               []string{"data-type", "steps", "reconcile"},
		SourceFamily:         "wearable",
		SourceFamilyProvided: true,
		From:                 "2026-01-01T00:00:00Z",
		To:                   "2026-01-02T00:00:00Z",
		ResolvedAt:           time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
		PageSize:             17,
		PageSizeProvided:     true,
		PageToken:            "synthetic-page-token",
		PageTokenProvided:    true,
	}
	description, err := DescribeRawRequest(options)
	if err != nil {
		t.Fatalf("DescribeRawRequest: %v", err)
	}
	wantURL := googleHealthBaseURL + "/users/me/dataTypes/steps/dataPoints:reconcile?dataSourceFamily=users%2Fme%2FdataSourceFamilies%2Fgoogle-wearables&filter=steps.interval.start_time+%3E%3D+%222026-01-01T00%3A00%3A00Z%22+AND+steps.interval.start_time+%3C+%222026-01-02T00%3A00%3A00Z%22&pageSize=17&pageToken=synthetic-page-token"
	if description.Request.Method != http.MethodGet || description.Request.URL != wantURL {
		t.Fatalf("raw request = %+v, want GET %s", description.Request, wantURL)
	}
	if description.PageSize != 17 || !description.PageTokenProvided {
		t.Fatalf("paging description = size %d, token provided %t", description.PageSize, description.PageTokenProvided)
	}
	if strings.Contains(description.SanitizedURL, "synthetic-page-token") || !strings.Contains(description.SanitizedURL, "pageToken=REDACTED") {
		t.Fatalf("sanitized URL = %q", description.SanitizedURL)
	}
}

func TestRawReconcileCatalogSupportAndScopes(t *testing.T) {
	t.Parallel()

	dataTypes := ReconcileDataTypes()
	if len(dataTypes) == 0 || !slices.Contains(dataTypes, "steps") || slices.Contains(dataTypes, "electrocardiogram") {
		t.Fatalf("ReconcileDataTypes = %v", dataTypes)
	}
	for _, dataType := range dataTypes {
		request, err := BuildRawRequest(RawRequestOptions{
			Target:               []string{"data-type", dataType, "reconcile"},
			SourceFamily:         "wearable",
			SourceFamilyProvided: true,
			From:                 "2026-01-01",
			To:                   "2026-01-02",
			ResolvedAt:           time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("BuildRawRequest(%s): %v", dataType, err)
		}
		if request.Method != http.MethodGet || request.EndpointName != "dataTypes."+dataType+".reconcile" {
			t.Fatalf("request(%s) = %+v", dataType, request)
		}
		if !slices.Equal(request.RequiredScopes, ScopesForDataType(dataType)) {
			t.Fatalf("RequiredScopes(%s) = %v, want %v", dataType, request.RequiredScopes, ScopesForDataType(dataType))
		}
	}
}

func TestValidateRawReconcileOptionsAcceptsProgrammaticValuesWithoutFlagProvenance(t *testing.T) {
	t.Parallel()

	err := ValidateRawRequestOptions(RawRequestOptions{
		Target:       []string{"data-type", "steps", "reconcile"},
		From:         "2026-01-01",
		SourceFamily: "wearable",
	})
	if err != nil {
		t.Fatalf("ValidateRawRequestOptions: %v", err)
	}
}
