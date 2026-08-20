package googlehealth

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogDataTypeDescriptionProjectsCompiledFacts(t *testing.T) {
	t.Parallel()

	steps, err := CatalogDataTypeDescription("steps")
	if err != nil {
		t.Fatalf("describe steps: %v", err)
	}
	wantSteps := CatalogCompiledDescription{
		Source:         "compiled_catalog",
		RequiredScopes: []string{ScopeActivityReadonly},
		RecordKind:     "interval",
		EndpointFamilies: []CatalogEndpointFamily{
			{
				Name:        "list",
				FilterField: "steps.interval.start_time",
				RangeShape:  "physical",
				PagePolicy:  CatalogPagePolicy{Pagination: "nextPageToken", PageSize: 10000, PageSizePolicy: "explicit"},
			},
			{
				Name:        "reconcile",
				FilterField: "steps.interval.start_time",
				RangeShape:  "physical",
				PagePolicy:  CatalogPagePolicy{Pagination: "nextPageToken", PageSize: 10000, PageSizePolicy: "explicit"},
			},
			{
				Name:       "rollUp",
				RangeShape: "physical",
				PagePolicy: CatalogPagePolicy{Pagination: "nextPageToken", PageSizePolicy: "provider_default"},
			},
			{
				Name:       "dailyRollUp",
				RangeShape: "daily",
				PagePolicy: CatalogPagePolicy{Pagination: "nextPageToken", PageSizePolicy: "provider_default", RangeWindowMaxDays: 90},
			},
		},
		RollupModes: []string{"daily", "hourly", "weekly", "window=<duration>"},
	}
	if steps.DataType != "steps" || !reflect.DeepEqual(steps.Compiled, wantSteps) {
		t.Fatalf("steps description = %#v, want compiled %#v", steps, wantSteps)
	}

	ecg, err := CatalogDataTypeDescription("electrocardiogram")
	if err != nil {
		t.Fatalf("describe electrocardiogram: %v", err)
	}
	if len(ecg.Compiled.EndpointFamilies) != 1 || !ecg.Compiled.EndpointFamilies[0].LowerBoundOnly {
		t.Fatalf("ECG endpoint families = %#v, want one lower-bound-only list endpoint", ecg.Compiled.EndpointFamilies)
	}

	totalCalories, err := CatalogDataTypeDescription("total-calories")
	if err != nil {
		t.Fatalf("describe total-calories: %v", err)
	}
	if totalCalories.Compiled.RecordKind != "none" {
		t.Errorf("total-calories record kind = %q, want none", totalCalories.Compiled.RecordKind)
	}
	if got := totalCalories.Compiled.EndpointFamilies; len(got) != 2 || got[0].Name != "rollUp" || got[0].PagePolicy.RangeWindowMaxDays != 14 || got[1].Name != "dailyRollUp" || got[1].PagePolicy.RangeWindowMaxDays != 14 {
		t.Errorf("total-calories endpoint families = %#v, want 14-day rollUp and dailyRollUp policies", got)
	}
}

func TestCatalogDataTypeDescriptionRejectsUnknownDataType(t *testing.T) {
	t.Parallel()

	_, err := CatalogDataTypeDescription("not-a-data-type")
	if !errors.Is(err, ErrCatalogDataTypeUnknown) {
		t.Fatalf("unknown Data Type error = %v, want ErrCatalogDataTypeUnknown", err)
	}
}

func TestEnrichCatalogDataTypeDescriptionUsesDiscoveryFieldsOnly(t *testing.T) {
	t.Parallel()

	description, err := CatalogDataTypeDescription("steps")
	if err != nil {
		t.Fatalf("compiled description: %v", err)
	}
	payload := []byte(`{
  "kind":"discovery#restDescription",
  "name":"health",
  "version":"v4",
  "revision":"synthetic-1",
  "schemas":{
    "DataPoint":{"properties":{"steps":{"$ref":"Steps","description":"Data for points in the ` + "`steps`" + ` interval data type collection."}}},
    "Steps":{"properties":{
      "tags":{"type":"array"},
      "interval":{"$ref":"ObservationTimeInterval"},
      "count":{"type":"string"}
    }}
  }
}`)
	got, err := EnrichCatalogDataTypeDescription(description, payload, "file")
	if err != nil {
		t.Fatalf("enrich steps: %v", err)
	}
	wantDiscovery := &CatalogDiscoveryDescription{
		Source:    "file",
		Revision:  "synthetic-1",
		JSONField: "steps",
		SchemaRef: "Steps",
		Fields: []CatalogDiscoveryField{
			{Name: "count", JSONType: "string"},
			{Name: "interval", JSONType: "object", SchemaRef: "ObservationTimeInterval"},
			{Name: "tags", JSONType: "array"},
		},
	}
	if !reflect.DeepEqual(got.Discovery, wantDiscovery) {
		t.Fatalf("discovery = %#v, want %#v", got.Discovery, wantDiscovery)
	}
	if !reflect.DeepEqual(got.Compiled, description.Compiled) {
		t.Fatalf("compiled facts changed during enrichment: got %#v want %#v", got.Compiled, description.Compiled)
	}
}

func TestEnrichCatalogDataTypeDescriptionUsesRollupSchemaForRollupOnlyType(t *testing.T) {
	t.Parallel()

	description, err := CatalogDataTypeDescription("total-calories")
	if err != nil {
		t.Fatalf("compiled description: %v", err)
	}
	payload := []byte(`{
  "kind":"discovery#restDescription",
  "name":"health",
  "version":"v4",
  "revision":"synthetic-rollup",
  "schemas":{
    "DataPoint":{"properties":{"steps":{"$ref":"Steps","description":"Data for points in the ` + "`steps`" + ` interval data type collection."}}},
    "Steps":{"properties":{"interval":{"$ref":"ObservationTimeInterval"}}},
    "RollupDataPoint":{"properties":{"totalCalories":{"$ref":"TotalCaloriesRollupValue"}}},
    "TotalCaloriesRollupValue":{"properties":{"kcalSum":{"type":"number"}}}
  }
}`)
	got, err := EnrichCatalogDataTypeDescription(description, payload, "live")
	if err != nil {
		t.Fatalf("enrich total-calories: %v", err)
	}
	if got.Discovery.JSONField != "totalCalories" || got.Discovery.SchemaRef != "TotalCaloriesRollupValue" || !reflect.DeepEqual(got.Discovery.Fields, []CatalogDiscoveryField{{Name: "kcalSum", JSONType: "number"}}) {
		t.Fatalf("total-calories discovery = %#v", got.Discovery)
	}
}

func TestEnrichCatalogDataTypeDescriptionRejectsMalformedAndCanonicalOverride(t *testing.T) {
	t.Parallel()

	description, err := CatalogDataTypeDescription("steps")
	if err != nil {
		t.Fatalf("compiled description: %v", err)
	}
	if _, err := EnrichCatalogDataTypeDescription(description, []byte(`{"broken":`), "file"); !errors.Is(err, ErrCatalogDiscoveryMalformed) {
		t.Fatalf("malformed error = %v, want ErrCatalogDiscoveryMalformed", err)
	}
	override := []byte(`{
  "kind":"discovery#restDescription",
  "name":"health",
  "version":"v4",
  "revision":"synthetic-override",
  "schemas":{
    "DataPoint":{"properties":{"steps":{"$ref":"Steps","description":"Data for points in the ` + "`steps`" + ` sample data type collection."}}},
    "Steps":{"properties":{"sampleTime":{"$ref":"ObservationSampleTime"},"count":{"type":"string"}}}
  }
}`)
	if _, err := EnrichCatalogDataTypeDescription(description, override, "file"); !errors.Is(err, ErrCatalogDiscoveryIncompatible) {
		t.Fatalf("override error = %v, want ErrCatalogDiscoveryIncompatible", err)
	}
}

func TestEnrichCatalogDataTypeDescriptionRejectsRawToRollupFallback(t *testing.T) {
	t.Parallel()

	description, err := CatalogDataTypeDescription("steps")
	if err != nil {
		t.Fatalf("compiled description: %v", err)
	}
	payload := []byte(`{
  "kind":"discovery#restDescription",
  "name":"health",
  "version":"v4",
  "revision":"synthetic-missing-raw",
  "schemas":{
    "DataPoint":{"properties":{}},
    "RollupDataPoint":{"properties":{"steps":{"$ref":"StepsRollupValue"}}},
    "StepsRollupValue":{"properties":{"count":{"type":"string"}}}
  }
}`)
	if _, err := EnrichCatalogDataTypeDescription(description, payload, "file"); !errors.Is(err, ErrCatalogDiscoveryIncompatible) {
		t.Fatalf("missing raw union error = %v, want ErrCatalogDiscoveryIncompatible", err)
	}
}

func TestCatalogDiscoverySnapshotDescribesEveryCompiledDataType(t *testing.T) {
	t.Parallel()

	payload := CatalogDiscoverySnapshot()
	for _, summary := range CatalogDataTypes() {
		description, err := CatalogDataTypeDescription(summary.DataType)
		if err != nil {
			t.Fatalf("compiled %s: %v", summary.DataType, err)
		}
		description, err = EnrichCatalogDataTypeDescription(description, payload, "committed_snapshot")
		if err != nil {
			t.Errorf("embedded discovery %s: %v", summary.DataType, err)
			continue
		}
		if description.Discovery.Revision != "20260817" || len(description.Discovery.Fields) == 0 {
			t.Errorf("embedded discovery %s = %#v, want revision and fields", summary.DataType, description.Discovery)
		}
	}

	steps, _ := CatalogDataTypeDescription("steps")
	steps, err := EnrichCatalogDataTypeDescription(steps, payload, "committed_snapshot")
	if err != nil {
		t.Fatalf("embedded steps: %v", err)
	}
	var fieldNames []string
	for _, field := range steps.Discovery.Fields {
		fieldNames = append(fieldNames, field.Name+":"+field.JSONType)
	}
	if !strings.Contains(strings.Join(fieldNames, ","), "count:string") {
		t.Fatalf("embedded steps fields = %v, want count:string from revision 20260817", fieldNames)
	}
}
