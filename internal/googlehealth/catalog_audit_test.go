package googlehealth

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
)

func TestVerifyCatalogDiscoveryDetectsSchemaReferenceChange(t *testing.T) {
	t.Parallel()

	document := loadCatalogDiscoveryFixture(t)
	dataPoint := document["schemas"].(map[string]any)["DataPoint"].(map[string]any)
	properties := dataPoint["properties"].(map[string]any)
	properties["steps"].(map[string]any)["$ref"] = "Distance"

	result := VerifyCatalogDiscovery(marshalCatalogDiscovery(t, document), "file")
	want := []CatalogDrift{{Kind: "schema_reference_changed", DataType: "steps"}}
	if len(result.Drift) != len(want) || result.Drift[0] != want[0] {
		t.Fatalf("drift = %#v, want %#v", result.Drift, want)
	}
	if result.Status != CatalogDriftDetected {
		t.Errorf("status = %q, want %q", result.Status, CatalogDriftDetected)
	}
}

func TestVerifyCatalogDiscoveryDetectsTemporalSchemaChange(t *testing.T) {
	t.Parallel()

	document := loadCatalogDiscoveryFixture(t)
	schemas := document["schemas"].(map[string]any)
	steps := schemas["Steps"].(map[string]any)
	properties := steps["properties"].(map[string]any)
	properties["interval"].(map[string]any)["$ref"] = "ChangedInterval"

	result := VerifyCatalogDiscovery(marshalCatalogDiscovery(t, document), "file")
	want := []CatalogDrift{{Kind: "schema_shape_changed", DataType: "steps"}}
	if len(result.Drift) != len(want) || result.Drift[0] != want[0] {
		t.Fatalf("drift = %#v, want %#v", result.Drift, want)
	}
}

func TestVerifyCatalogDiscoveryRejectsTemporalFoodSchema(t *testing.T) {
	t.Parallel()

	document := loadCatalogDiscoveryFixture(t)
	schemas := document["schemas"].(map[string]any)
	food := schemas["Food"].(map[string]any)
	food["properties"].(map[string]any)["interval"] = map[string]any{"$ref": "ObservationTimeInterval"}

	result := VerifyCatalogDiscovery(marshalCatalogDiscovery(t, document), "file")
	assertInvalidDiscoveryResult(t, result)
}

func TestVerifyCatalogDiscoveryRejectsMultipleTemporalMarkers(t *testing.T) {
	t.Parallel()

	document := loadCatalogDiscoveryFixture(t)
	schemas := document["schemas"].(map[string]any)
	dailyVO2Max := schemas["DailyVO2Max"].(map[string]any)
	dailyVO2Max["properties"].(map[string]any)["interval"] = map[string]any{"$ref": "ObservationTimeInterval"}

	result := VerifyCatalogDiscovery(marshalCatalogDiscovery(t, document), "file")
	assertInvalidDiscoveryResult(t, result)
}

func assertInvalidDiscoveryResult(t *testing.T, result CatalogAuditResult) {
	t.Helper()
	want := CatalogAuditResult{
		Status: CatalogDriftDetected,
		Source: "file",
		Drift:  []CatalogDrift{{Kind: "discovery_invalid"}},
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %#v, want %#v", result, want)
	}
}

func TestVerifyCatalogDiscoveryMutationDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   []CatalogDrift
	}{
		{
			name: "addition",
			mutate: func(document map[string]any) {
				schemas := document["schemas"].(map[string]any)
				properties := schemas["DataPoint"].(map[string]any)["properties"].(map[string]any)
				properties["newType"] = map[string]any{
					"$ref":        "NewType",
					"description": "Data for points in the `new-type` interval data type collection.",
				}
				schemas["NewType"] = map[string]any{"properties": map[string]any{
					"interval": map[string]any{"$ref": "ObservationTimeInterval"},
				}}
			},
			want: []CatalogDrift{{Kind: "upstream_raw_added", DataType: "new-type"}},
		},
		{
			name: "removal",
			mutate: func(document map[string]any) {
				schemas := document["schemas"].(map[string]any)
				properties := schemas["DataPoint"].(map[string]any)["properties"].(map[string]any)
				delete(properties, "steps")
			},
			want: []CatalogDrift{{Kind: "upstream_raw_removed", DataType: "steps"}},
		},
		{
			name: "record kind",
			mutate: func(document map[string]any) {
				schemas := document["schemas"].(map[string]any)
				steps := schemas["Steps"].(map[string]any)
				steps["properties"].(map[string]any)["interval"].(map[string]any)["$ref"] = "SessionTimeInterval"
			},
			want: []CatalogDrift{
				{Kind: "record_kind_changed", DataType: "steps"},
				{Kind: "schema_shape_changed", DataType: "steps"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			document := loadCatalogDiscoveryFixture(t)
			test.mutate(document)
			result := VerifyCatalogDiscovery(marshalCatalogDiscovery(t, document), "file")
			if !reflect.DeepEqual(result.Drift, test.want) {
				t.Errorf("drift = %#v, want %#v", result.Drift, test.want)
			}
			if result.Status != CatalogDriftDetected {
				t.Errorf("status = %q, want %q", result.Status, CatalogDriftDetected)
			}
		})
	}
}

func TestVerifyCatalogDiscoveryMalformedDocument(t *testing.T) {
	t.Parallel()

	result := VerifyCatalogDiscovery([]byte(`{"not":"complete"`), "file")
	want := CatalogAuditResult{
		Status: CatalogDriftDetected,
		Source: "file",
		Drift:  []CatalogDrift{{Kind: "discovery_invalid"}},
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %#v, want %#v", result, want)
	}
}

func TestVerifyCatalogDiscoverySortsDrift(t *testing.T) {
	t.Parallel()

	document := loadCatalogDiscoveryFixture(t)
	schemas := document["schemas"].(map[string]any)
	properties := schemas["DataPoint"].(map[string]any)["properties"].(map[string]any)
	for _, dataType := range []string{"zeta-type", "alpha-type"} {
		ref := dataType + "Schema"
		properties[dataType] = map[string]any{
			"$ref":        ref,
			"description": "Data for points in the `" + dataType + "` interval data type collection.",
		}
		schemas[ref] = map[string]any{"properties": map[string]any{
			"interval": map[string]any{"$ref": "ObservationTimeInterval"},
		}}
	}

	result := VerifyCatalogDiscovery(marshalCatalogDiscovery(t, document), "file")
	want := []CatalogDrift{
		{Kind: "upstream_raw_added", DataType: "alpha-type"},
		{Kind: "upstream_raw_added", DataType: "zeta-type"},
	}
	if !reflect.DeepEqual(result.Drift, want) {
		t.Errorf("drift = %#v, want %#v", result.Drift, want)
	}
}

func TestVerifyCatalogDiscoveryRejectsUnclassifiedUnionMember(t *testing.T) {
	t.Parallel()

	document := loadCatalogDiscoveryFixture(t)
	schemas := document["schemas"].(map[string]any)
	properties := schemas["DataPoint"].(map[string]any)["properties"].(map[string]any)
	properties["mysteryMetric"] = map[string]any{
		"$ref":        "MysteryMetric",
		"description": "A newly worded union member.",
	}
	schemas["MysteryMetric"] = map[string]any{"properties": map[string]any{
		"interval": map[string]any{"$ref": "ObservationTimeInterval"},
	}}

	result := VerifyCatalogDiscovery(marshalCatalogDiscovery(t, document), "file")
	want := CatalogAuditResult{
		Status: CatalogDriftDetected,
		Source: "file",
		Drift:  []CatalogDrift{{Kind: "discovery_invalid"}},
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %#v, want %#v", result, want)
	}
}

func TestCompareCatalogDetectsOrdinaryTypeMissingLocally(t *testing.T) {
	t.Parallel()

	document := loadCatalogDiscoveryDocument(t)
	discovered, err := discoveryDataTypes(document)
	if err != nil {
		t.Fatalf("parse discovery fixture: %v", err)
	}
	localEntries := make(map[string]googleHealthDataTypeCatalogEntry, len(googleHealthDataTypes.entries))
	for dataType, entry := range googleHealthDataTypes.entries {
		localEntries[dataType] = entry
	}
	delete(localEntries, "steps")

	drift := compareCatalog(discovered, discovered, localEntries, googleHealthDataTypes.order)
	want := []CatalogDrift{{Kind: "local_raw_missing", DataType: "steps"}}
	if !reflect.DeepEqual(drift, want) {
		t.Errorf("drift = %#v, want %#v", drift, want)
	}
}

func TestCompareCatalogDetectsStaleKnownGapDeclarations(t *testing.T) {
	t.Parallel()

	document := loadCatalogDiscoveryDocument(t)
	discovered, err := discoveryDataTypes(document)
	if err != nil {
		t.Fatalf("parse discovery fixture: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(map[string]googleHealthDataTypeCatalogEntry)
		want   CatalogDrift
	}{
		{
			name: "declared local Rollup-only type absent locally",
			mutate: func(local map[string]googleHealthDataTypeCatalogEntry) {
				delete(local, "total-calories")
			},
			want: CatalogDrift{Kind: "known_gap_stale", DataType: "total-calories"},
		},
		{
			name: "declared upstream-only raw type now local",
			mutate: func(local map[string]googleHealthDataTypeCatalogEntry) {
				local["food"] = googleHealthDataTypeCatalogEntry{
					DataType:   "food",
					JSONField:  "food",
					RecordKind: "catalog",
				}
			},
			want: CatalogDrift{Kind: "known_gap_stale", DataType: "food"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			local := make(map[string]googleHealthDataTypeCatalogEntry, len(googleHealthDataTypes.entries))
			for dataType, entry := range googleHealthDataTypes.entries {
				local[dataType] = entry
			}
			test.mutate(local)
			order := append([]string(nil), googleHealthDataTypes.order...)
			if _, added := local["food"]; added {
				order = append(order, "food")
			}
			drift := compareCatalog(discovered, discovered, local, order)
			if !containsCatalogDrift(drift, test.want) {
				t.Errorf("drift = %#v, want it to contain %#v", drift, test.want)
			}
		})
	}
}

func TestCompareCatalogKnownGapOrderDoesNotChangeAudit(t *testing.T) {
	t.Parallel()

	document := loadCatalogDiscoveryDocument(t)
	discovered, err := discoveryDataTypes(document)
	if err != nil {
		t.Fatalf("parse discovery fixture: %v", err)
	}
	reordered := []CatalogKnownGap{catalogKnownGaps[1], catalogKnownGaps[0]}

	got := compareCatalogWithKnownGaps(
		discovered,
		discovered,
		googleHealthDataTypes.entries,
		googleHealthDataTypes.order,
		reordered,
	)
	if len(got) != 0 {
		t.Errorf("drift = %#v, want none", got)
	}
}

func containsCatalogDrift(drift []CatalogDrift, want CatalogDrift) bool {
	for _, item := range drift {
		if item == want {
			return true
		}
	}
	return false
}

func TestCatalogAuditStatusValues(t *testing.T) {
	t.Parallel()

	if got := catalogAuditStatus(nil, nil); got != CatalogVerified {
		t.Errorf("no gaps or drift status = %q, want %q", got, CatalogVerified)
	}
	if got := catalogAuditStatus(catalogKnownGaps, nil); got != CatalogVerifiedWithKnownGaps {
		t.Errorf("known gaps status = %q, want %q", got, CatalogVerifiedWithKnownGaps)
	}
	if got := catalogAuditStatus(nil, []CatalogDrift{{Kind: "test"}}); got != CatalogDriftDetected {
		t.Errorf("drift status = %q, want %q", got, CatalogDriftDetected)
	}
}

func TestCatalogAuditDoesNotSuppressElectrocardiogramContractAsKnownGap(t *testing.T) {
	t.Parallel()
	for _, gap := range catalogKnownGaps {
		for _, dataType := range gap.DataTypes {
			if dataType == "electrocardiogram" {
				t.Fatalf("ECG contract mismatch was suppressed as known gap %q", gap.Kind)
			}
		}
	}
	if !slices.ContainsFunc(catalogUnverifiableFacts, func(fact CatalogUnverifiableFact) bool {
		return fact.Fact == "filter_fields"
	}) {
		t.Fatal("catalog audit must continue to report general per-Data-Type filter fields as unverifiable")
	}
}

func loadCatalogDiscoveryFixture(t *testing.T) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("testdata/google-health-discovery-v4.json")
	if err != nil {
		t.Fatalf("read discovery fixture: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("decode discovery fixture: %v", err)
	}
	return document
}

func loadCatalogDiscoveryDocument(t *testing.T) discoveryDocument {
	t.Helper()
	payload, err := os.ReadFile("testdata/google-health-discovery-v4.json")
	if err != nil {
		t.Fatalf("read discovery fixture: %v", err)
	}
	var document discoveryDocument
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("decode discovery fixture: %v", err)
	}
	return document
}

func marshalCatalogDiscovery(t *testing.T, document map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal discovery fixture: %v", err)
	}
	return payload
}
