package googlehealth

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

var (
	// ErrCatalogDataTypeUnknown identifies a name outside the compiled catalog.
	ErrCatalogDataTypeUnknown = errors.New("Data Type is not in the compiled catalog")
	// ErrCatalogDiscoveryMalformed identifies invalid discovery JSON.
	ErrCatalogDiscoveryMalformed = errors.New("discovery document is malformed")
	// ErrCatalogDiscoveryIncompatible identifies discovery metadata that cannot
	// safely enrich the selected compiled Data Type.
	ErrCatalogDiscoveryIncompatible = errors.New("discovery document is incompatible")
)

// CatalogDescription is the detailed read-only contract for one compiled Data
// Type. Discovery is added separately so it cannot replace compiled facts.
type CatalogDescription struct {
	DataType  string                       `json:"data_type"`
	Compiled  CatalogCompiledDescription   `json:"compiled"`
	Discovery *CatalogDiscoveryDescription `json:"discovery,omitempty"`
}

// CatalogCompiledDescription projects the canonical Provider catalog facts.
type CatalogCompiledDescription struct {
	Source           string                  `json:"source"`
	EndpointFamilies []CatalogEndpointFamily `json:"endpoint_families"`
	RequiredScopes   []string                `json:"required_scopes"`
	RecordKind       string                  `json:"record_kind"`
	RollupModes      []string                `json:"rollup_modes"`
}

// CatalogEndpointFamily describes one operation family supported by a Data Type.
type CatalogEndpointFamily struct {
	Name           string            `json:"name"`
	FilterField    string            `json:"filter_field,omitempty"`
	LowerBoundOnly bool              `json:"lower_bound_only"`
	RangeShape     string            `json:"range_shape"`
	PagePolicy     CatalogPagePolicy `json:"page_policy"`
}

// CatalogPagePolicy is the static pagination and range-splitting contract.
type CatalogPagePolicy struct {
	Pagination         string `json:"pagination"`
	PageSize           int64  `json:"page_size"`
	PageSizePolicy     string `json:"page_size_policy"`
	RangeWindowMaxDays int    `json:"range_window_max_days,omitempty"`
}

// CatalogDiscoveryDescription is populated by discovery enrichment.
type CatalogDiscoveryDescription struct {
	Source    string                  `json:"source"`
	Revision  string                  `json:"revision"`
	JSONField string                  `json:"json_field"`
	SchemaRef string                  `json:"schema_ref"`
	Fields    []CatalogDiscoveryField `json:"fields"`
}

// CatalogDiscoveryField names one JSON property from the discovery schema.
type CatalogDiscoveryField struct {
	Name      string `json:"name"`
	JSONType  string `json:"json_type"`
	SchemaRef string `json:"schema_ref,omitempty"`
}

var catalogEndpointOrder = []endpointFamily{
	endpointFamilyList,
	endpointFamilyGet,
	endpointFamilyReconcile,
	endpointFamilyRollUp,
	endpointFamilyDailyRollUp,
}

// CatalogDataTypeDescription projects one entry from the canonical compiled
// catalog without consulting discovery, config, credentials, or an archive.
func CatalogDataTypeDescription(dataType string) (CatalogDescription, error) {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if !ok {
		return CatalogDescription{}, fmt.Errorf("%w: %q", ErrCatalogDataTypeUnknown, dataType)
	}
	recordKind := entry.RecordKind
	if recordKind == "" {
		recordKind = "none"
	}
	description := CatalogDescription{
		DataType: dataType,
		Compiled: CatalogCompiledDescription{
			Source:           "compiled_catalog",
			EndpointFamilies: make([]CatalogEndpointFamily, 0, len(entry.SupportedEndpoints)),
			RequiredScopes:   append([]string(nil), entry.RequiredScopes...),
			RecordKind:       recordKind,
			RollupModes:      []string{},
		},
	}
	for _, family := range catalogEndpointOrder {
		support, supported := entry.SupportedEndpoints[family]
		if !supported {
			continue
		}
		endpoint, err := catalogEndpointDescription(dataType, family, support)
		if err != nil {
			return CatalogDescription{}, err
		}
		description.Compiled.EndpointFamilies = append(description.Compiled.EndpointFamilies, endpoint)
	}
	if _, ok := entry.SupportedEndpoints[endpointFamilyDailyRollUp]; ok {
		description.Compiled.RollupModes = append(description.Compiled.RollupModes, "daily")
	}
	if _, ok := entry.SupportedEndpoints[endpointFamilyRollUp]; ok {
		description.Compiled.RollupModes = append(description.Compiled.RollupModes, "hourly", "weekly", "window=<duration>")
	}
	return description, nil
}

func catalogEndpointDescription(dataType string, family endpointFamily, support endpointSupport) (CatalogEndpointFamily, error) {
	endpoint := CatalogEndpointFamily{
		Name:           string(family),
		FilterField:    support.FilterField,
		LowerBoundOnly: support.LowerBoundOnly,
		RangeShape:     string(RangeTargetPhysical),
		PagePolicy: CatalogPagePolicy{
			Pagination:     "nextPageToken",
			PageSizePolicy: "provider_default",
		},
	}
	switch family {
	case endpointFamilyList, endpointFamilyReconcile:
		reconcile := family == endpointFamilyReconcile
		target, err := SyncRangeTarget(dataType, nil, reconcile)
		if err != nil {
			return CatalogEndpointFamily{}, fmt.Errorf("describe %s %s range: %w", dataType, family, err)
		}
		endpoint.RangeShape = string(target)
		endpoint.PagePolicy.PageSize = syncDataPointPageSize(dataType)
		endpoint.PagePolicy.PageSizePolicy = "explicit"
	case endpointFamilyGet:
		endpoint.RangeShape = "none"
		endpoint.PagePolicy.Pagination = "none"
		endpoint.PagePolicy.PageSizePolicy = "not_applicable"
	case endpointFamilyDailyRollUp:
		endpoint.RangeShape = string(RangeTargetDaily)
		endpoint.PagePolicy.RangeWindowMaxDays = googleHealthDailyRollupMaxRangeDays(dataType)
	case endpointFamilyRollUp:
		endpoint.PagePolicy.RangeWindowMaxDays = googleHealthRollupMaxRangeDays(dataType)
	}
	return endpoint, nil
}

// EnrichCatalogDataTypeDescription adds only field-shape facts from one
// discovery document. It validates discovery's record shape against the
// compiled entry and never writes into the Compiled group.
func EnrichCatalogDataTypeDescription(description CatalogDescription, payload []byte, source string) (CatalogDescription, error) {
	entry, ok := googleHealthDataTypes.Lookup(description.DataType)
	if !ok {
		return CatalogDescription{}, fmt.Errorf("%w: %q", ErrCatalogDataTypeUnknown, description.DataType)
	}
	var document discoveryDocument
	if err := json.Unmarshal(payload, &document); err != nil {
		return CatalogDescription{}, ErrCatalogDiscoveryMalformed
	}
	if document.Kind != "discovery#restDescription" || document.Name != "health" || document.Version != "v4" || document.Revision == "" {
		return CatalogDescription{}, ErrCatalogDiscoveryIncompatible
	}
	jsonField, schemaRef, schema, err := discoverySchemaForCatalogEntry(document, entry)
	if err != nil {
		return CatalogDescription{}, err
	}
	fieldNames := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)
	fields := make([]CatalogDiscoveryField, 0, len(fieldNames))
	for _, name := range fieldNames {
		property := schema.Properties[name]
		jsonType := property.Type
		if jsonType == "" && property.Ref != "" {
			jsonType = "object"
		}
		if jsonType == "" {
			return CatalogDescription{}, ErrCatalogDiscoveryIncompatible
		}
		fields = append(fields, CatalogDiscoveryField{
			Name:      name,
			JSONType:  jsonType,
			SchemaRef: property.Ref,
		})
	}
	if len(fields) == 0 {
		return CatalogDescription{}, ErrCatalogDiscoveryIncompatible
	}
	description.Discovery = &CatalogDiscoveryDescription{
		Source:    source,
		Revision:  document.Revision,
		JSONField: jsonField,
		SchemaRef: schemaRef,
		Fields:    fields,
	}
	return description, nil
}

func discoverySchemaForCatalogEntry(document discoveryDocument, entry googleHealthDataTypeCatalogEntry) (string, string, discoverySchema, error) {
	if entry.JSONField != "" {
		if dataPoint, ok := document.Schemas["DataPoint"]; ok {
			if property, ok := dataPoint.Properties[entry.JSONField]; ok {
				match := discoveryDataTypeName.FindStringSubmatch(property.Description)
				if len(match) != 2 || match[1] != entry.DataType || property.Ref == "" {
					return "", "", discoverySchema{}, ErrCatalogDiscoveryIncompatible
				}
				schema, ok := document.Schemas[property.Ref]
				if !ok {
					return "", "", discoverySchema{}, ErrCatalogDiscoveryIncompatible
				}
				recordKind, _, err := discoveryRecordKind(schema)
				if err != nil || entry.RecordKind == "" || recordKind != entry.RecordKind {
					return "", "", discoverySchema{}, ErrCatalogDiscoveryIncompatible
				}
				return entry.JSONField, property.Ref, schema, nil
			}
		}
		if _, supportsList := entry.SupportedEndpoints[endpointFamilyList]; supportsList {
			return "", "", discoverySchema{}, ErrCatalogDiscoveryIncompatible
		}
		if _, supportsReconcile := entry.SupportedEndpoints[endpointFamilyReconcile]; supportsReconcile {
			return "", "", discoverySchema{}, ErrCatalogDiscoveryIncompatible
		}
	}

	candidates := make([]string, 0, len(entry.SupportedEndpoints)+1)
	if entry.JSONField != "" {
		candidates = append(candidates, entry.JSONField)
	}
	for _, family := range catalogEndpointOrder {
		valueType := entry.SupportedEndpoints[family].RollupValueType
		if valueType != "" {
			candidates = append(candidates, valueType)
		}
	}
	for _, unionName := range []string{"RollupDataPoint", "DailyRollupDataPoint"} {
		union, ok := document.Schemas[unionName]
		if !ok {
			continue
		}
		for _, jsonField := range candidates {
			property, ok := union.Properties[jsonField]
			if !ok || property.Ref == "" {
				continue
			}
			schema, ok := document.Schemas[property.Ref]
			if !ok {
				return "", "", discoverySchema{}, ErrCatalogDiscoveryIncompatible
			}
			return jsonField, property.Ref, schema, nil
		}
	}
	return "", "", discoverySchema{}, ErrCatalogDiscoveryIncompatible
}
