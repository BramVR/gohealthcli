package main

import (
	"errors"
	"fmt"
	"time"
)

// syncPreflightGate is the one seam between flag parsing and the Sync Run
// lifecycle. Validate fans every preflight rule that can fire without
// contacting the upstream provider through a single entry point. On
// success it returns a fully-resolved preflightPlan (fan-out list,
// parsed --from/--to, optional rollup spec, current Connection, and per-
// Data-Type cursor keys) so the downstream lifecycle never re-parses or
// re-validates the same inputs. On failure it returns a *preflightFailure
// carrying the rule discriminator so callers can route the error without
// string-matching, and so the Sync Run lifecycle has not yet written an
// audit row — concentrating the no-audit-row contract here instead of
// scattering early-return-before-StartSyncRun checks across the CLI
// entry point, the orchestrator, and the run executor.
type syncPreflightGate struct {
	ctx syncPreflightContext
}

// syncPreflightContext is the minimal seam the gate needs to run every
// rule. Production wires this to the catalog + archive lookups; unit
// tests pass in-memory fakes (no provider stub, no archive write) so the
// table test exercises every rule in milliseconds.
type syncPreflightContext struct {
	now                    func() time.Time
	dataTypeSupported      func(dataType string) bool
	dataTypeUsesDateRange  func(dataType string) bool
	sourceFamilyFilter     func(dataType, sourceFamily string) (string, error)
	defaultAllDataTypes    func() []string
	currentConnection      func() (archivedConnection, error)
	rollupCatalogValidator func(spec syncRollupSpec, dataType string) error
}

// preflightPlan is the resolved fan-out the Sync Run lifecycle consumes.
// Every field is already validated; consumers MUST NOT re-parse or re-
// check rules the gate already enforced. dataTypes is the iteration
// order; cursorKeys is index-aligned. rollupSpec is nil when --rollup is
// empty so the lifecycle can branch on presence without re-parsing.
type preflightPlan struct {
	dataTypes  []string
	from       string
	to         string
	rollup     string
	rollupSpec *syncRollupSpec
	connection archivedConnection
	cursorKeys []syncCursorKey
}

// preflightFailure tags every gate rejection with a stable rule
// identifier so the CLI can decide JSON envelope shape, logging, and
// exit-code routing without string-matching the error message. The rule
// constants below are the canonical names.
type preflightFailure struct {
	rule string
	err  error
}

// preflight rule discriminators. New rules added in later slices append
// here; existing constants are stable so downstream tests can pin them.
const (
	preflightRuleMissingDataTypes          = "missing_data_types"
	preflightRuleAllVsTypesConflict        = "all_vs_types_conflict"
	preflightRuleDuplicateDataType         = "duplicate_data_type"
	preflightRuleUnsupportedDataType       = "unsupported_data_type"
	preflightRuleRollupParse               = "rollup_parse"
	preflightRuleRollupCatalog             = "rollup_catalog"
	preflightRuleSourceFamily              = "source_family"
	preflightRuleRollupSourceFamilyConflict = "rollup_source_family_conflict"
	preflightRuleConnectionLookup          = "connection_lookup"
)

func (f *preflightFailure) Error() string { return f.err.Error() }
func (f *preflightFailure) Unwrap() error { return f.err }
func (f *preflightFailure) Rule() string  { return f.rule }

func newPreflightFailure(rule string, err error) *preflightFailure {
	return &preflightFailure{rule: rule, err: err}
}

// Validate runs every preflight rule in deterministic order and returns
// either the resolved plan or the first rule that rejected. Connection
// presence checks run AFTER flag-shape checks so an operator typo on
// --types surfaces faster than the archive open.
func (gate syncPreflightGate) Validate(options syncCommandOptions) (preflightPlan, error) {
	dataTypes, err := gate.expandDataTypes(options)
	if err != nil {
		return preflightPlan{}, err
	}
	for _, dataType := range dataTypes {
		if !gate.ctx.dataTypeSupported(dataType) {
			return preflightPlan{}, newPreflightFailure(
				preflightRuleUnsupportedDataType,
				fmt.Errorf("sync Data Type %q is not supported yet", dataType),
			)
		}
	}
	if options.rollup != "" && options.sourceFamily != "" {
		return preflightPlan{}, newPreflightFailure(
			preflightRuleRollupSourceFamilyConflict,
			errors.New("sync --source-family cannot be combined with --rollup"),
		)
	}
	var rollupSpec *syncRollupSpec
	if options.rollup != "" {
		spec, err := parseSyncRollupSpec(options.rollup)
		if err != nil {
			return preflightPlan{}, newPreflightFailure(preflightRuleRollupParse, err)
		}
		validate := gate.ctx.rollupCatalogValidator
		if validate == nil {
			validate = validateSyncRollupAgainstDataType
		}
		for _, dataType := range dataTypes {
			if err := validate(spec, dataType); err != nil {
				return preflightPlan{}, newPreflightFailure(preflightRuleRollupCatalog, err)
			}
		}
		rollupSpec = &spec
	}
	if options.sourceFamily != "" {
		for _, dataType := range dataTypes {
			if _, err := gate.ctx.sourceFamilyFilter(dataType, options.sourceFamily); err != nil {
				return preflightPlan{}, newPreflightFailure(preflightRuleSourceFamily, err)
			}
		}
	}
	to := options.to
	if to == "" {
		to = gate.defaultTo(options, dataTypes)
	}
	connection, err := gate.ctx.currentConnection()
	if err != nil {
		return preflightPlan{}, newPreflightFailure(preflightRuleConnectionLookup, err)
	}
	cursorKeys := make([]syncCursorKey, 0, len(dataTypes))
	for _, dataType := range dataTypes {
		cursorKeys = append(cursorKeys, syncCursorKey{
			connectionID:       connection.id,
			dataType:           dataType,
			sourceFamilyFilter: options.sourceFamily,
			rollupKind:         rollupKindForSync(options.rollup),
		})
	}
	return preflightPlan{
		dataTypes:  dataTypes,
		from:       options.from,
		to:         to,
		rollup:     options.rollup,
		rollupSpec: rollupSpec,
		connection: connection,
		cursorKeys: cursorKeys,
	}, nil
}

// expandDataTypes resolves --all / --types into the concrete ordered list
// the gate then validates per-type. Empty --types + no --all is the
// "missing inputs" rule; --all + --types is the mutual-exclusion rule;
// duplicate --types entries are rejected before any per-type validation
// so the operator hears about the duplicate before any individual-type
// failure that depends on order.
func (gate syncPreflightGate) expandDataTypes(options syncCommandOptions) ([]string, error) {
	if options.allTypes {
		if len(options.dataTypes) != 0 {
			return nil, newPreflightFailure(
				preflightRuleAllVsTypesConflict,
				errors.New("sync --all cannot be combined with --types"),
			)
		}
		all := gate.ctx.defaultAllDataTypes()
		resolved := make([]string, 0, len(all))
		for _, dataType := range all {
			if gate.ctx.dataTypeSupported(dataType) {
				resolved = append(resolved, dataType)
			}
		}
		return resolved, nil
	}
	if len(options.dataTypes) == 0 {
		return nil, newPreflightFailure(
			preflightRuleMissingDataTypes,
			errors.New("sync requires --types or --all"),
		)
	}
	seen := make(map[string]struct{}, len(options.dataTypes))
	resolved := make([]string, 0, len(options.dataTypes))
	for _, dataType := range options.dataTypes {
		if _, ok := seen[dataType]; ok {
			return nil, newPreflightFailure(
				preflightRuleDuplicateDataType,
				fmt.Errorf("sync --types lists %q more than once", dataType),
			)
		}
		seen[dataType] = struct{}{}
		resolved = append(resolved, dataType)
	}
	return resolved, nil
}

// defaultTo mirrors the historical executor default: civil date for
// daily rollups or catalog-flagged date-range Data Types; RFC3339 for
// everything else. The choice depends on the first Data Type today
// (single-type execution at the executor seam) — applied to the whole
// fan-out for now since the rule cannot disagree across types in any
// currently supported invocation (daily rollup applies to every type;
// date-range default is a property of the type but the existing
// executor only saw one type at a time so the rule never had to choose).
func (gate syncPreflightGate) defaultTo(options syncCommandOptions, dataTypes []string) string {
	if options.rollup == "daily" {
		return gate.ctx.now().UTC().Format("2006-01-02")
	}
	for _, dataType := range dataTypes {
		if gate.ctx.dataTypeUsesDateRange(dataType) {
			return gate.ctx.now().UTC().Format("2006-01-02")
		}
	}
	return gate.ctx.now().UTC().Format(time.RFC3339)
}

// productionSyncPreflightContext wires the gate to the real catalog +
// archive openers. The archivePath/configPath round-trip the same way
// the executor used to call inspectIdentityConfig + openHealthArchiveWriter;
// the gate exposes that as one closure so call sites do not duplicate
// the open-then-close dance.
func productionSyncPreflightContext(options syncCommandOptions, runtime runtimeAdapters) syncPreflightContext {
	return syncPreflightContext{
		now:                   runtime.now,
		dataTypeSupported:     syncDataPointDataTypeSupported,
		dataTypeUsesDateRange: syncDataPointUsesDateRange,
		sourceFamilyFilter:    googleHealthSourceFamilyFilterName,
		// defaultDataTypes is a package-level var that the gate only ranges
		// over; other readers also treat it as read-only, so returning it
		// directly avoids allocating a fresh copy on every Validate call.
		defaultAllDataTypes: func() []string { return defaultDataTypes },
		currentConnection: func() (archivedConnection, error) {
			if _, err := inspectIdentityConfig(options.configPath, options.archivePath); err != nil {
				return archivedConnection{}, fmt.Errorf("config check failed: %w", err)
			}
			archive, err := healthArchiveWriterOpenerForTest(options.archivePath)
			if err != nil {
				return archivedConnection{}, err
			}
			defer archive.Close()
			return archive.CurrentConnection()
		},
		rollupCatalogValidator: validateSyncRollupAgainstDataType,
	}
}
