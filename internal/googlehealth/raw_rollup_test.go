package googlehealth

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRawRollupCatalogOperations(t *testing.T) {
	t.Parallel()

	if !slices.Contains(RawDataTypes(), "total-calories") {
		t.Fatalf("RawDataTypes = %v, want Rollup-only total-calories", RawDataTypes())
	}
	if got, want := RawDataTypeOperations("steps"), []string{"daily-rollup", "reconcile", "rollup"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RawDataTypeOperations(steps) = %v, want %v", got, want)
	}
	if got, want := RawDataTypeOperations("total-calories"), []string{"daily-rollup", "rollup"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RawDataTypeOperations(total-calories) = %v, want %v", got, want)
	}
	if got, want := RawRollupWindowGranularities("steps"), []string{"1h", "1d", "7d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RawRollupWindowGranularities(steps) = %v, want %v", got, want)
	}
}

func TestRawRollupUsesSyncFirstRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		target     []string
		window     string
		dataType   string
		from       string
		to         string
		syncFrom   string
		syncTo     string
		syncRollup string
	}{
		{
			name:       "daily",
			target:     []string{"data-type", "heart-rate", "daily-rollup"},
			dataType:   "heart-rate",
			from:       "2026-01-01",
			to:         "2026-01-10",
			syncFrom:   "2026-01-01",
			syncTo:     "2026-01-10",
			syncRollup: "daily",
		},
		{
			name:       "physical window",
			target:     []string{"data-type", "total-calories", "rollup"},
			window:     "1h",
			dataType:   "total-calories",
			from:       "2026-01-01T00:00:00Z",
			to:         "2026-01-10T00:00:00Z",
			syncFrom:   "2026-01-01T00:00:00Z",
			syncTo:     "2026-01-10T00:00:00Z",
			syncRollup: "window=1h",
		},
		{
			name:       "daily RFC3339 input",
			target:     []string{"data-type", "steps", "daily-rollup"},
			dataType:   "steps",
			from:       "2026-01-01T23:00:00-05:00",
			to:         "2026-01-04T01:00:00+02:00",
			syncFrom:   "2026-01-02",
			syncTo:     "2026-01-03",
			syncRollup: "daily",
		},
		{
			name:       "physical civil input and equivalent duration",
			target:     []string{"data-type", "steps", "rollup"},
			window:     "60m",
			dataType:   "steps",
			from:       "2026-01-01",
			to:         "2026-01-02",
			syncFrom:   "2026-01-01T00:00:00Z",
			syncTo:     "2026-01-02T00:00:00Z",
			syncRollup: "window=1h",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw, err := DescribeRawRequest(RawRequestOptions{
				Target:         test.target,
				From:           test.from,
				To:             test.to,
				Window:         test.window,
				WindowProvided: test.window != "",
				ResolvedAt:     time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("DescribeRawRequest: %v", err)
			}
			sync, err := (Ingestion{}).DescribePlan(IngestionRequest{
				DataType: test.dataType,
				From:     test.syncFrom,
				To:       test.syncTo,
				Rollup:   test.syncRollup,
			})
			if err != nil {
				t.Fatalf("DescribePlan: %v", err)
			}
			if !reflect.DeepEqual(raw.Request, sync.Request) {
				t.Fatalf("raw request = %+v, sync request = %+v", raw.Request, sync.Request)
			}
		})
	}
}

func TestRawRollupPagingAndSanitization(t *testing.T) {
	t.Parallel()

	description, err := DescribeRawRequest(RawRequestOptions{
		Target:            []string{"data-type", "steps", "rollup"},
		From:              "2026-01-01T00:00:00Z",
		To:                "2026-01-02T00:00:00Z",
		Window:            "1h",
		WindowProvided:    true,
		PageSize:          17,
		PageSizeProvided:  true,
		PageToken:         "synthetic-page-token",
		PageTokenProvided: true,
		ResolvedAt:        time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("DescribeRawRequest: %v", err)
	}
	if !strings.Contains(string(description.Request.Body), `"pageSize":17`) ||
		!strings.Contains(string(description.Request.Body), `"pageToken":"synthetic-page-token"`) {
		t.Fatalf("request body = %s", description.Request.Body)
	}
	if strings.Contains(string(description.SanitizedBody), "synthetic-page-token") ||
		!strings.Contains(string(description.SanitizedBody), `"pageToken":"REDACTED"`) {
		t.Fatalf("sanitized body = %s", description.SanitizedBody)
	}
}

func TestRawRollupAcceptsEveryCatalogWindowGranularity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		window string
		want   string
	}{
		{window: "1h", want: `"windowSize":"3600s"`},
		{window: "1d", want: `"windowSize":"86400s"`},
		{window: "7d", want: `"windowSize":"604800s"`},
	} {
		description, err := DescribeRawRequest(RawRequestOptions{
			Target:         []string{"data-type", "steps", "rollup"},
			From:           "2026-01-01",
			To:             "2026-01-08",
			Window:         test.window,
			WindowProvided: true,
			ResolvedAt:     time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("DescribeRawRequest(%s): %v", test.window, err)
		}
		if !strings.Contains(string(description.Request.Body), test.want) {
			t.Fatalf("window %s body = %s, want %s", test.window, description.Request.Body, test.want)
		}
	}
}

func TestRawRollupRejectsUnsupportedOrMultiRequestInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options RawRequestOptions
		want    string
	}{
		{
			name: "unsupported Data Type",
			options: RawRequestOptions{
				Target: []string{"data-type", "sleep", "daily-rollup"},
				From:   "2026-01-01",
				To:     "2026-01-02",
			},
			want: "not supported by dataPoints.dailyRollUp",
		},
		{
			name: "unsupported granularity",
			options: RawRequestOptions{
				Target:         []string{"data-type", "steps", "rollup"},
				From:           "2026-01-01T00:00:00Z",
				To:             "2026-01-02T00:00:00Z",
				Window:         "6h",
				WindowProvided: true,
			},
			want: "supported window granularities: 1h, 1d, 7d",
		},
		{
			name: "daily range needs multiple requests",
			options: RawRequestOptions{
				Target: []string{"data-type", "heart-rate", "daily-rollup"},
				From:   "2026-01-01",
				To:     "2026-01-16",
			},
			want: "narrow --from/--to to one Provider request",
		},
		{
			name: "physical range needs multiple requests",
			options: RawRequestOptions{
				Target:         []string{"data-type", "total-calories", "rollup"},
				From:           "2026-01-01T00:00:00Z",
				To:             "2026-01-16T00:00:00Z",
				Window:         "1h",
				WindowProvided: true,
			},
			want: "narrow --from/--to to one Provider request",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.options.ResolvedAt = time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
			_, err := DescribeRawRequest(test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DescribeRawRequest error = %v, want %q", err, test.want)
			}
		})
	}
}
