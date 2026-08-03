package googlehealth

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

type CatalogAuditStatus string

const (
	CatalogVerified              CatalogAuditStatus = "verified"
	CatalogVerifiedWithKnownGaps CatalogAuditStatus = "verified_with_known_gaps"
	CatalogDriftDetected         CatalogAuditStatus = "drift_detected"
)

type CatalogKnownGap struct {
	Kind      string   `json:"kind"`
	DataTypes []string `json:"data_types"`
}

type CatalogUnverifiableFact struct {
	Fact   string `json:"fact"`
	Reason string `json:"reason"`
}

type CatalogDrift struct {
	Kind     string `json:"kind"`
	DataType string `json:"data_type,omitempty"`
}

type CatalogAuditResult struct {
	Status            CatalogAuditStatus        `json:"status"`
	Source            string                    `json:"source"`
	DiscoveryRevision string                    `json:"discovery_revision,omitempty"`
	KnownGaps         []CatalogKnownGap         `json:"known_gaps,omitempty"`
	Unverifiable      []CatalogUnverifiableFact `json:"unverifiable,omitempty"`
	Drift             []CatalogDrift            `json:"drift,omitempty"`
}

type discoveryDocument struct {
	Kind     string                     `json:"kind"`
	Name     string                     `json:"name"`
	Version  string                     `json:"version"`
	Revision string                     `json:"revision"`
	Schemas  map[string]discoverySchema `json:"schemas"`
}

type discoverySchema struct {
	Properties map[string]discoveryProperty `json:"properties"`
}

type discoveryProperty struct {
	Description string `json:"description"`
	Ref         string `json:"$ref"`
	Type        string `json:"type"`
}

type discoveredDataType struct {
	DataType   string
	JSONField  string
	RecordKind string
	SchemaRef  string
	Shape      string
}

var discoveryDataTypeName = regexp.MustCompile("`([a-z0-9-]+)`[^.]*data type collection")

var discoveryDataPointMetadataFields = map[string]bool{
	"dataSource": true,
	"name":       true,
}

// The public Data Types catalog classifies these two non-temporal DataPoint
// union members as independently addressable nutrition Data Types, even though
// their discovery descriptions call them details rather than collections.
var discoveryNonTemporalDataTypes = map[string]string{
	"food":                "food",
	"foodMeasurementUnit": "food-measurement-unit",
}

var catalogKnownGaps = []CatalogKnownGap{
	{Kind: "local_rollup_only", DataTypes: []string{"calories-in-heart-rate-zone", "total-calories"}},
	{Kind: "upstream_raw_only", DataTypes: []string{"food", "food-measurement-unit"}},
}

var catalogUnverifiableFacts = []CatalogUnverifiableFact{
	{
		Fact:   "filter_fields",
		Reason: "the discovery document describes shared filters but not each Data Type's accepted filter field",
	},
	{
		Fact:   "operation_support",
		Reason: "the discovery document lists shared methods but not exact per-Data-Type operation support",
	},
}

// catalogDiscoveryBaseline is a deliberately reduced copy of the public v4
// discovery surface: DataPoint union membership plus each member's temporal
// shape. Those are the only discovery facts the local catalog can verify.
//
//go:embed testdata/google-health-discovery-v4.json
var catalogDiscoveryBaseline []byte

// VerifyCatalogDiscovery compares the canonical local catalog with one Google
// Health v4 discovery document. It is pure: no config, credential, archive, or
// Provider operation is consulted while constructing the result.
func VerifyCatalogDiscovery(payload []byte, source string) CatalogAuditResult {
	result := CatalogAuditResult{Source: source}
	var document discoveryDocument
	if err := json.Unmarshal(payload, &document); err != nil {
		return invalidDiscoveryResult(source)
	}
	if document.Kind != "discovery#restDescription" || document.Name != "health" || document.Version != "v4" {
		return invalidDiscoveryResult(source)
	}

	discovered, err := discoveryDataTypes(document)
	if err != nil {
		return invalidDiscoveryResult(source)
	}
	var baselineDocument discoveryDocument
	if err := json.Unmarshal(catalogDiscoveryBaseline, &baselineDocument); err != nil {
		return invalidDiscoveryResult(source)
	}
	baseline, err := discoveryDataTypes(baselineDocument)
	if err != nil {
		return invalidDiscoveryResult(source)
	}
	result.DiscoveryRevision = document.Revision
	result.KnownGaps = cloneKnownGaps(catalogKnownGaps)
	result.Unverifiable = append([]CatalogUnverifiableFact(nil), catalogUnverifiableFacts...)
	result.Drift = compareCatalog(discovered, baseline, googleHealthDataTypes.entries, googleHealthDataTypes.order)
	result.Status = catalogAuditStatus(result.KnownGaps, result.Drift)
	return result
}

func invalidDiscoveryResult(source string) CatalogAuditResult {
	return CatalogAuditResult{
		Status: CatalogDriftDetected,
		Source: source,
		Drift:  []CatalogDrift{{Kind: "discovery_invalid"}},
	}
}

func discoveryDataTypes(document discoveryDocument) (map[string]discoveredDataType, error) {
	dataPoint, ok := document.Schemas["DataPoint"]
	if !ok || len(dataPoint.Properties) == 0 {
		return nil, fmt.Errorf("discovery document has no DataPoint schema")
	}
	discovered := make(map[string]discoveredDataType)
	for jsonField, property := range dataPoint.Properties {
		dataType, nonTemporal := discoveryNonTemporalDataTypes[jsonField]
		if !nonTemporal {
			match := discoveryDataTypeName.FindStringSubmatch(property.Description)
			if len(match) == 2 {
				dataType = match[1]
			}
		}
		if dataType == "" {
			if discoveryDataPointMetadataFields[jsonField] {
				continue
			}
			return nil, fmt.Errorf("unclassified DataPoint property %q", jsonField)
		}
		if property.Ref == "" {
			return nil, fmt.Errorf("Data Type %q has no schema reference", dataType)
		}
		schema, ok := document.Schemas[property.Ref]
		if !ok {
			return nil, fmt.Errorf("Data Type %q references absent schema", dataType)
		}
		recordKind := "food"
		shape := "non_temporal"
		var err error
		if nonTemporal {
			if len(discoveryTemporalMarkers(schema)) != 0 {
				return nil, fmt.Errorf("non-temporal Data Type %q has a temporal property", dataType)
			}
		} else {
			recordKind, shape, err = discoveryRecordKind(schema)
			if err != nil {
				return nil, fmt.Errorf("Data Type %q: %w", dataType, err)
			}
		}
		if _, duplicate := discovered[dataType]; duplicate {
			return nil, fmt.Errorf("duplicate Data Type %q", dataType)
		}
		discovered[dataType] = discoveredDataType{
			DataType:   dataType,
			JSONField:  jsonField,
			RecordKind: recordKind,
			SchemaRef:  property.Ref,
			Shape:      shape,
		}
	}
	if len(discovered) == 0 {
		return nil, fmt.Errorf("discovery document has no raw Data Types")
	}
	return discovered, nil
}

func discoveryRecordKind(schema discoverySchema) (string, string, error) {
	markers := discoveryTemporalMarkers(schema)
	if len(markers) != 1 {
		return "", "", fmt.Errorf("schema has %d temporal properties; want exactly one", len(markers))
	}
	marker := markers[0]
	recordKind := marker.recordKind
	if marker.name == "interval" && marker.property.Ref == "SessionTimeInterval" {
		recordKind = "session"
	}
	return recordKind, discoveryPropertyShape(marker.name, marker.property), nil
}

type discoveryTemporalMarker struct {
	name       string
	recordKind string
	property   discoveryProperty
}

func discoveryTemporalMarkers(schema discoverySchema) []discoveryTemporalMarker {
	markers := make([]discoveryTemporalMarker, 0, 3)
	for _, marker := range []discoveryTemporalMarker{
		{name: "date", recordKind: "daily"},
		{name: "sampleTime", recordKind: "sample"},
		{name: "interval", recordKind: "interval"},
	} {
		property, ok := schema.Properties[marker.name]
		if !ok {
			continue
		}
		marker.property = property
		markers = append(markers, marker)
	}
	return markers
}

func discoveryPropertyShape(name string, property discoveryProperty) string {
	return name + ":" + property.Ref + ":" + property.Type
}

func compareCatalog(
	discovered, baseline map[string]discoveredDataType,
	localEntries map[string]googleHealthDataTypeCatalogEntry,
	localOrder []string,
) []CatalogDrift {
	return compareCatalogWithKnownGaps(discovered, baseline, localEntries, localOrder, catalogKnownGaps)
}

func compareCatalogWithKnownGaps(
	discovered, baseline map[string]discoveredDataType,
	localEntries map[string]googleHealthDataTypeCatalogEntry,
	localOrder []string,
	knownGaps []CatalogKnownGap,
) []CatalogDrift {
	localRollupOnly := knownGapDataTypes(knownGaps, "local_rollup_only")
	upstreamRawOnly := knownGapDataTypes(knownGaps, "upstream_raw_only")
	drift := make([]CatalogDrift, 0)
	seen := make(map[string]bool)
	for dataType := range localRollupOnly {
		_, local := localEntries[dataType]
		_, upstreamRaw := discovered[dataType]
		if !local || upstreamRaw {
			drift = appendCatalogDrift(drift, seen, "known_gap_stale", dataType)
		}
	}
	for dataType := range upstreamRawOnly {
		_, local := localEntries[dataType]
		_, upstreamRaw := discovered[dataType]
		if local || !upstreamRaw {
			drift = appendCatalogDrift(drift, seen, "known_gap_stale", dataType)
		}
	}
	for dataType, expected := range baseline {
		if _, local := localEntries[dataType]; !local && !upstreamRawOnly[dataType] {
			drift = appendCatalogDrift(drift, seen, "local_raw_missing", dataType)
		}
		upstream, ok := discovered[dataType]
		if !ok {
			drift = appendCatalogDrift(drift, seen, "upstream_raw_removed", dataType)
			continue
		}
		if upstream.JSONField != expected.JSONField {
			drift = appendCatalogDrift(drift, seen, "json_field_changed", dataType)
		}
		if upstream.RecordKind != expected.RecordKind {
			drift = appendCatalogDrift(drift, seen, "record_kind_changed", dataType)
		}
		if upstream.SchemaRef != expected.SchemaRef {
			drift = appendCatalogDrift(drift, seen, "schema_reference_changed", dataType)
		}
		if upstream.Shape != expected.Shape {
			drift = appendCatalogDrift(drift, seen, "schema_shape_changed", dataType)
		}
	}
	for dataType := range discovered {
		if _, ok := baseline[dataType]; !ok {
			drift = appendCatalogDrift(drift, seen, "upstream_raw_added", dataType)
		}
	}

	for _, dataType := range localOrder {
		if localRollupOnly[dataType] {
			continue
		}
		entry, local := localEntries[dataType]
		if !local {
			continue
		}
		upstream, ok := discovered[dataType]
		if !ok {
			if _, expected := baseline[dataType]; !expected {
				drift = appendCatalogDrift(drift, seen, "local_raw_unrepresented", dataType)
			}
			continue
		}
		if upstream.JSONField != entry.JSONField {
			drift = appendCatalogDrift(drift, seen, "json_field_changed", dataType)
		}
		if upstream.RecordKind != entry.RecordKind {
			drift = appendCatalogDrift(drift, seen, "record_kind_changed", dataType)
		}
	}
	sort.Slice(drift, func(i, j int) bool {
		if drift[i].DataType != drift[j].DataType {
			return drift[i].DataType < drift[j].DataType
		}
		return drift[i].Kind < drift[j].Kind
	})
	return drift
}

func knownGapDataTypes(knownGaps []CatalogKnownGap, kind string) map[string]bool {
	for _, gap := range knownGaps {
		if gap.Kind == kind {
			return stringSet(gap.DataTypes)
		}
	}
	return map[string]bool{}
}

func appendCatalogDrift(drift []CatalogDrift, seen map[string]bool, kind, dataType string) []CatalogDrift {
	key := kind + "\x00" + dataType
	if seen[key] {
		return drift
	}
	seen[key] = true
	return append(drift, CatalogDrift{Kind: kind, DataType: dataType})
}

func catalogAuditStatus(knownGaps []CatalogKnownGap, drift []CatalogDrift) CatalogAuditStatus {
	if len(drift) != 0 {
		return CatalogDriftDetected
	}
	if len(knownGaps) != 0 {
		return CatalogVerifiedWithKnownGaps
	}
	return CatalogVerified
}

func cloneKnownGaps(gaps []CatalogKnownGap) []CatalogKnownGap {
	cloned := make([]CatalogKnownGap, len(gaps))
	for i, gap := range gaps {
		cloned[i] = CatalogKnownGap{Kind: gap.Kind, DataTypes: append([]string(nil), gap.DataTypes...)}
	}
	return cloned
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}
