package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
)

// Identity Snapshot kind constants (issue #282). One definition shared
// by the commands that write snapshots, the Health Archive reader that
// projects them, and the status freshness loop — so a typo'd kind
// string can no longer split the archive into rows no view will ever
// project. The values are the CONTEXT.md terms and are part of the
// stored data contract (identity_snapshots.snapshot_kind), so they
// never change casually; the Normalized View SQL in views_identity.go
// quotes the same literals inside view bodies owned by the views
// registry.
const (
	snapshotKindProfile       = "profile"
	snapshotKindSettings      = "settings"
	snapshotKindPairedDevices = "paired-devices"
	snapshotKindIRNProfile    = "irn-profile"
)

// identitySnapshotKinds lists every snapshot kind in the stable order
// `status` emits freshness lines (and the order CONTEXT.md documents).
var identitySnapshotKinds = []string{snapshotKindProfile, snapshotKindSettings, snapshotKindPairedDevices, snapshotKindIRNProfile}

// identitySnapshotCommandSpec drives the Identity Snapshot command
// engine (issue #282) for one command of the family: devices,
// settings, irn-profile, profile, and identity. Before the engine,
// each command was the same ~250-line module copy-pasted — parse
// flags → config → open archive → Connection access (+auto-refresh) →
// access token → fetch → snapshot handoff → render — and the copies
// had already drifted once (identity silently skipping auto-refresh,
// resolved as parity by #273). The spec carries everything that
// genuinely differs per command; the engine owns the pipeline once.
//
// R is the command's result struct (devicesResult, settingsResult, …):
// kept per-command so every JSON/plain/human output shape stays
// bit-for-bit identical to the pre-engine command. P is the
// command's provider payload type (googlePairedDevices, …) so
// decorations receive the typed payload their fetcher seam returned —
// no re-parsing, no type assertions.
type identitySnapshotCommandSpec[R, P any] struct {
	// command is the CLI subcommand name, used for the FlagSet, the
	// unexpected-argument wording, and Failure Reporter envelopes.
	command string

	// commonFlags returns the Common Flag Set contract the command
	// accepts. devices/settings/irn-profile omit --no-input (issue
	// #171: they never block on browser input); identity/profile
	// accept all five shared flags. A func — not a value — so each
	// invocation gets a fresh Accepted slice, mirroring the
	// identitySnapshotCommonFlagNames convention.
	commonFlags func() CommonFlagSpec

	// Per-command statuses for the engine-owned failure paths. The
	// success status lives in finishArchived (or act) because its
	// assembly is per-command.
	statusFailed       string // statusless-error fallback, e.g. "devices_failed"
	statusUnavailable  string // no Connection on file, e.g. "devices_unavailable"
	statusScopeMissing string // scope pre-check sentinel, e.g. "devices_scope_missing"

	// scopeEndpointKey selects the required scopes from the
	// googleHealthIdentityEndpointScopes catalog (PRD #142), so a
	// catalog revision flows into the command automatically.
	scopeEndpointKey string

	// seedResult builds the result struct from the archived
	// Connection once it is known (after the unavailable check).
	seedResult func(connection archivedConnection) R

	// status / setStatus / setMessage are the engine's window into
	// the per-command result struct, keeping R free of interface
	// obligations.
	status     func(result *R) string
	setStatus  func(result *R, status string)
	setMessage func(result *R, message string)

	// writeResult renders the result in the requested output mode.
	writeResult func(result R, mode outputMode, stdout io.Writer) error

	// snapshotKind tags the archived row (one of the snapshotKind*
	// constants). Unused when act overrides the snapshot flow.
	snapshotKind string

	// fetchPayload fetches the provider payload with a usable access
	// token. Closures call the per-command runtime-adapters seam
	// (runtime.fetchPairedDevices / fetchSettings / fetchIRNProfile /
	// fetchProfile) at invocation time, so tests inject fakes through
	// the adapters value (#283).
	fetchPayload func(runtime runtimeAdapters, accessToken string) (P, error)

	// payloadRawJSON projects the payload's verbatim provider JSON —
	// the bytes the snapshot handoff archives.
	payloadRawJSON func(payload P) string

	// decorate (optional) enriches the result from the fetched
	// payload before the handoff: devices parses its per-device
	// summaries here so a later handoff failure still reports them.
	decorate func(result *R, payload P)

	// verifyPayload (optional) runs after decorate and before the
	// handoff: profile verifies the payload belongs to the archived
	// Google Identity here, setting its own mismatch status.
	verifyPayload func(engine identitySnapshotCommandContext, result *R, payload P) error

	// finishArchived assembles the per-command success fields after
	// the snapshot handoff: archived status, snapshot ID, fetched-at,
	// and message.
	finishArchived func(result *R, snapshotID int64, fetchedAt string)

	// act (optional) replaces the entire fetch→decorate→verify→handoff
	// phase for a command whose post-token behavior is genuinely
	// unique: identity refreshes the archived Connection identity
	// metadata instead of archiving a snapshot (#273 parity decision
	// brought it into the engine).
	act func(engine identitySnapshotCommandContext, result *R) error
}

// identitySnapshotCommandContext is the per-invocation state the
// engine hands to spec decorations (verifyPayload, act) once a usable
// access token exists.
type identitySnapshotCommandContext struct {
	archivePath      string
	runtime          runtimeAdapters
	archive          healthArchiveConnectionAPI
	connection       archivedConnection
	connectionAccess currentConnectionAccess
	accessToken      string
}

// runIdentitySnapshotCommand is the engine's runner: the uniform parse
// flags → setup → render → exit-code shell every Identity Snapshot
// family command shares. Failure rendering matches the pre-engine
// commands bit-for-bit: a setup error renders the result with the
// per-command failed status as fallback and exits 1; a result-writer
// error routes through the Failure Reporter as archive_unwritable.
func runIdentitySnapshotCommand[R, P any](spec identitySnapshotCommandSpec[R, P], args []string, globals CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet(spec.command, flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, spec.commonFlags(), CommonFlagValues{
		ConfigPath:  globals.ConfigPath,
		ArchivePath: globals.ArchivePath,
		JSONOutput:  globals.JSONOutput,
		PlainOutput: globals.PlainOutput,
	})

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode := commonOutputMode(*common)
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: spec.command,
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected %s argument: %s", spec.command, flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	result, err := identitySnapshotSetupWithRuntime(spec, common.ConfigPath, common.ArchivePath, runtime)
	if err != nil {
		if spec.status(&result) == "" {
			spec.setStatus(&result, spec.statusFailed)
		}
		spec.setMessage(&result, err.Error())
		if writeErr := spec.writeResult(result, mode, stdout); writeErr != nil {
			return reportWriteFailure(spec.command, writeErr, mode, stdout, stderr)
		}
		return 1
	}
	if err := spec.writeResult(result, mode, stdout); err != nil {
		return reportWriteFailure(spec.command, err, mode, stdout, stderr)
	}
	return 0
}

// identitySnapshotSetupWithRuntime is the engine's setup pipeline:
// config check, archive open, current Connection (with the shared
// no-Connection wording), Connection access with auto-refresh for
// file-based OAuth clients (PRD #142; identity joined via #273), the
// scope pre-check inside AccessToken, then the per-command post-token
// phase — the standard fetch → decorate → verify → snapshot handoff
// flow, or the spec's act override.
func identitySnapshotSetupWithRuntime[R, P any](spec identitySnapshotCommandSpec[R, P], configPath, archivePath string, runtime runtimeAdapters) (R, error) {
	var result R
	runtime = runtime.withDefaults()
	config, err := inspectIdentityConfig(configPath, archivePath)
	if err != nil {
		return result, fmt.Errorf("config check failed: %w", err)
	}
	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		return result, err
	}
	// archive is closed either by writeIdentitySnapshotHandoff (success
	// path) or by this deferred guard (any error before handoff, and
	// the act override which never hands off).
	archiveClosed := false
	defer func() {
		if !archiveClosed {
			_ = archive.Close()
		}
	}()
	connection, err := archive.CurrentConnection()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			spec.setStatus(&result, spec.statusUnavailable)
			return result, errors.New("no Connection found; run `gohealthcli connect` first")
		}
		return result, err
	}
	result = spec.seedResult(connection)
	// The deepened currentConnectionAccess pattern (PRD #142): wire
	// WithAutoRefresh when the OAuth client is a file source — the
	// archive handle openHealthArchiveConnectionAPI returned already
	// satisfies connectionTokenWriter — so an expired access token
	// refreshes and persists transparently, the way
	// sync_run_lifecycle.go already does. The scope pre-check happens
	// inside AccessToken via the errCurrentConnectionScopeMissing
	// sentinel, so the engine sets the per-command status without
	// re-implementing the scope-list comparison locally.
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, []string{configPath, archivePath}, runtime)
	if config.oauthClient.kind == "file" {
		connectionAccess = connectionAccess.WithAutoRefresh(config.oauthClient, archive)
	}
	accessToken, err := connectionAccess.AccessToken(googleHealthIdentityEndpointScopes[spec.scopeEndpointKey])
	if err != nil {
		if errors.Is(err, errCurrentConnectionScopeMissing) {
			spec.setStatus(&result, spec.statusScopeMissing)
		}
		return result, err
	}
	engine := identitySnapshotCommandContext{
		archivePath:      archivePath,
		runtime:          runtime,
		archive:          archive,
		connection:       connection,
		connectionAccess: connectionAccess,
		accessToken:      accessToken,
	}
	if spec.act != nil {
		return result, spec.act(engine, &result)
	}
	payload, err := spec.fetchPayload(runtime, accessToken)
	if err != nil {
		// Provider outage (non-auth HTTP failure or network error) gets
		// its own documented JSON failure status so automation can tell
		// it apart from local misconfiguration (issue #272).
		if isProviderUnreachableError(err) {
			spec.setStatus(&result, "provider_unreachable")
		}
		return result, normalizeProviderError(err)
	}
	if spec.decorate != nil {
		spec.decorate(&result, payload)
	}
	if spec.verifyPayload != nil {
		if err := spec.verifyPayload(engine, &result, payload); err != nil {
			return result, err
		}
	}
	fetchedAt := runtime.now().UTC().Format(time.RFC3339)
	// context.Background(): the Identity Snapshot commands are
	// synchronous fetch-then-archive flows with no cancellation path
	// today (their Provider fetches ride context.Background() the same
	// way, #284); the context keeps the snapshot write on the Context
	// API (#305) without changing behavior.
	snapshotID, err := writeIdentitySnapshotHandoff(context.Background(), archive, archivePath, connection, spec.snapshotKind, spec.payloadRawJSON(payload), fetchedAt)
	archiveClosed = true // handoff owns archive's lifecycle now
	if err != nil {
		return result, err
	}
	spec.finishArchived(&result, snapshotID, fetchedAt)
	return result, nil
}
