package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/BramVR/gohealthcli/internal/googlehealth"
	"math/rand"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// syncRunLifecycle owns the post-Validate path of a single-Data-Type
// Sync Run: archive open, cursor resume, token refresh, ingestion,
// terminal finalize, and the recovery write when the writer's
// retry-budget for SQLITE_BUSY is exhausted. Every return from
// lifecycle.Run produces a syncResult with a non-empty enum Status
// (sync_completed | sync_failed | sync_canceled) — the empty string is
// structurally impossible because every return path goes through
// syncResultFromOutcome (AC #2 of PRD #141 slice 4).
//
// Future deferral note (slice 1 architecture seam): the gate currently
// opens the archive once for currentConnection lookup, and Run reopens
// it. Threading the open handle through preflightPlan would remove the
// double-open but widens the gate interface and the orchestrator's
// per-Data-Type loop. Slice 4 keeps the double-open and addresses it
// in a future slice (see PRD #141 slice 4 design notes).
type syncRunLifecycle struct {
	options syncCommandOptions
	plan    preflightPlan
	runtime runtimeAdapters
}

// Run is the single entry point for the post-Validate flow. The
// returned syncResult always carries a non-empty Status; the error
// return is for the orchestrator's "did the per-Data-Type call
// surface a Go error" signal and is independent of Status (a sync
// can fail and still return nil error if the failure was already
// captured in result.Message + result.Status).
func (lifecycle syncRunLifecycle) Run(ctx context.Context) (syncResult, error) {
	runtime := lifecycle.runtime.withDefaults()
	options := lifecycle.options
	plan := lifecycle.plan
	// PRD #141 slice 5: close the SIGINT-pre-first-Data-Type race. The
	// orchestrator's per-Data-Type loop checks the context at the top of
	// the loop, but a signal that lands between that check and this
	// entry still races against StartSyncRun. Catching it here — before
	// StartSyncRun writes the audit row — keeps the no-audit-row
	// invariant from sync.md honest: gate.Validate already opened the
	// archive to read CurrentConnection, but that's a read and not part
	// of the sync_runs audit trail. A Sync Run canceled before it could
	// start writes zero sync_runs rows and surfaces a fully-populated
	// sync_canceled envelope (Status is never the empty string, AC #4).
	if ctx.Err() != nil {
		return syncResultFromOutcome(syncRunOutcomeCanceled, syncResult{
			DataTypes: plan.dataTypes,
			From:      plan.from,
			To:        plan.to,
			Message:   googlehealth.ErrSyncCanceled.Error(),
		}), googlehealth.ErrSyncCanceled
	}
	if len(plan.dataTypes) != 1 {
		return syncRunFailure(syncResult{DataTypes: plan.dataTypes},
			errors.New("sync currently supports one Data Type per run"))
	}
	dataType := plan.dataTypes[0]
	options.to = plan.to
	if plan.from != "" {
		options.from = plan.from
	}
	connection := plan.connection
	archive, err := runtime.openHealthArchiveWriter(options.archivePath)
	if err != nil {
		return syncRunFailure(syncResult{
			DataTypes: plan.dataTypes,
			From:      options.from,
			To:        options.to,
		}, err)
	}
	defer archive.Close()
	// Fence abandoned sync_running rows on the handle we already hold
	// (#236), so the audit trail never shows a corpse from a killed
	// process alongside this run's live row. Sitting AFTER the gate
	// means preflight rejections stay side-effect-free (no-audit-row
	// contract: a rejected `sync --rollup bogus` neither migrates nor
	// mutates the archive). Best-effort: a fence that loses a
	// SQLITE_BUSY race is retried by the next entry point; it must not
	// fail a sync that is otherwise ready to run.
	_, _ = archive.FenceAbandonedSyncRuns(ctx, runtime.now().UTC())
	config, err := inspectIdentityConfig(options.configPath, options.archivePath)
	if err != nil {
		return syncRunFailure(syncResult{
			DataTypes: plan.dataTypes,
			From:      options.from,
			To:        options.to,
		}, fmt.Errorf("config check failed: %w", err))
	}
	cursorKey := plan.cursorKeys[0]
	resumedFromCursor := false
	if options.from == "" {
		cursorTime, found, err := archive.ResolveSyncCursor(ctx, cursorKey)
		if err != nil {
			return syncRunFailure(syncResult{
				DataTypes: options.dataTypes,
				To:        options.to,
			}, fmt.Errorf("resolve Sync Cursor: %w", err))
		}
		if !found {
			return syncRunFailure(syncResult{
				DataTypes: options.dataTypes,
				To:        options.to,
			}, errors.New("sync has no Sync Cursor for this Data Type yet; set --from for the initial backfill"))
		}
		options.from = cursorTime
		resumedFromCursor = true
	}
	ingestion := newGoogleHealthIngestionWithRuntime(runtime)
	_, grantedScopes, err := connectionTokenExpiryAndScopes(connection.TokenMetadataJSON)
	if err != nil {
		return syncRunFailure(syncResult{
			DataTypes: options.dataTypes,
			From:      options.from,
			To:        options.to,
		}, err)
	}
	ingestionRequest := googlehealth.IngestionRequest{
		Connection:    connection,
		DataType:      dataType,
		From:          options.from,
		To:            options.to,
		Rollup:        options.rollup,
		SourceFamily:  options.sourceFamily,
		GrantedScopes: grantedScopes,
	}
	ingestionPlan, err := ingestion.Plan(ingestionRequest)
	if err != nil {
		return syncRunFailure(syncResult{
			DataTypes: options.dataTypes,
			From:      options.from,
			To:        options.to,
		}, err)
	}
	result := syncResult{
		ConnectionID:      connection.ID,
		ProviderName:      connection.ProviderName,
		DataTypes:         options.dataTypes,
		From:              options.from,
		To:                options.to,
		EndpointFamily:    ingestionPlan.EndpointFamily,
		SourceFamily:      options.sourceFamily,
		ResumedFromCursor: resumedFromCursor,
	}
	startedAt := runtime.now().UTC().Format(time.RFC3339)
	syncRunID, err := archive.StartSyncRun(ctx, syncRunStart{
		Connection:         connection,
		DataTypes:          options.dataTypes,
		From:               options.from,
		To:                 options.to,
		EndpointFamily:     result.EndpointFamily,
		SourceFamilyFilter: result.SourceFamily,
		StartedAt:          startedAt,
	})
	if err != nil {
		// StartSyncRun failed before any audit row was written: no
		// SyncRunID to populate. The status enum stays well-defined
		// (sync_failed) so the JSON envelope still satisfies AC #2. A
		// SIGINT that lands mid-INSERT surfaces as the canceled outcome
		// (#305), matching every other cancellation boundary.
		if ctx.Err() != nil {
			result.Message = googlehealth.ErrSyncCanceled.Error()
			return syncResultFromOutcome(syncRunOutcomeCanceled, result), googlehealth.ErrSyncCanceled
		}
		return syncRunFailure(result, err)
	}
	result.SyncRunID = syncRunID
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, []string{options.configPath, options.archivePath}, runtime)
	if config.oauthClient.kind == "file" {
		connectionAccess = connectionAccess.WithAutoRefresh(config.oauthClient, archive)
	}
	accessToken, err := connectionAccess.AccessToken(googlehealth.ScopesForDataType(dataType))
	if err != nil {
		return lifecycle.finalize(ctx, archive, result, syncRunID, cursorKey, options.to, syncRunOutcomeFailed, err)
	}
	if _, err := connectionAccess.FetchVerifiedIdentity(accessToken); err != nil {
		return lifecycle.finalize(ctx, archive, result, syncRunID, cursorKey, options.to, syncRunOutcomeFailed, err)
	}
	ingestionRequest.AccessToken = accessToken
	// Per-page heartbeat (#236): before every page fetch the counts so
	// far plus last_progress_at land on the sync_running row, so a
	// concurrent `sync --status` poller sees live progress instead of
	// 0/0/0 until finalize. Best-effort by design — a heartbeat that
	// loses a SQLITE_BUSY race is dropped, the next page writes a fresh
	// one, and FinalizeSyncRun remains the authoritative terminal write.
	// Totals go through the same applyGoogleHealthIngestionCounts +
	// syncResultTotalCounts pair the finalize uses, so the advisory and
	// authoritative counts cannot drift when a new count family is added.
	ingestionRequest.Progress = func(counts googlehealth.IngestionResult) {
		var snapshot syncResult
		applyGoogleHealthIngestionCounts(&snapshot, counts)
		seen, newCount, updated := syncResultTotalCounts(snapshot)
		_ = archive.HeartbeatSyncRun(ctx, syncRunHeartbeat{
			SyncRunID:    syncRunID,
			SeenCount:    seen,
			NewCount:     newCount,
			UpdatedCount: updated,
			At:           runtime.now().UTC().Format(time.RFC3339),
		})
	}
	ingestionRequest.RefreshAccessToken = connectionAccess.MidRunTokenRefresher()
	ingestionResult, err := ingestion.Execute(ctx, archive, ingestionRequest)
	applyGoogleHealthIngestionCounts(&result, ingestionResult)
	if err != nil {
		if errors.Is(err, googlehealth.ErrSyncCanceled) {
			return lifecycle.finalize(ctx, archive, result, syncRunID, cursorKey, options.to, syncRunOutcomeCanceled, err)
		}
		return lifecycle.finalize(ctx, archive, result, syncRunID, cursorKey, options.to, syncRunOutcomeFailed, err)
	}
	if options.rollup != "" {
		result.Message = fmt.Sprintf("Sync Run archived %s %s Rollups", dataType, options.rollup)
	} else if options.sourceFamily != "" {
		result.Message = fmt.Sprintf("Sync Run archived %s Data Points with source-family filter", dataType)
	} else {
		result.Message = fmt.Sprintf("Sync Run archived %s Data Points", dataType)
	}
	return lifecycle.finalize(ctx, archive, result, syncRunID, cursorKey, options.to, syncRunOutcomeCompleted, nil)
}

// finalize is the single terminal-write seam. Every Sync Run that
// reached the audit-row state passes through here, so the SyncRunID
// populates the returned syncResult on every path (AC #3) and the
// status enum is sealed by syncResultFromOutcome.
//
// When FinalizeSyncRun returns errFinalizeSyncRunBusyExhausted, the
// writer's retry budget against SQLITE_BUSY ran out. The lifecycle
// converts that into sync_failed with a contention-aware message and
// writes a recovery row in a separate short transaction so the
// sync_runs row never lingers as sync_running. The cursor remains
// untouched because the recovery write goes through FinishSyncRun
// (not Finalize) and the original Finalize never committed.
func (lifecycle syncRunLifecycle) finalize(ctx context.Context, archive healthArchiveWriter, result syncResult, syncRunID int64, cursorKey syncCursorKey, cursorTo string, outcome syncRunOutcome, cause error) (syncResult, error) {
	runtime := lifecycle.runtime.withDefaults()
	// The terminal audit write must complete even when the run's context
	// is already canceled — a SIGINT'd run owes the audit trail its
	// sync_canceled row (the orchestrator cancel test pins the persisted
	// status). WithoutCancel detaches cancellation while preserving the
	// context's values (#305).
	ctx = context.WithoutCancel(ctx)
	result.SyncRunID = syncRunID
	result = syncResultFromOutcome(outcome, result)
	errorSummary := ""
	if cause != nil {
		result.Message = cause.Error()
		errorSummary = result.Message
	}
	now := runtime.now().UTC().Format(time.RFC3339)
	seen, newCount, updated := syncResultTotalCounts(result)
	finalizeErr := archive.FinalizeSyncRun(ctx, syncRunFinalize{
		SyncRunID:      syncRunID,
		Outcome:        outcome,
		SeenCount:      seen,
		NewCount:       newCount,
		UpdatedCount:   updated,
		FinishedAt:     now,
		ErrorSummary:   errorSummary,
		CursorKey:      cursorKey,
		CursorTo:       cursorTo,
		CursorAdvanced: now,
	})
	if finalizeErr == nil {
		return result, cause
	}
	// The atomic finalize failed (rolled back). The sync_runs row is
	// still in StartSyncRun's sync_running state and no cursor advance
	// happened. Branch: a SQLITE_BUSY exhaustion gets a dedicated
	// contention message, everything else gets the historical wrap.
	// Either way we drive the row to a terminal state via a separate
	// short transaction (FinishSyncRun) so concurrent invocations
	// never leave dangling sync_running rows.
	var busyExhausted *errFinalizeSyncRunBusyExhausted
	if errors.As(finalizeErr, &busyExhausted) {
		result.Message = fmt.Sprintf("FinalizeSyncRun lost contention against another writer after %d attempts: %v", busyExhausted.attempts, busyExhausted.cause)
	} else {
		result.Message = finalizeErr.Error()
	}
	// Wrap so the finalize error is the %w (errors.As reach typed
	// errFinalizeSyncRunBusyExhausted from callers); fold in the
	// underlying cause as %v so it survives in the message without
	// clobbering the typed-error chain.
	finalErr := fmt.Errorf("record %s Sync Run: %w", outcome, finalizeErr)
	if cause != nil {
		finalErr = fmt.Errorf("%w (after upstream cause: %v)", finalErr, cause) //nolint:errorlint // deliberate non-wrapping %v, per the comment above
	}
	// Finding 5: preserve sync_canceled through the recovery write so
	// the audit trail does not misrepresent a user cancellation as a
	// contention failure. sync_completed always becomes sync_failed
	// (slice 1 behavior: cursor cannot be atomically advanced after a
	// rolled-back finalize). sync_canceled stays sync_canceled —
	// Canceled.AdvancesCursor() returns false anyway so the cursor
	// invariant is preserved either way.
	recoveryStatus := "sync_failed"
	envelopeOutcome := syncRunOutcomeFailed
	if outcome == syncRunOutcomeCanceled {
		recoveryStatus = string(syncRunOutcomeCanceled)
		envelopeOutcome = syncRunOutcomeCanceled
	}
	result = syncResultFromOutcome(envelopeOutcome, result)
	// Finding 2: the recovery write itself runs under the same retry
	// budget+backoff because if the original finalize lost the lock,
	// the recovery write almost certainly hits the same contention.
	recoveryErr := retryFinalizeSyncRunOnBusyWithSleep(finalizeSyncRunRetryBudget, runtime.sleep, func() error {
		return archive.FinishSyncRun(ctx, syncRunFinish{
			SyncRunID:    syncRunID,
			Status:       recoveryStatus,
			SeenCount:    seen,
			NewCount:     newCount,
			UpdatedCount: updated,
			FinishedAt:   now,
			ErrorSummary: result.Message,
		})
	})
	if recoveryErr != nil {
		// Finding 3: errors.Join preserves BOTH typed chains —
		// errFinalizeSyncRunBusyExhausted from the finalize side AND
		// the typed error from the recovery side — so callers using
		// errors.As on either can still branch on contention.
		finalErr = errors.Join(finalErr, fmt.Errorf("mark Sync Run %s (recovery): %w", recoveryStatus, recoveryErr))
	}
	return result, finalErr
}

// syncRunFailure is the single failure-return seam for the lifecycle's
// pre-finalize phases (#277): Status is sealed to sync_failed via
// syncResultFromOutcome, Message mirrors the cause text, and the cause
// itself is returned as the error so the orchestrator's
// "did the per-Data-Type call surface a Go error" signal keeps its
// identity. base carries whichever envelope fields the failing phase
// already knows (DataTypes, From, To — or the fully-populated result
// once StartSyncRun has been attempted).
func syncRunFailure(base syncResult, cause error) (syncResult, error) {
	base.Message = cause.Error()
	return syncResultFromOutcome(syncRunOutcomeFailed, base), cause
}

// finalizeSyncRunRetryBudget bounds how many attempts the SQLite
// adapter makes when it sees SQLITE_BUSY during FinalizeSyncRun. The
// value is small enough that the operator does not stare at a hung
// terminal but large enough that brief contention from a sibling
// reader or a competing Sync Run does not surface as a failure. The
// modernc.org driver does not implement a global busy_timeout pragma
// the way mattn's binding does, so this loop owns the policy.
const finalizeSyncRunRetryBudget = 8

// retryFinalizeSyncRunOnBusy invokes attempt repeatedly while the
// returned error matches the SQLITE_BUSY contention predicate, up to
// budget total attempts. When the budget is exhausted the helper
// returns errFinalizeSyncRunBusyExhausted wrapping the last underlying
// error so the lifecycle module can branch via errors.As. Non-busy
// errors short-circuit on the first occurrence so unrelated failures
// (constraint violations, IO errors) surface immediately.
//
// The wrapper sleeps between busy attempts via time.Sleep.
// retryFinalizeSyncRunOnBusyWithSleep is the seam tests use to pass a
// no-op sleeper without consuming wallclock time.
func retryFinalizeSyncRunOnBusy(budget int, attempt func() error) error {
	return retryFinalizeSyncRunOnBusyWithSleep(budget, time.Sleep, attempt)
}

// retryFinalizeSyncRunOnBusyWithSleep is the injectable-sleep variant
// of retryFinalizeSyncRunOnBusy. Production callers use the wrapper
// above which threads finalizeSyncRunSleeper through. Tests pass a
// recording or no-op sleep so they neither block on wallclock time nor
// silently turn the loop into a hot-loop.
func retryFinalizeSyncRunOnBusyWithSleep(budget int, sleep func(time.Duration), attempt func() error) error {
	var lastErr error
	for i := 0; i < budget; i++ {
		lastErr = attempt()
		if lastErr == nil {
			return nil
		}
		if !isSQLiteBusy(lastErr) {
			return lastErr
		}
		// Skip the trailing sleep after the final attempt — no point
		// waiting if there's no retry left.
		if i < budget-1 {
			sleep(finalizeSyncRunBackoff(i))
		}
	}
	return &errFinalizeSyncRunBusyExhausted{attempts: budget, cause: lastErr}
}

// finalizeSyncRunBackoff returns the backoff duration before the
// (attempt+1)-th retry, given that attempt index `attempt` (zero-based)
// just observed SQLITE_BUSY. The shape is exponential with full
// jitter, capped at finalizeSyncRunBackoffCap. A pure exponential
// without jitter would cause two contending writers to keep colliding
// in lockstep; jitter desynchronizes them.
func finalizeSyncRunBackoff(attempt int) time.Duration {
	base := finalizeSyncRunBackoffBase << attempt
	if base <= 0 || base > finalizeSyncRunBackoffCap {
		base = finalizeSyncRunBackoffCap
	}
	// Full jitter: a uniform random in (0, base]. The +1 ensures the
	// minimum is one nanosecond so callers never observe a zero-sleep
	// hot-loop dressed up as "backoff".
	jittered := rand.Int63n(int64(base)) + 1
	return time.Duration(jittered)
}

const (
	finalizeSyncRunBackoffBase = 50 * time.Millisecond
	finalizeSyncRunBackoffCap  = 1 * time.Second
)

// isSQLiteBusy returns true when err originates from the SQLite
// adapter reporting SQLITE_BUSY (or its extended variants). The
// modernc.org driver wraps the result code in *sqlite.Error so we
// match on Code() first; a string-prefix fallback covers errors that
// drop the typed wrapper (for example wrapped with fmt.Errorf %v).
func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code()
		if code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_BUSY_SNAPSHOT || code == sqlite3.SQLITE_BUSY_RECOVERY {
			return true
		}
	}
	return strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "SQLITE_BUSY")
}

// syncResultFromOutcome is the ONLY constructor the lifecycle uses to
// produce a syncResult. It guarantees AC #2 of PRD #141 slice 4: a
// returned syncResult always carries a non-empty enum Status
// (sync_completed | sync_failed | sync_canceled). The base argument
// supplies the rest of the envelope (counts, ids, message) so callers
// do not have to thread Status through every error path by hand.
func syncResultFromOutcome(outcome syncRunOutcome, base syncResult) syncResult {
	base.Status = string(outcome)
	return base
}

// errFinalizeSyncRunBusyExhausted is the typed error the SQLite writer
// surfaces when FinalizeSyncRun's retry budget is exhausted by
// repeated SQLITE_BUSY responses. The lifecycle module recognises this
// via errors.As, marks the Sync Run sync_failed in a fresh short
// transaction, and surfaces a contention message to the operator.
// attempts is the number of attempts made (including the final
// failure); cause is the last underlying SQLite error.
type errFinalizeSyncRunBusyExhausted struct {
	attempts int
	cause    error
}

func (err *errFinalizeSyncRunBusyExhausted) Error() string {
	return fmt.Sprintf("FinalizeSyncRun exhausted %d retries against SQLITE_BUSY: %v", err.attempts, err.cause)
}

func (err *errFinalizeSyncRunBusyExhausted) Unwrap() error { return err.cause }
