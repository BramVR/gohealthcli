package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

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

var syncPlanOnlineChecksNotPerformed = []string{
	"credential_availability",
	"google_identity_match",
	"provider_reachability",
}

const syncPlanCommandHelp = "\n\n`--plan` resolves one Data Type's local Sync Run plan without making a Provider request, reading the Credential Store, refreshing a token, opening a writable Health Archive, migrating, fencing runs, advancing a Sync Cursor, or creating an Attachment sidecar. It reports the resolved range and source, endpoint, page policy, required scopes, sanitized first-request preview, conditional exercise TCX operation, and predicted effects of the future sync. Readiness covers local facts only: credential availability, Google Identity match, and Provider reachability remain explicitly unchecked. Locally blocked plans exit nonzero with blocker details. This slice intentionally rejects `--all` and multi-value `--types`; complete fan-out planning is separate work."

func buildSyncPlan(ctx context.Context, options syncCommandOptions, runtime runtimeAdapters) syncPlanResult {
	runtime = runtime.withDefaults()
	gate := syncPreflightGate{ctx: productionSyncPreflightContext(ctx, options, runtime)}
	plan, err := gate.Validate(options)
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
			return blockedSyncPlan(options, "planning_archive", err)
		}
		cursorTime, found, readErr := archive.ResolveSyncCursor(ctx, plan.cursorKeys[0])
		closeErr := archive.Close()
		if readErr != nil {
			readErr = fmt.Errorf("resolve Sync Cursor: %w", readErr)
		}
		if err := syncPlanningResultError(readErr, closeErr); err != nil {
			return blockedSyncPlan(options, "sync_cursor_read", err)
		}
		if !found {
			return blockedSyncPlan(options, "missing_sync_cursor", errors.New("sync has no Sync Cursor for this Data Type yet; set --from for the initial backfill"))
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
		return blockedSyncPlan(options, "request_preview", err)
	}
	if _, _, err := connectionTokenExpiryAndScopes(plan.connection.TokenMetadataJSON); err != nil {
		return blockedSyncPlan(options, "connection_token_metadata", err)
	}
	if err := requireConnectionScopes(plan.connection.TokenMetadataJSON, description.Request.RequiredScopes); err != nil {
		return blockedSyncPlan(options, "missing_required_scope", err)
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
		writeSyncPlanPlain(writer, result)
	} else {
		writeSyncPlanHuman(writer, result)
	}
	return writer.Err()
}

func writeSyncPlanPlain(writer *stickyWriter, result syncPlanResult) {
	writer.Printf("status: %s\n", result.Status)
	writer.Printf("ready: %t\n", result.Ready)
	if result.DataType != "" {
		writer.Printf("data_type: %s\n", result.DataType)
	}
	if result.Range.From != "" {
		writer.Printf("range.from: %s\n", result.Range.From)
	}
	if result.Range.To != "" {
		writer.Printf("range.to: %s\n", result.Range.To)
	}
	if result.Range.Timezone != "" {
		writer.Printf("range.timezone: %s\n", result.Range.Timezone)
	}
	if result.Range.ResolvedAt != "" {
		writer.Printf("range.resolved_at: %s\n", result.Range.ResolvedAt)
	}
	writer.Printf("range.resumed_from_cursor: %t\n", result.Range.ResumedFromCursor)
	if result.SourceFamily != "" {
		writer.Printf("source_family: %s\n", result.SourceFamily)
	}
	if result.EndpointFamily != "" {
		writer.Printf("endpoint_family: %s\n", result.EndpointFamily)
	}
	if result.PagePolicy != nil {
		writer.Printf("page_policy.pagination: %s\n", result.PagePolicy.Pagination)
		writer.Printf("page_policy.page_size: %d\n", result.PagePolicy.PageSize)
		writer.Printf("page_policy.page_size_policy: %s\n", result.PagePolicy.PageSizePolicy)
		writer.Printf("page_policy.range_window_count: %d\n", result.PagePolicy.RangeWindowCount)
		if result.PagePolicy.RangeWindowMaxDays != 0 {
			writer.Printf("page_policy.range_window_max_days: %d\n", result.PagePolicy.RangeWindowMaxDays)
		}
	}
	for index, scope := range result.RequiredScopes {
		writer.Printf("required_scopes.%d: %s\n", index, scope)
	}
	if result.Request != nil {
		writer.Printf("request.endpoint_name: %s\n", result.Request.EndpointName)
		writer.Printf("request.method: %s\n", result.Request.Method)
		writer.Printf("request.url: %s\n", result.Request.URL)
		if len(result.Request.Body) != 0 {
			writer.Printf("request.body: %s\n", result.Request.Body)
		}
	}
	for index, operation := range result.ConditionalOperations {
		prefix := fmt.Sprintf("conditional_operations.%d.", index)
		writer.Printf("%sname: %s\n", prefix, operation.Name)
		writer.Printf("%scondition: %s\n", prefix, operation.Condition)
		for scopeIndex, scope := range operation.RequiredScopes {
			writer.Printf("%srequired_scopes.%d: %s\n", prefix, scopeIndex, scope)
		}
		writer.Printf("%sprovider_effect: %s\n", prefix, operation.ProviderEffect)
		writer.Printf("%sarchive_effect: %s\n", prefix, operation.ArchiveEffect)
	}
	if result.PredictedSyncEffects != nil {
		writer.Printf("predicted_sync_effects.provider_requests: %s\n", result.PredictedSyncEffects.ProviderRequests)
		writer.Printf("predicted_sync_effects.credential_store: %s\n", result.PredictedSyncEffects.CredentialStore)
		writer.Printf("predicted_sync_effects.token_refresh: %s\n", result.PredictedSyncEffects.TokenRefresh)
		writer.Printf("predicted_sync_effects.health_archive: %s\n", result.PredictedSyncEffects.HealthArchive)
		writer.Printf("predicted_sync_effects.sync_cursor: %s\n", result.PredictedSyncEffects.SyncCursor)
		writer.Printf("predicted_sync_effects.attachment_sidecar: %s\n", result.PredictedSyncEffects.AttachmentSidecar)
	}
	if result.Readiness != nil {
		for index, check := range result.Readiness.OnlineChecksNotPerformed {
			writer.Printf("readiness.online_checks_not_performed.%d: %s\n", index, check)
		}
	}
	writer.Println("planning_effects.provider_request: false")
	writer.Println("planning_effects.credential_read: false")
	writer.Println("planning_effects.token_refresh: false")
	writer.Println("planning_effects.archive_write: false")
	writer.Println("planning_effects.migration: false")
	writer.Println("planning_effects.cursor_advance: false")
	writer.Println("planning_effects.attachment_sidecar_write: false")
	for index, blocker := range result.Blockers {
		writer.Printf("blockers.%d.code: %s\n", index, blocker.Code)
		writer.Printf("blockers.%d.message: %s\n", index, blocker.Message)
	}
	writer.Printf("message: %s\n", result.Message)
}

func writeSyncPlanHuman(writer *stickyWriter, result syncPlanResult) {
	if !result.Ready {
		writer.Println("Sync plan blocked")
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
