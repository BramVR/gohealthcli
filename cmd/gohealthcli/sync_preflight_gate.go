package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/BramVR/gohealthcli/internal/archived"
	"github.com/BramVR/gohealthcli/internal/googlehealth"
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
	configuredTimezone     func() (string, error)
	currentConnection      func() (archived.Connection, error)
	rollupCatalogValidator func(spec googlehealth.RollupSpec, dataType string) error
}

// preflightPlan is the resolved fan-out the Sync Run lifecycle consumes.
// Every field is already validated; consumers MUST NOT re-parse or re-
// check rules the gate already enforced. dataTypes is the iteration
// order; cursorKeys is index-aligned. rollupSpec is nil when --rollup is
// empty so the lifecycle can branch on presence without re-parsing.
type preflightPlan struct {
	dataTypes []string
	// from/to are the canonical provider boundaries and therefore the
	// durable cursor contract. Exact resolved instants exist only long
	// enough for preflight ordering: civil endpoints have no offset/fold
	// discriminator, so persisting a physical instant would invent provider
	// semantics and break the exact --to cursor round trip (ADR-0008).
	from       string
	to         string
	timezone   string
	resolvedAt time.Time
	fromInput  string
	toInput    string
	rollup     string
	rollupSpec *googlehealth.RollupSpec
	connection archived.Connection
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
	preflightRuleMissingDataTypes           = "missing_data_types"
	preflightRuleAllExpandedEmpty           = "all_expanded_empty"
	preflightRuleAllVsTypesConflict         = "all_vs_types_conflict"
	preflightRuleDuplicateDataType          = "duplicate_data_type"
	preflightRuleUnsupportedDataType        = "unsupported_data_type"
	preflightRuleRollupParse                = "rollup_parse"
	preflightRuleRollupCatalog              = "rollup_catalog"
	preflightRuleSourceFamily               = "source_family"
	preflightRuleRollupSourceFamilyConflict = "rollup_source_family_conflict"
	preflightRuleConnectionLookup           = "connection_lookup"
	preflightRuleRangeOrderInverted         = "range_order_inverted"
	preflightRuleRangeZeroWidth             = "range_zero_width"
	preflightRuleRangeParse                 = "range_parse"
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
		if options.rollup == "" && !gate.ctx.dataTypeSupported(dataType) {
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
	var rollupSpec *googlehealth.RollupSpec
	if options.rollup != "" {
		spec, err := googlehealth.ParseRollupSpec(options.rollup)
		if err != nil {
			return preflightPlan{}, newPreflightFailure(preflightRuleRollupParse, err)
		}
		validate := gate.ctx.rollupCatalogValidator
		if validate == nil {
			validate = googlehealth.ValidateRollupAgainstDataType
		}
		for _, dataType := range dataTypes {
			if err := validate(spec, dataType); err != nil {
				return preflightPlan{}, newPreflightFailure(preflightRuleRollupCatalog, err)
			}
			if err := spec.ValidateRequestWindow(dataType); err != nil {
				return preflightPlan{}, newPreflightFailure(preflightRuleRangeParse, err)
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
	timezone := options.timezone
	if timezone == "" && gate.ctx.configuredTimezone != nil {
		timezone, err = gate.ctx.configuredTimezone()
		if err != nil {
			return preflightPlan{}, newPreflightFailure(
				preflightRuleConnectionLookup,
				fmt.Errorf("config check failed: %w", err),
			)
		}
	}
	resolvedAt := options.resolvedAt
	if resolvedAt.IsZero() {
		resolvedAt = gate.ctx.now()
	}
	to := options.to
	if to == "" && options.timezone == "" && !googlehealth.IsNamedRangeBoundary(options.from) {
		to, err = gate.legacyDefaultTo(options, dataTypes, resolvedAt, timezone)
		if err != nil {
			return preflightPlan{}, newPreflightFailure(preflightRuleRangeParse, err)
		}
	}
	if rollupSpec != nil {
		if _, _, err := rollupSpec.NormalizeRange(options.from, to, resolvedAt); err != nil {
			return preflightPlan{}, newPreflightFailure(preflightRuleRangeParse, err)
		}
	}
	target, err := googlehealth.SyncRangeTarget(dataTypes[0], rollupSpec, options.sourceFamily != "")
	if err != nil {
		return preflightPlan{}, newPreflightFailure(preflightRuleRangeParse, err)
	}
	resolved, err := googlehealth.ResolveRange(options.from, to, timezone, resolvedAt, target)
	if err != nil {
		return preflightPlan{}, newPreflightFailure(preflightRuleRangeParse, err)
	}
	normFrom, normTo, err := gate.normalizeRange(rollupSpec, resolved.From, resolved.To, resolvedAt)
	if err != nil {
		return preflightPlan{}, newPreflightFailure(preflightRuleRangeParse, err)
	}
	// Range ordering: --from > --to and --from == --to are both flag-
	// shape rejections and must fire before the archive connection
	// lookup so an operator typo surfaces faster than a disk open.
	// --from "" is the cursor-resume case (lifecycle fills it from the
	// Sync Cursor later) and the executor's resume path covers that
	// separately, so skip the check entirely when --from is empty. We
	// validate against the RESOLVED --to (post-defaultTo) so a future
	// --from with --to omitted still trips the inverted-range rule
	// instead of silently producing a plan{from=2099, to=today}.
	if normFrom != "" {
		if err := validatePreflightRangeOrder(
			normFrom,
			normTo,
			target,
			resolved.FromInstant,
			resolved.ToInstant,
		); err != nil {
			return preflightPlan{}, err
		}
	}
	connection, err := gate.ctx.currentConnection()
	if err != nil {
		return preflightPlan{}, newPreflightFailure(preflightRuleConnectionLookup, err)
	}
	cursorKeys := make([]syncCursorKey, 0, len(dataTypes))
	for _, dataType := range dataTypes {
		cursorKeys = append(cursorKeys, syncCursorKey{
			connectionID:       connection.ID,
			dataType:           dataType,
			sourceFamilyFilter: options.sourceFamily,
			rollupKind:         rollupKindForSync(options.rollup),
		})
	}
	return preflightPlan{
		dataTypes:  dataTypes,
		from:       normFrom,
		to:         normTo,
		timezone:   resolved.Timezone,
		resolvedAt: resolved.ResolvedAt,
		fromInput:  namedRangeInput(options.from, resolved.FromNamed),
		toInput:    namedRangeInput(options.to, resolved.ToNamed),
		rollup:     options.rollup,
		rollupSpec: rollupSpec,
		connection: connection,
		cursorKeys: cursorKeys,
	}, nil
}

func namedRangeInput(input string, named bool) string {
	if !named {
		return ""
	}
	return input
}

// normalizeRange applies the established rollup endpoint shape after named
// boundaries have been resolved. Non-rollup list/reconcile requests already
// received their target-specific shape from ResolveRange.
func (gate syncPreflightGate) normalizeRange(spec *googlehealth.RollupSpec, from, to string, resolvedAt time.Time) (string, string, error) {
	if spec == nil {
		return from, to, nil
	}
	return spec.NormalizeRange(from, to, resolvedAt)
}

// legacyDefaultTo preserves the pre-relative-range cursor/backfill default
// shape when an invocation supplies neither a named boundary nor --timezone.
// The resolved config timezone still selects its calendar date. Relative or
// explicitly flag-zoned invocations leave --to empty so ResolveRange can
// render target-aware local now.
func (gate syncPreflightGate) legacyDefaultTo(options syncCommandOptions, dataTypes []string, resolvedAt time.Time, timezone string) (string, error) {
	target := googlehealth.RangeTargetPhysical
	if options.rollup == "daily" {
		target = googlehealth.RangeTargetDaily
	}
	for _, dataType := range dataTypes {
		if gate.ctx.dataTypeUsesDateRange(dataType) {
			target = googlehealth.RangeTargetDaily
			break
		}
	}
	resolved, err := googlehealth.ResolveRange("", "now", timezone, resolvedAt, target)
	if err != nil {
		return "", err
	}
	return resolved.To, nil
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

// validatePreflightRangeOrder enforces the two range-ordering rules
// (inverted range, zero-width window) on a parsed time.Time so civil-
// date and RFC3339 inputs compose correctly. It reuses the single
// boundary parser the googlehealth package owns (ParseRangeBoundary)
// so there is ONE source of truth for the two-shape acceptance contract.
//
// Parse failures have already been rejected by ResolveRange or
// NormalizeRange. The boolean guard remains defensive for cursor-resume
// composition and any future provider target shape.
func validatePreflightRangeOrder(
	from, to string,
	target googlehealth.RangeTarget,
	fromTime, toTime time.Time,
) error {
	// Daily normalization emits one canonical YYYY-MM-DD provider shape.
	// Compare that shape first: provenance affects how a named date maps to
	// an instant, but it must not hide an empty or inverted endpoint window.
	_, fromDateErr := time.Parse("2006-01-02", from)
	_, toDateErr := time.Parse("2006-01-02", to)
	if target == googlehealth.RangeTargetDaily && fromDateErr == nil && toDateErr == nil {
		if from == to {
			return newPreflightFailure(
				preflightRuleRangeZeroWidth,
				fmt.Errorf("sync --from %s and --to %s normalize to the same instant; zero-width sync window is not useful", from, to),
			)
		}
		if from > to {
			return newPreflightFailure(
				preflightRuleRangeOrderInverted,
				fmt.Errorf("sync --from %s: from must be earlier than to (got --to %s)", from, to),
			)
		}
		return nil
	}
	if fromTime.Equal(toTime) {
		return newPreflightFailure(
			preflightRuleRangeZeroWidth,
			fmt.Errorf("sync --from %s and --to %s normalize to the same instant; zero-width sync window is not useful", from, to),
		)
	}
	if fromTime.After(toTime) {
		return newPreflightFailure(
			preflightRuleRangeOrderInverted,
			fmt.Errorf("sync --from %s: from must be earlier than to (got --to %s)", from, to),
		)
	}
	return nil
}

// productionSyncPreflightContext wires the gate to the real catalog +
// strict planning archive opener. The archivePath/configPath
// round-trip the same way the executor used to inspect them, but the
// gate can only read the current Connection.
func productionSyncPreflightContext(ctx context.Context, options syncCommandOptions, runtime runtimeAdapters) syncPreflightContext {
	var config fullConfigCheck
	var configErr error
	configLoaded := false
	loadConfig := func() (fullConfigCheck, error) {
		if !configLoaded {
			config, configErr = inspectIdentityConfig(options.configPath, options.archivePath)
			configLoaded = true
		}
		return config, configErr
	}
	return syncPreflightContext{
		now:                   runtime.now,
		dataTypeSupported:     googlehealth.SupportsSyncDataPoints,
		dataTypeUsesDateRange: googlehealth.UsesDateRangeDefault,
		sourceFamilyFilter:    googlehealth.SourceFamilyFilterName,
		// googlehealth.DefaultDataTypes returns the shared package-level
		// slice; the gate only ranges over it and other readers also treat
		// it as read-only, so no defensive copy is made per Validate call.
		defaultAllDataTypes: func() []string { return googlehealth.DefaultDataTypes() },
		configuredTimezone: func() (string, error) {
			config, err := loadConfig()
			if err != nil {
				return "", err
			}
			return config.timezone, nil
		},
		currentConnection: func() (archived.Connection, error) {
			if _, err := loadConfig(); err != nil {
				return archived.Connection{}, fmt.Errorf("config check failed: %w", err)
			}
			archive, err := runtime.openSyncPlanningArchive(context.WithoutCancel(ctx), options.archivePath)
			if err != nil {
				return archived.Connection{}, err
			}
			// WithoutCancel: the gate's connection lookup is a fast local
			// read, not a cancellation point — the lifecycle entry check
			// owns the pre-start SIGINT contract (no-audit-row +
			// sync_canceled envelope, PRD #141 slice 5). Aborting this
			// read on a canceled context would misreport a pre-start
			// cancel as a preflight sync_failed (#305).
			connection, readErr := archive.CurrentConnection(context.WithoutCancel(ctx))
			return connection, syncPlanningResultError(readErr, archive.Close())
		},
		rollupCatalogValidator: googlehealth.ValidateRollupAgainstDataType,
	}
}
