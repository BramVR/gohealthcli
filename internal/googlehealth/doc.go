// Package googlehealth is the Google Health API Provider client
// (ADR-0001, ADR-0007): the Data Type catalog, raw request builders
// and the single-attempt fetch, Sync Run ingestion with pagination and
// the bounded retry middleware, the Data Point and Rollup parsers, the
// shared Provider GET module, the typed error translation layer, the
// `--rollup` spec, the OAuth scope constants, and the identity
// endpoint catalog.
//
// The package depends only on internal/archived (the shared
// archived-row types) and the standard library. Main supplies the
// transport through the Doer seam — production binds the shared
// timeout HTTPClient via the runtime adapters — and consumes the
// ingestion through NewIngestion / Execute. `raw` exploration uses
// BuildRawRequest + the fetchRawProvider seam; Identity Snapshot
// fetchers in main ride GET.FetchJSON.
//
// Extracted from the main dispatch package in issue #287; it is the
// first internal package per ADR-0007's sequencing and sets the
// pattern for a later internal/archive extraction.
package googlehealth
