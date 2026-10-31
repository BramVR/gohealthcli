// Package archived holds the archived-row types shared by the Google
// Health Provider client (which produces them from upstream responses)
// and the Health Archive (which persists and reads them). It is a
// types-only leaf package so the dependency arrows point one way:
// main and internal/googlehealth import archived; archived imports
// nothing. A later internal/archive extraction can import this package
// without depending on any Provider client (issue #287, ADR-0007).
package archived

// Connection is the archived form of one Connection row: the local
// authorization relationship between gohealthcli and one Google
// Identity (see CONTEXT.md "Connection"). The Provider client stamps
// ProviderName and ID onto every Data Point and Rollup it parses;
// Connection access reads TokenMetadataJSON for expiry and scopes.
type Connection struct {
	ID                 string
	ProviderName       string
	GoogleHealthUserID string
	LegacyFitbitUserID string
	TokenMetadataJSON  string
}

// DataPoint is the archived form of one upstream Data Point: the
// parsed, column-shaped projection the Health Archive upserts into
// data_points. RawJSON carries the canonical upstream record verbatim;
// the remaining fields are the indexed projections the schema needs.
type DataPoint struct {
	ProviderName         string
	ConnectionID         string
	DataType             string
	UpstreamResourceName string
	RecordKind           string
	StartTimeUTC         string
	EndTimeUTC           string
	StartCivilTime       string
	EndCivilTime         string
	ProviderCivilDate    string
	TimezoneMetadataJSON string
	DataSourceJSON       string
	SourceFamilyFilter   string
	RawJSON              string
}

// Rollup is the archived form of one upstream Rollup: an aggregate
// returned by a rollUp or dailyRollUp endpoint over a time window
// (see CONTEXT.md "Rollup"). Daily rollups carry CivilDate; windowed
// rollups carry WindowStartUTC/WindowEndUTC.
type Rollup struct {
	ProviderName         string
	ConnectionID         string
	DataType             string
	RollupKind           string
	WindowStartUTC       string
	WindowEndUTC         string
	CivilDate            string
	TimezoneMetadataJSON string
	RawJSON              string
}
