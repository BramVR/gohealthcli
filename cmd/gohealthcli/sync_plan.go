package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

type syncPlanRange struct {
	From              string `json:"from,omitempty"`
	To                string `json:"to,omitempty"`
	Timezone          string `json:"timezone,omitempty"`
	ResolvedAt        string `json:"resolved_at,omitempty"`
	ResumedFromCursor bool   `json:"resumed_from_cursor"`
}

type syncPlanRequestPreview struct {
	EndpointName string          `json:"endpoint_name"`
	Method       string          `json:"method"`
	URL          string          `json:"url"`
	Body         json.RawMessage `json:"body,omitempty"`
}

type syncPlanConditionalOperation struct {
	Name           string   `json:"name"`
	Condition      string   `json:"condition"`
	RequiredScopes []string `json:"required_scopes"`
	ProviderEffect string   `json:"provider_effect"`
	ArchiveEffect  string   `json:"archive_effect"`
}

type syncPlanPredictedEffects struct {
	ProviderRequests  string `json:"provider_requests"`
	CredentialStore   string `json:"credential_store"`
	TokenRefresh      string `json:"token_refresh"`
	HealthArchive     string `json:"health_archive"`
	SyncCursor        string `json:"sync_cursor"`
	AttachmentSidecar string `json:"attachment_sidecar"`
}

type syncPlanExecutionEffects struct {
	ProviderRequest        bool `json:"provider_request"`
	CredentialRead         bool `json:"credential_read"`
	TokenRefresh           bool `json:"token_refresh"`
	ArchiveWrite           bool `json:"archive_write"`
	Migration              bool `json:"migration"`
	CursorAdvance          bool `json:"cursor_advance"`
	AttachmentSidecarWrite bool `json:"attachment_sidecar_write"`
}

type syncPlanReadiness struct {
	OnlineChecksNotPerformed []string `json:"online_checks_not_performed"`
}

type syncPlanBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type syncPlanResult struct {
	Status                string                            `json:"status"`
	Ready                 bool                              `json:"ready"`
	DataType              string                            `json:"data_type,omitempty"`
	Range                 syncPlanRange                     `json:"range,omitempty"`
	SourceFamily          string                            `json:"source_family,omitempty"`
	EndpointFamily        string                            `json:"endpoint_family,omitempty"`
	PagePolicy            *googlehealth.IngestionPagePolicy `json:"page_policy,omitempty"`
	RequiredScopes        []string                          `json:"required_scopes,omitempty"`
	Request               *syncPlanRequestPreview           `json:"request,omitempty"`
	ConditionalOperations []syncPlanConditionalOperation    `json:"conditional_operations,omitempty"`
	PredictedSyncEffects  *syncPlanPredictedEffects         `json:"predicted_sync_effects,omitempty"`
	PlanningEffects       syncPlanExecutionEffects          `json:"planning_effects"`
	Readiness             *syncPlanReadiness                `json:"readiness,omitempty"`
	Blockers              []syncPlanBlocker                 `json:"blockers,omitempty"`
	Message               string                            `json:"message"`
}

// syncPlanFanOutResult intentionally uses operations rather than the
// results/summary shape of a real multi-Data-Type Sync Run. Planning predicts
// future work; it does not create Sync Runs or reuse their result contract.
type syncPlanFanOutResult struct {
	Status     string            `json:"status"`
	Ready      bool              `json:"ready"`
	Operations []syncPlanResult  `json:"operations"`
	Blockers   []syncPlanBlocker `json:"blockers,omitempty"`
	Message    string            `json:"message"`
}

var syncPlanOnlineChecksNotPerformed = []string{
	"credential_availability",
	"google_identity_match",
	"provider_reachability",
}

const syncPlanCommandHelp = "\n\n`--plan` resolves one operation per requested Data Type without making a Provider request, reading the Credential Store, refreshing a token, opening a writable Health Archive, migrating, fencing runs, advancing a Sync Cursor, or creating an Attachment sidecar. In planning mode, `--types` preserves requested order and may be repeated; `--all` uses catalog order. Every operation resolves its own range and Sync Cursor, and a local blocker affects only that Data Type. Planning reports the resolved range and source, endpoint, page policy, required scopes, sanitized first-request preview, conditional exercise TCX operation, and predicted effects of the future sync. Readiness covers local facts only: credential availability, Google Identity match, and Provider reachability remain explicitly unchecked. A fan-out with any locally blocked operation exits nonzero after reporting every operation."

func runSyncPlan(ctx context.Context, options syncCommandOptions, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	fanOut := options.allTypes || len(options.dataTypes) > 1
	if !fanOut {
		result := buildSyncPlan(ctx, options, runtime)
		if writeErr := writeSyncPlanResult(result, mode, stdout); writeErr != nil {
			return reportWriteFailure("sync", writeErr, mode, stdout, stderr)
		}
		if !result.Ready {
			return 1
		}
		return 0
	}
	result := buildSyncPlanFanOut(ctx, options, runtime)
	if writeErr := writeSyncPlanFanOutResult(result, mode, stdout); writeErr != nil {
		return reportWriteFailure("sync", writeErr, mode, stdout, stderr)
	}
	if !result.Ready {
		return 1
	}
	return 0
}

func buildSyncPlanFanOut(ctx context.Context, options syncCommandOptions, runtime runtimeAdapters) syncPlanFanOutResult {
	runtime = runtime.withDefaults()
	if options.resolvedAt.IsZero() {
		options.resolvedAt = runtime.now()
	}
	dataTypes, err := newSyncOrchestrator(runtime).expandDataTypes(options)
	if err != nil {
		return blockedSyncPlanFanOut(syncPlanBlockerCode(err), err)
	}
	if len(dataTypes) == 0 {
		err := errors.New("sync --all expanded to zero supported Data Types; catalog has no syncable entries")
		return blockedSyncPlanFanOut(preflightRuleAllExpandedEmpty, err)
	}
	result := syncPlanFanOutResult{
		Status:     "plan_ready",
		Ready:      true,
		Operations: make([]syncPlanResult, 0, len(dataTypes)),
	}
	readyCount := 0
	for _, dataType := range dataTypes {
		operation := buildSyncPlan(ctx, perTypeSyncOptions(options, dataType), runtime)
		result.Operations = append(result.Operations, operation)
		if operation.Ready {
			readyCount++
		} else {
			result.Status = "plan_blocked"
			result.Ready = false
		}
	}
	blockedCount := len(result.Operations) - readyCount
	if blockedCount == 0 {
		result.Message = fmt.Sprintf("Sync plan fan-out is locally ready across %d Data Types; online checks were not performed.", readyCount)
	} else {
		result.Message = fmt.Sprintf("Sync plan fan-out has %d ready and %d locally blocked Data Types.", readyCount, blockedCount)
	}
	return result
}

func blockedSyncPlanFanOut(code string, err error) syncPlanFanOutResult {
	return syncPlanFanOutResult{
		Status:     "plan_blocked",
		Ready:      false,
		Operations: []syncPlanResult{},
		Blockers:   []syncPlanBlocker{{Code: code, Message: err.Error()}},
		Message:    "Sync plan fan-out is locally blocked before Data Type expansion.",
	}
}

func buildSyncPlan(ctx context.Context, options syncCommandOptions, runtime runtimeAdapters) syncPlanResult {
	if err := ctx.Err(); err != nil {
		return blockedSyncPlan(options, "planning_canceled", err)
	}
	runtime = runtime.withDefaults()
	gate := syncPreflightGate{ctx: productionSyncPreflightContext(ctx, options, runtime)}
	plan, err := gate.Validate(options)
	if cancelErr := ctx.Err(); cancelErr != nil {
		return blockedSyncPlan(options, "planning_canceled", cancelErr)
	}
	if err != nil {
		return blockedSyncPlan(options, syncPlanBlockerCode(err), err)
	}
	if len(plan.dataTypes) != 1 {
		return blockedSyncPlan(options, "fan_out_not_supported", errors.New("sync --plan currently supports exactly one single Data Type"))
	}
	dataType := plan.dataTypes[0]
	from := plan.from
	resumedFromCursor := false
	if from == "" {
		archive, err := runtime.openSyncPlanningArchive(ctx, options.archivePath)
		if err != nil {
			return blockedSyncPlanFromPreflight(plan, "", false, "planning_archive", err)
		}
		cursorTime, found, readErr := archive.ResolveSyncCursor(ctx, plan.cursorKeys[0])
		closeErr := archive.Close()
		if readErr != nil {
			readErr = fmt.Errorf("resolve Sync Cursor: %w", readErr)
		}
		if err := syncPlanningResultError(readErr, closeErr); err != nil {
			return blockedSyncPlanFromPreflight(plan, "", false, "sync_cursor_read", err)
		}
		if !found {
			return blockedSyncPlanFromPreflight(plan, "", false, "missing_sync_cursor", errors.New("sync has no Sync Cursor for this Data Type yet; set --from for the initial backfill"))
		}
		from = cursorTime
		resumedFromCursor = true
	}
	description, err := newGoogleHealthIngestionWithRuntime(runtime).DescribePlan(googlehealth.IngestionRequest{
		Connection:   plan.connection,
		DataType:     dataType,
		From:         from,
		To:           plan.to,
		Rollup:       options.rollup,
		SourceFamily: options.sourceFamily,
	})
	if err != nil {
		return blockedSyncPlanFromPreflight(plan, from, resumedFromCursor, "request_preview", err)
	}
	if _, _, err := connectionTokenExpiryAndScopes(plan.connection.TokenMetadataJSON); err != nil {
		return blockedSyncPlanFromPreflight(plan, from, resumedFromCursor, "connection_token_metadata", err)
	}
	if err := requireConnectionScopes(plan.connection.TokenMetadataJSON, description.Request.RequiredScopes); err != nil {
		return blockedSyncPlanFromPreflight(plan, from, resumedFromCursor, "missing_required_scope", err)
	}
	archiveEffect := "write_data_points_and_sync_run"
	if options.rollup != "" {
		archiveEffect = "write_rollups_and_sync_run"
	}
	resolvedSourceFamily := options.sourceFamily
	if resolvedSourceFamily == "" {
		resolvedSourceFamily = "all"
	}
	result := syncPlanResult{
		Status:         "plan_ready",
		Ready:          true,
		DataType:       dataType,
		SourceFamily:   resolvedSourceFamily,
		EndpointFamily: description.EndpointFamily,
		Range: syncPlanRange{
			From:              from,
			To:                plan.to,
			Timezone:          plan.timezone,
			ResolvedAt:        plan.resolvedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			ResumedFromCursor: resumedFromCursor,
		},
		PagePolicy:     &description.PagePolicy,
		RequiredScopes: append([]string(nil), description.Request.RequiredScopes...),
		Request: &syncPlanRequestPreview{
			EndpointName: description.Request.EndpointName,
			Method:       description.Request.Method,
			URL:          description.Request.URL,
			Body:         append(json.RawMessage(nil), description.Request.Body...),
		},
		PredictedSyncEffects: &syncPlanPredictedEffects{
			ProviderRequests:  "read",
			CredentialStore:   "read",
			TokenRefresh:      "conditional",
			HealthArchive:     archiveEffect,
			SyncCursor:        "advance_after_sync_completed",
			AttachmentSidecar: "none",
		},
		Readiness: &syncPlanReadiness{
			OnlineChecksNotPerformed: append([]string(nil), syncPlanOnlineChecksNotPerformed...),
		},
		Message: "Sync plan is locally ready; online credential, Google Identity, and Provider reachability checks were not performed.",
	}
	if description.ConditionalExerciseTcx {
		result.ConditionalOperations = []syncPlanConditionalOperation{{
			Name:           "exercise_tcx",
			Condition:      "per exercise Data Point with a resource name when the location scope is granted and the Provider returns TCX bytes",
			RequiredScopes: []string{googlehealth.ScopeActivityReadonly, googlehealth.ScopeLocationReadonly},
			ProviderEffect: "conditional_read",
			ArchiveEffect:  "conditional_attachment_sidecar_write",
		}}
		result.PredictedSyncEffects.AttachmentSidecar = "conditional_exercise_tcx"
	}
	return result
}

func blockedSyncPlan(options syncCommandOptions, code string, err error) syncPlanResult {
	dataType := ""
	if len(options.dataTypes) == 1 {
		dataType = options.dataTypes[0]
	}
	return syncPlanResult{
		Status:   "plan_blocked",
		Ready:    false,
		DataType: dataType,
		Range: syncPlanRange{
			From: options.from,
			To:   options.to,
		},
		Blockers: []syncPlanBlocker{{Code: code, Message: err.Error()}},
		Message:  "Sync plan is locally blocked.",
	}
}

func blockedSyncPlanFromPreflight(plan preflightPlan, from string, resumedFromCursor bool, code string, err error) syncPlanResult {
	dataType := ""
	if len(plan.dataTypes) == 1 {
		dataType = plan.dataTypes[0]
	}
	return syncPlanResult{
		Status:   "plan_blocked",
		Ready:    false,
		DataType: dataType,
		Range: syncPlanRange{
			From:              from,
			To:                plan.to,
			Timezone:          plan.timezone,
			ResolvedAt:        plan.resolvedAt.UTC().Format(time.RFC3339Nano),
			ResumedFromCursor: resumedFromCursor,
		},
		Blockers: []syncPlanBlocker{{Code: code, Message: err.Error()}},
		Message:  "Sync plan is locally blocked.",
	}
}

func syncPlanBlockerCode(err error) string {
	var failure *preflightFailure
	if errors.As(err, &failure) {
		return failure.Rule()
	}
	return "local_preflight"
}

func writeSyncPlanResult(result syncPlanResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writeSyncPlanPlain(writer, "", result)
	} else {
		writeSyncPlanHuman(writer, result)
	}
	return writer.Err()
}

func writeSyncPlanFanOutResult(result syncPlanFanOutResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writer.Printf("status: %s\n", result.Status)
		writer.Printf("ready: %t\n", result.Ready)
		for index, operation := range result.Operations {
			writeSyncPlanPlain(writer, fmt.Sprintf("operations.%d.", index), operation)
		}
		for index, blocker := range result.Blockers {
			writer.Printf("blockers.%d.code: %s\n", index, blocker.Code)
			writer.Printf("blockers.%d.message: %s\n", index, blocker.Message)
		}
		writer.Printf("message: %s\n", result.Message)
	} else {
		state := "ready"
		if !result.Ready {
			state = "blocked"
		}
		writer.Printf("Sync plan fan-out %s across %d Data Types\n", state, len(result.Operations))
		for index, operation := range result.Operations {
			writer.Printf("\nOperation %d of %d\n", index+1, len(result.Operations))
			writeSyncPlanHuman(writer, operation)
		}
		for _, blocker := range result.Blockers {
			writer.Printf("Blocker [%s]: %s\n", blocker.Code, blocker.Message)
		}
		writer.Printf("Message: %s\n", result.Message)
	}
	return writer.Err()
}

func writeSyncPlanPlain(writer *stickyWriter, prefix string, result syncPlanResult) {
	writer.Printf("%sstatus: %s\n", prefix, result.Status)
	writer.Printf("%sready: %t\n", prefix, result.Ready)
	if result.DataType != "" {
		writer.Printf("%sdata_type: %s\n", prefix, result.DataType)
	}
	if result.Range.From != "" {
		writer.Printf("%srange.from: %s\n", prefix, result.Range.From)
	}
	if result.Range.To != "" {
		writer.Printf("%srange.to: %s\n", prefix, result.Range.To)
	}
	if result.Range.Timezone != "" {
		writer.Printf("%srange.timezone: %s\n", prefix, result.Range.Timezone)
	}
	if result.Range.ResolvedAt != "" {
		writer.Printf("%srange.resolved_at: %s\n", prefix, result.Range.ResolvedAt)
	}
	writer.Printf("%srange.resumed_from_cursor: %t\n", prefix, result.Range.ResumedFromCursor)
	if result.SourceFamily != "" {
		writer.Printf("%ssource_family: %s\n", prefix, result.SourceFamily)
	}
	if result.EndpointFamily != "" {
		writer.Printf("%sendpoint_family: %s\n", prefix, result.EndpointFamily)
	}
	if result.PagePolicy != nil {
		writer.Printf("%spage_policy.pagination: %s\n", prefix, result.PagePolicy.Pagination)
		writer.Printf("%spage_policy.page_size: %d\n", prefix, result.PagePolicy.PageSize)
		writer.Printf("%spage_policy.page_size_policy: %s\n", prefix, result.PagePolicy.PageSizePolicy)
		writer.Printf("%spage_policy.range_window_count: %d\n", prefix, result.PagePolicy.RangeWindowCount)
		if result.PagePolicy.RangeWindowMaxDays != 0 {
			writer.Printf("%spage_policy.range_window_max_days: %d\n", prefix, result.PagePolicy.RangeWindowMaxDays)
		}
	}
	for index, scope := range result.RequiredScopes {
		writer.Printf("%srequired_scopes.%d: %s\n", prefix, index, scope)
	}
	if result.Request != nil {
		writer.Printf("%srequest.endpoint_name: %s\n", prefix, result.Request.EndpointName)
		writer.Printf("%srequest.method: %s\n", prefix, result.Request.Method)
		writer.Printf("%srequest.url: %s\n", prefix, result.Request.URL)
		if len(result.Request.Body) != 0 {
			writer.Printf("%srequest.body: %s\n", prefix, result.Request.Body)
		}
	}
	for index, operation := range result.ConditionalOperations {
		operationPrefix := fmt.Sprintf("%sconditional_operations.%d.", prefix, index)
		writer.Printf("%sname: %s\n", operationPrefix, operation.Name)
		writer.Printf("%scondition: %s\n", operationPrefix, operation.Condition)
		for scopeIndex, scope := range operation.RequiredScopes {
			writer.Printf("%srequired_scopes.%d: %s\n", operationPrefix, scopeIndex, scope)
		}
		writer.Printf("%sprovider_effect: %s\n", operationPrefix, operation.ProviderEffect)
		writer.Printf("%sarchive_effect: %s\n", operationPrefix, operation.ArchiveEffect)
	}
	if result.PredictedSyncEffects != nil {
		writer.Printf("%spredicted_sync_effects.provider_requests: %s\n", prefix, result.PredictedSyncEffects.ProviderRequests)
		writer.Printf("%spredicted_sync_effects.credential_store: %s\n", prefix, result.PredictedSyncEffects.CredentialStore)
		writer.Printf("%spredicted_sync_effects.token_refresh: %s\n", prefix, result.PredictedSyncEffects.TokenRefresh)
		writer.Printf("%spredicted_sync_effects.health_archive: %s\n", prefix, result.PredictedSyncEffects.HealthArchive)
		writer.Printf("%spredicted_sync_effects.sync_cursor: %s\n", prefix, result.PredictedSyncEffects.SyncCursor)
		writer.Printf("%spredicted_sync_effects.attachment_sidecar: %s\n", prefix, result.PredictedSyncEffects.AttachmentSidecar)
	}
	if result.Readiness != nil {
		for index, check := range result.Readiness.OnlineChecksNotPerformed {
			writer.Printf("%sreadiness.online_checks_not_performed.%d: %s\n", prefix, index, check)
		}
	}
	writer.Printf("%splanning_effects.provider_request: false\n", prefix)
	writer.Printf("%splanning_effects.credential_read: false\n", prefix)
	writer.Printf("%splanning_effects.token_refresh: false\n", prefix)
	writer.Printf("%splanning_effects.archive_write: false\n", prefix)
	writer.Printf("%splanning_effects.migration: false\n", prefix)
	writer.Printf("%splanning_effects.cursor_advance: false\n", prefix)
	writer.Printf("%splanning_effects.attachment_sidecar_write: false\n", prefix)
	for index, blocker := range result.Blockers {
		writer.Printf("%sblockers.%d.code: %s\n", prefix, index, blocker.Code)
		writer.Printf("%sblockers.%d.message: %s\n", prefix, index, blocker.Message)
	}
	writer.Printf("%smessage: %s\n", prefix, result.Message)
}

func writeSyncPlanHuman(writer *stickyWriter, result syncPlanResult) {
	if !result.Ready {
		writer.Println("Sync plan blocked")
		if result.DataType != "" {
			writer.Printf("Data Type: %s\n", result.DataType)
		}
		if result.Range.From != "" || result.Range.To != "" {
			writer.Printf("Range: %s to %s\n", result.Range.From, result.Range.To)
		}
		for _, blocker := range result.Blockers {
			writer.Printf("Blocker [%s]: %s\n", blocker.Code, blocker.Message)
		}
		writer.Printf("Message: %s\n", result.Message)
		return
	}
	writer.Println("Sync plan ready")
	writer.Printf("Data Type: %s\n", result.DataType)
	writer.Printf("Range: %s to %s (%s; resolved %s)\n", result.Range.From, result.Range.To, result.Range.Timezone, result.Range.ResolvedAt)
	if result.Range.ResumedFromCursor {
		writer.Println("Range start: resumed from Sync Cursor")
	}
	source := "all"
	if result.SourceFamily != "" {
		source = result.SourceFamily
	}
	writer.Printf("Source family: %s\n", source)
	writer.Printf("Endpoint: %s\n", result.EndpointFamily)
	pageSize := fmt.Sprintf("%d", result.PagePolicy.PageSize)
	if result.PagePolicy.PageSizePolicy == "provider_default" {
		pageSize = "Provider default"
	}
	writer.Printf("Page policy: %s; page size %s; %d range window(s)\n", result.PagePolicy.Pagination, pageSize, result.PagePolicy.RangeWindowCount)
	writer.Printf("Required scopes: %s\n", strings.Join(result.RequiredScopes, ", "))
	writer.Printf("Request preview: %s %s\n", result.Request.Method, result.Request.URL)
	if len(result.Request.Body) != 0 {
		writer.Printf("Request body: %s\n", result.Request.Body)
	}
	for _, operation := range result.ConditionalOperations {
		writer.Printf("Conditional operation: %s — %s\n", operation.Name, operation.Condition)
	}
	writer.Printf("Predicted sync effects: Provider=%s, Credential Store=%s, token refresh=%s, Health Archive=%s, Sync Cursor=%s, Attachment sidecar=%s\n",
		result.PredictedSyncEffects.ProviderRequests,
		result.PredictedSyncEffects.CredentialStore,
		result.PredictedSyncEffects.TokenRefresh,
		result.PredictedSyncEffects.HealthArchive,
		result.PredictedSyncEffects.SyncCursor,
		result.PredictedSyncEffects.AttachmentSidecar,
	)
	writer.Println("Online checks not performed: credential availability, Google Identity match, Provider reachability")
	writer.Println("Planning performed no Provider, credential, OAuth refresh, or Health Archive write effects.")
}
