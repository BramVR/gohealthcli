package main

import "io"

// commandDef describes a single gohealthcli subcommand for both documentation
// and dispatch. The Project Site's command-reference pages are generated from
// the JSON encoding of this slice via `gohealthcli schema --json` — keep field
// names stable, because they are part of the contract downstream tooling reads.
//
// Fields on the published JSON contract:
//   - name            (string)              — the subcommand's invocation name
//   - short           (string)              — one-line description for the index
//   - long            (string)              — full prose for the per-page body
//   - hidden          (bool)                — hidden from --help and reference
//   - positional_args (string, optional)    — usage hint for trailing positional
//                                             arguments (e.g. "<SQL>"); omitted
//                                             entirely when empty
//   - flags           (array of flagSpec)   — flag specifications
//   - common_flags    (array of strings, optional) — subset of the five shared
//                                             flag names that the subcommand
//                                             accepts via the runtime
//                                             CommonFlagSet module (issue #166).
//                                             Omitted entirely when empty so
//                                             the wire shape stays additive.
//
// Run is the dispatch adapter — invoked by runWithRuntime after a successful
// registry lookup (PRD #143 slice 6, issue #175). The `json:"-"` tag keeps
// the function out of the published JSON contract: the schema documents the
// surface, the adapter implements the call. Subcommands whose underlying
// signature differs (status's ArchivePathExplicit, export's no-runtime,
// schema's stdout/stderr-only) satisfy this uniform type via thin wrappers
// that fold the differences into CommonFlagValues or ignore unused params.
type commandDef struct {
	Name           string                                                                                              `json:"name"`
	Short          string                                                                                              `json:"short"`
	Long           string                                                                                              `json:"long"`
	Hidden         bool                                                                                                `json:"hidden"`
	PositionalArgs string                                                                                              `json:"positional_args,omitempty"`
	Flags          []flagSpec                                                                                          `json:"flags"`
	CommonFlags    []string                                                                                            `json:"common_flags,omitempty"`
	Run            func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int `json:"-"`
}

// flagSpec describes one flag accepted by a subcommand. The string-typed
// Default field carries the literal default value emitted in the schema.
//
// Flags whose real runtime default is platform-dependent (XDG-derived paths,
// OS-resolved state) carry the empty string here; the Project Site generator
// renders the em-dash rather than hard-coding a path that would be wrong on
// other machines. Document the resolved location in prose (long descriptions,
// README, install page) rather than baking a per-host value into the schema.
type flagSpec struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default string `json:"default"`
	Usage   string `json:"usage"`
}

// schemaVersion is the version of the schema --json payload's outer shape.
// Bump when a backwards-incompatible field change ships; the Node generator
// pins this version so a drift fails the build instead of producing wrong
// markdown.
const commandSchemaVersion = 1

// lookupCommand returns the registry entry whose Name matches the given
// subcommand. It deliberately INCLUDES hidden entries: the `help <cmd>` verb
// (PRD #143 slice 2) must surface the prose for hidden build-time commands
// like `schema` when asked explicitly, even though they are filtered from the
// top-level `--help` listing.
func lookupCommand(name string) (commandDef, bool) {
	for _, cmd := range commands {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return commandDef{}, false
}

// commonFlags are the five shared flags that the standard output subcommands
// (init, doctor, connect, identity, profile, sync, status, query) accept.
// `export` and `raw` use different flag sets — see their explicit Flags slices
// below — because their output semantics differ.
//
// Centralising the shared flags here keeps the registry concise; the dedicated
// commonFlagsSpec module that #76 introduces will collapse them further.
var commonFlags = []flagSpec{
	{Name: "config", Type: "string", Default: "", Usage: "config file path"},
	{Name: "db", Type: "string", Default: "", Usage: "SQLite Health Archive path"},
	{Name: "json", Type: "bool", Default: "false", Usage: "write stable JSON to stdout"},
	{Name: "plain", Type: "bool", Default: "false", Usage: "write plain key/value output to stdout"},
	{Name: "no-input", Type: "bool", Default: "false", Usage: "never prompt, never wait for browser input"},
}

func withCommon(extra ...flagSpec) []flagSpec {
	out := make([]flagSpec, 0, len(commonFlags)+len(extra))
	out = append(out, commonFlags...)
	out = append(out, extra...)
	return out
}

// identitySnapshotCommonFlagNames returns the subset of common flag
// names the three Identity Snapshot reach commands (settings, devices,
// irn-profile) accept. They never block on browser input, so --no-input
// is intentionally omitted: declaring it would imply a behaviour the
// commands do not have (issue #171). The Common Flag Set's pre-Parse
// scan rejects a stray --no-input with the targeted "--no-input is
// not supported by <cmd>" wording. Returned fresh each call to mirror
// commonFlagNames so per-entry CommonFlags slices stay independent.
func identitySnapshotCommonFlagNames() []string {
	return []string{"config", "db", "json", "plain"}
}

// withCommonSubset is the per-subcommand variant of withCommon that
// projects only the named shared flags (in the canonical commonFlags
// order) into the registry entry's Flags slice. Used by subcommands
// whose runtime CommonFlagSpec.Accepted is a strict subset of the
// five shared flags, so the help block and `--json` schema reflect
// the runtime contract exactly.
//
// Unknown or duplicated names panic at init() — registry-build mistakes
// are programmer errors, not runtime conditions, and silently dropping
// a misspelled name would reintroduce the help/schema drift this
// helper exists to prevent (mirroring withCommonOverrides' contract).
func withCommonSubset(names []string, extra ...flagSpec) []flagSpec {
	known := make(map[string]bool, len(commonFlags))
	for _, flag := range commonFlags {
		known[flag.Name] = true
	}
	include := make(map[string]bool, len(names))
	for _, name := range names {
		if !known[name] {
			panic("withCommonSubset: unknown common flag " + name)
		}
		if include[name] {
			panic("withCommonSubset: duplicate common flag " + name)
		}
		include[name] = true
	}
	out := make([]flagSpec, 0, len(names)+len(extra))
	for _, flag := range commonFlags {
		if include[flag.Name] {
			out = append(out, flag)
		}
	}
	out = append(out, extra...)
	return out
}

// withCommonOverrides returns the shared flagSpec slice with the Usage
// strings for the named common flags overridden. The Project Site's
// command-reference pages and `gohealthcli schema --json` both read the
// per-flag Usage as the source of truth, so a subcommand whose
// `--plain` / `--json` semantics differ from the generic "write stable
// JSON" / "write plain key/value" wording must override the description
// at registry time — otherwise the published schema would say one thing
// and the live `--help` output would say another. The overrides map
// from common flag name to the subcommand-specific Usage string;
// unknown names panic at init() because registry-build mistakes are
// programmer errors, not runtime conditions.
func withCommonOverrides(overrides map[string]string, extra ...flagSpec) []flagSpec {
	out := make([]flagSpec, 0, len(commonFlags)+len(extra))
	for _, flag := range commonFlags {
		if override, ok := overrides[flag.Name]; ok {
			flag.Usage = override
		}
		out = append(out, flag)
	}
	for name := range overrides {
		found := false
		for _, flag := range commonFlags {
			if flag.Name == name {
				found = true
				break
			}
		}
		if !found {
			panic("withCommonOverrides: unknown common flag " + name)
		}
	}
	out = append(out, extra...)
	return out
}

// commonFlagNames returns the five shared flag names in their canonical
// commonFlags order. Registry entries for subcommands whose runtime flag
// setup goes through the CommonFlagSet module (issue #166) carry this
// slice as their `common_flags` schema field, so downstream tooling can
// see at a glance which subset of shared flags each subcommand accepts.
func commonFlagNames() []string {
	names := make([]string, 0, len(commonFlags))
	for _, f := range commonFlags {
		names = append(names, f.Name)
	}
	return names
}

// commonOutputMode collapses the CommonFlagValues' json/plain pair into
// the outputMode struct every subcommand's WithRuntime entry point already
// expects. Keeping the projection in one place means a future shared-flag
// addition (a hypothetical --quiet) only has to be wired here and in the
// CommonFlagSet module — not threaded through every adapter inline.
func commonOutputMode(common CommonFlagValues) outputMode {
	return outputMode{json: common.JSONOutput, plain: common.PlainOutput}
}

// commands is the registry of every subcommand the binary exposes. The
// dispatch path (`runWithRuntime` after `--version` / `--help`) and the
// `--help` printer both read from this slice — there is no parallel switch.
// PRD #143 slice 6 (issue #175): every entry's Run adapter binds the
// uniform `(args, common, stdout, stderr, runtime) → int` shape down to
// each subcommand's existing entry point, folding the diverged signatures
// (status's ArchivePathExplicit, export's no-runtime, schema's minimal)
// into thin wrappers that ignore unused params.
//
// Entries are listed in the order that they should appear in the Project
// Site sidebar and the auto-generated command-reference index.
var commands = []commandDef{
	{
		Name:  "init",
		Short: "Create local config and an empty Health Archive.",
		Long:  "Initialise a fresh `gohealthcli` install: write the config file, create the Health Archive on disk, and run the initial schema migration. After `init` finishes the binary is ready for `connect`.\n\n`--oauth-client-file` points at a Google OAuth Desktop-app client JSON downloaded from the Google Cloud console (see the [Install](../install.html) page). `--secret-provider` and `--oauth-client-item` are an alternative path that pulls the client from a Secret Provider (for example, 1Password) instead of a file.\n\n`init` never overwrites an existing Health Archive; rerun with a different `--db` to create a second archive in a separate location.",
		Flags: withCommon(
			flagSpec{Name: "oauth-client-file", Type: "string", Default: "", Usage: "OAuth client JSON file reference"},
			flagSpec{Name: "secret-provider", Type: "string", Default: "", Usage: "Secret Provider name for OAuth client setup"},
			flagSpec{Name: "oauth-client-item", Type: "string", Default: "", Usage: "Secret Provider item name for OAuth client setup"},
		),
		CommonFlags: commonFlagNames(),
		// init runs entirely against the local filesystem (config + archive),
		// so the runtime adapter bundle is ignored.
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, _ runtimeAdapters) int {
			return runInit(args, common.ConfigPath, common.ArchivePath, commonOutputMode(common), stdout, stderr)
		},
	},
	{
		Name:  "doctor",
		Short: "Validate local setup and provider reachability.",
		Long:  "Run a diagnostic check against the local gohealthcli installation: config presence, Health Archive path, Credential Store status, schema version, and connection count.\n\nThe report also includes the Data Point Attachment Store: the `attachment_root_path` and `attachment_root_mode` it owns, plus an `attachments` block listing orphan sidecar files (file on disk with no matching row) and orphan rows (row in the archive whose sidecar file is gone). In `--plain` mode the orphan counts surface as `attachments_orphan_files: N` and `attachments_orphan_rows: N` lines, emitted only when the count is positive. `doctor` never modifies the archive or the sidecar tree — it reports only.\n\nWith `--online`, also refresh stored tokens and verify Google Health API reachability. The command never writes health data; it only inspects local state and (with `--online`) performs a single read-only round trip to the provider.\n\nThe output is a structured report on stdout. Use `--json` for stable machine-readable output, `--plain` for terminal-friendly key/value lines.",
		Flags: withCommon(
			flagSpec{Name: "online", Type: "bool", Default: "false", Usage: "refresh tokens and check provider reachability"},
		),
		CommonFlags: commonFlagNames(),
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runDoctorWithRuntime(args, common.ConfigPath, common.ArchivePath, commonOutputMode(common), stdout, stderr, runtime)
		},
	},
	{
		Name:  "connect",
		Short: "Run the browser OAuth flow and anchor one Google Identity.",
		Long:  "Open the system browser, run the installed-app OAuth flow against the OAuth client supplied at `init`, and store the resulting tokens in the OS-native Credential Store (Keychain on macOS, Credential Manager on Windows, Secret Service on Linux).\n\nA Health Archive holds exactly one Connection. Running `connect` against an archive that already has a Connection refreshes the token material in place rather than adding a second identity.\n\n`--add-scopes` extends an existing grant with optional scope keywords (`irn`, `ecg`, `nutrition`, `tcx`) without re-running setup; Google's `include_granted_scopes=true` makes the resulting token cover the union of prior + new scopes. Use `connect --add-scopes irn` to unlock `gohealthcli irn-profile` and Tier 2 ECG / IRN Data Types; use `connect --add-scopes nutrition` to unlock hydration-log; use `connect --add-scopes tcx` to unlock TCX route archival on exercise sync (grants `googlehealth.location.readonly`, required on top of `activity_and_fitness.readonly` for Google's `exportExerciseTcx` endpoint).\n\n`--no-input` makes the command fail with a non-zero exit code if the browser flow would block (useful in CI smoke tests after the tokens are already provisioned).",
		Flags: withCommon(
			flagSpec{Name: "add-scopes", Type: "string", Default: "", Usage: "extend the OAuth grant with optional scope keywords (csv): irn, ecg, nutrition, tcx"},
		),
		CommonFlags: commonFlagNames(),
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runConnectWithRuntime(args, common.ConfigPath, common.ArchivePath, common.NoInput, commonOutputMode(common), stdout, stderr, runtime)
		},
	},
	{
		Name:        "identity",
		Short:       "Refresh the archived Google Identity metadata.",
		Long:        "Re-fetch the upstream Google Identity payload (Google Health user ID and legacy Fitbit user ID when present) and update the metadata stored alongside the Connection.\n\n`identity` does not change the OAuth tokens or move the Connection between archives — use `connect` for those. It is a low-cost, read-only operation against the provider.",
		Flags:       withCommon(),
		CommonFlags: commonFlagNames(),
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runIdentityWithRuntime(args, common.ConfigPath, common.ArchivePath, commonOutputMode(common), stdout, stderr, runtime)
		},
	},
	{
		Name:        "profile",
		Short:       "Archive a Profile Snapshot from the provider.",
		Long:        "Fetch the upstream profile blob (units, time zone, demographic settings as exposed by the Google Health API) and append it to the Health Archive as a new Profile Snapshot. Each invocation creates a new dated snapshot rather than overwriting the prior one, so historical settings drift is preserved.\n\nA Profile Snapshot is not a Data Point. It is metadata about the consenting user's account and the unit conventions in force at the time of fetch.",
		Flags:       withCommon(),
		CommonFlags: commonFlagNames(),
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runProfileWithRuntime(args, common.ConfigPath, common.ArchivePath, commonOutputMode(common), stdout, stderr, runtime)
		},
	},
	{
		Name:  "settings",
		Short: "Archive a Settings Snapshot from the provider.",
		Long:  "Fetch the upstream `users.getSettings` payload and append it to the Health Archive as a new Identity Snapshot of kind `settings`. The `current_settings` Normalized View projects the latest snapshot's measurement system, timezone, and stride-length type into columns for `query` and `export`.\n\n`settings` is read-only against the provider and writes the raw response to the archive; the JSON shape stays the source of truth, so new fields can be projected into the view without a re-sync.",
		// settings does no prompting and never blocks on browser input,
		// so --no-input is intentionally omitted from both Flags and
		// CommonFlags (issue #171): the help block, the schema, and
		// the runtime spec agree.
		Flags:       withCommonSubset(identitySnapshotCommonFlagNames()),
		CommonFlags: identitySnapshotCommonFlagNames(),
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runSettingsWithRuntime(args, common.ConfigPath, common.ArchivePath, commonOutputMode(common), stdout, stderr, runtime)
		},
	},
	{
		Name:  "devices",
		Short: "Archive a Paired Devices Snapshot from the provider.",
		Long:  "Fetch the upstream `users.pairedDevices.list` payload and append it to the Health Archive as a new Identity Snapshot of kind `paired-devices`. The `paired_devices` Normalized View explodes the latest snapshot via `json_each`, returning one row per device with `device_type`, `model`, `manufacturer`, `battery_percentage`, `last_sync_time`, and `features`.\n\nThis is the LLM's path to questions like \"which Pixel Watch synced last?\" or \"what's my Fitbit battery?\" — every projection is read-only against the raw snapshot, so new fields can be added without re-syncing.",
		// devices does no prompting and never blocks on browser input,
		// so --no-input is intentionally omitted from both Flags and
		// CommonFlags (issue #171): the help block, the schema, and
		// the runtime spec agree.
		Flags:       withCommonSubset(identitySnapshotCommonFlagNames()),
		CommonFlags: identitySnapshotCommonFlagNames(),
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runDevicesWithRuntime(args, common.ConfigPath, common.ArchivePath, commonOutputMode(common), stdout, stderr, runtime)
		},
	},
	{
		Name:  "irn-profile",
		Short: "Archive an IRN Profile Snapshot from the provider.",
		Long:  "Fetch the upstream `users.getIrnProfile` payload (onboarding state, enrollment state for Google's irregular-rhythm-notification feature) and append it to the Health Archive as a new Identity Snapshot of kind `irn-profile`. The `current_irn_profile` Normalized View projects the latest snapshot as columns.\n\nRequires the `irn.readonly` OAuth scope — run `gohealthcli connect --add-scopes irn` once to grant it. If the scope is not granted, `irn-profile` exits with a clear reconnect instruction and does **not** trigger the browser flow.",
		// irn-profile does no prompting and never blocks on browser
		// input, so --no-input is intentionally omitted from both
		// Flags and CommonFlags (issue #171): the help block, the
		// schema, and the runtime spec agree.
		Flags:       withCommonSubset(identitySnapshotCommonFlagNames()),
		CommonFlags: identitySnapshotCommonFlagNames(),
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runIRNProfileWithRuntime(args, common.ConfigPath, common.ArchivePath, commonOutputMode(common), stdout, stderr, runtime)
		},
	},
	{
		Name:  "sync",
		Short: "Archive Google Health Data Points and supported Rollups.",
		Long:  "Pull raw Data Points for the requested Data Types within an inclusive `--from` / exclusive `--to` window and append them to the Health Archive. Sync is the primary write path; everything else in the binary either reads from the archive or refreshes metadata.\n\n`--types` accepts a comma-separated list (for example `steps,heart-rate,sleep`); multi-type invocations fan out into one Sync Run per Data Type, each with its own outcome and Sync Cursor. When neither `--types` nor `--all` is set, `sync` falls back to a single-type run against `steps`. `--all` is shorthand for every default Data Type in the catalog. Per-type failures stay isolated: one Data Type erroring does not stop the others. `--rollup` switches the sync from raw Data Points to upstream Rollup records: `daily` calls the `dailyRollUp` endpoint (civil-time windows), `hourly` / `weekly` / `window=<duration>` call the windowed `rollUp` endpoint (RFC3339 windows) with a 1h / 7d / parsed-duration window size respectively. Unsupported combinations error with the Data Type's actual `SupportedEndpoints` quoted in the message. `--source-family wearable` restricts the result set to Data Points whose Data Source family is a watch or tracker.\n\n`--from` and `--to` accept both civil dates (`YYYY-MM-DD`, interpreted as start-of-UTC-day) and RFC3339 timestamps. The emitted shape is per rollup kind:\n\n- `daily`: emits civil dates (`YYYY-MM-DD`). RFC3339 inputs are projected to their UTC calendar day so the upstream `dailyRollUp` body carries the catalog-required civil interval.\n- `hourly` / `weekly` / `window=<duration>`: emits RFC3339 so the windowed `rollUp` body carries the upstream-required RFC3339 range.\n\nShape-rejection messages name both supported forms per rollup kind so operators no longer see an opaque upstream HTTP 400 for civil-on-hourly or similar.\n\n`--from` is optional once an initial backfill has succeeded — subsequent runs read the durable Sync Cursor for the same `(connection_id, data_type, source_family_filter, rollup_kind)` key and resume from it. Each rollup kind (`daily` / `hourly` / `weekly` / `window=<duration>`) carries its own cursor, so syncing weekly aggregates does not disturb the hourly cursor for the same Data Type. The cursor advances only when a Sync Run finishes with `sync_completed`, so failed or cancelled runs re-read the same window on the next attempt (ADR-0008). The terminal Sync Run status and the cursor advance are written in one SQLite transaction, so a crash between them cannot leave the audit trail and the cursor disagreeing.\n\nA Sync Run row is recorded for every invocation that reaches upstream — succeeded, failed, or cancelled — so the archive carries an audit trail of attempts as well as records. Every `--json` envelope carries a non-empty `status` from the enum `sync_completed | sync_failed | sync_canceled`; the empty string is structurally impossible because every code path emits a non-empty status.\n\nPreflight failures exit before contacting the provider and do NOT write a `sync_runs` audit row. The full list of no-audit-row rejections is:\n\n- Unparseable `--from` or `--to` (range parse).\n- Inverted range (`--from > --to`).\n- Zero-width range (`--from == --to`).\n- Unsupported `--rollup` kind (parse failure).\n- `--rollup <kind>` requested for a Data Type whose catalog entry does not support that kind (e.g. `--rollup hourly --types daily-resting-heart-rate`).\n- Unsupported Data Type (not syncable yet).\n- Source-family vs Data Type mismatch.\n- `--rollup` combined with `--source-family` (mutually exclusive).\n- No Connection on file (connection lookup failure).\n- `--all` combined with `--types` (mutually exclusive).\n- Duplicate entries in `--types`.\n- `--all` expanding to zero supported Data Types.\n- SIGINT received before any Data Type has started its audit row (no run is in flight to mark).\n\nSIGINT (Ctrl-C) during a fan-out marks the in-flight Sync Run `sync_canceled`, leaves its Sync Cursor un-advanced, and stops cleanly; prior Data Types remain `sync_completed`.\n\nTerminal writes are resilient to SQLite contention: on `SQLITE_BUSY`, the terminal write retries with bounded exponential backoff plus full jitter. If the retry budget is exhausted, the run surfaces as `sync_failed` with a contention-aware message and a separate short-transaction recovery write drives the row to a terminal state under the same retry budget so a `sync_running` row never lingers. `sync_canceled` outcomes are preserved through the recovery path — they are never reclassified as `sync_failed`.",
		Flags: withCommon(
			flagSpec{Name: "types", Type: "string", Default: "", Usage: "comma-separated Data Types; defaults to \"steps\" when neither --types nor --all is set"},
			flagSpec{Name: "all", Type: "bool", Default: "false", Usage: "sync every default Data Type"},
			flagSpec{Name: "from", Type: "string", Default: "", Usage: "inclusive sync range start; optional once a Sync Cursor exists"},
			flagSpec{Name: "to", Type: "string", Default: "", Usage: "exclusive sync range end"},
			flagSpec{Name: "rollup", Type: "string", Default: "", Usage: "rollup kind to sync; supported: daily | hourly | weekly | window=<duration>"},
			flagSpec{Name: "source-family", Type: "string", Default: "", Usage: "source family filter; supported: wearable"},
		),
		CommonFlags: commonFlagNames(),
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runSyncWithRuntime(args, common.ConfigPath, common.ArchivePath, commonOutputMode(common), stdout, stderr, runtime)
		},
	},
	{
		Name:        "status",
		Short:       "Summarise archive counts and newest synced timestamps.",
		Long:        "Print a per-Data-Type summary of the Health Archive: how many Data Points are stored, the newest synced timestamp, and the most recent Sync Run status. Useful as a quick health check before or after a long sync.\n\nAlso reports identity-metadata freshness: a `paired_device_count` line (when a `paired-devices` snapshot is archived) and an `identity_snapshot.<kind>.fetched_at` line per Identity Snapshot kind that has at least one row (`profile`, `settings`, `paired-devices`, `irn-profile`). In `--json` these surface under an `identity_snapshots_freshness` block — omitted entirely when no snapshots exist.\n\nAlso reports Tier 2 coverage: `electrocardiogram_event_count` and `irregular_rhythm_notification_count` (plain) appear only when the corresponding scope has been granted via `connect --add-scopes ecg,irn`. In `--json` these surface under a `tier_2` block alongside `electrocardiogram_scope_granted` / `irregular_rhythm_notification_scope_granted` flags, both counts defaulting to 0 when the scope is not granted.\n\n`status` does no provider I/O — it reads only the local Health Archive.",
		Flags:       withCommon(),
		CommonFlags: commonFlagNames(),
		// status's underlying signature carries an explicit ArchivePathExplicit
		// flag (so its own --db can win over the config-recorded path); the
		// adapter sources it from CommonFlagValues, which the dispatch path
		// populates from the FlagSet Visit pass.
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, _ runtimeAdapters) int {
			return runStatus(args, common.ConfigPath, common.ArchivePath, common.ArchivePathExplicit, commonOutputMode(common), stdout, stderr)
		},
	},
	{
		Name:           "query",
		Short:          "Run guarded read-only SQL over the Health Archive.",
		Long:           "Execute a single SQL statement against the Health Archive. The binary refuses anything that would write or alter the archive — `query` is for inspection, not maintenance.\n\nFlags must appear **before** the SQL argument because Go's `flag` parser stops at the first positional argument. To explore the schema, query the `sqlite_master` table or run `gohealthcli export` for the canonical normalised datasets.",
		PositionalArgs: "<sql>",
		Flags:          withCommon(),
		CommonFlags:    commonFlagNames(),
		// query, like status, reads ArchivePathExplicit so a --db passed
		// on the global side (before the subcommand) still wins. query
		// hits the archive read-only, so the runtime adapter bundle is
		// not needed.
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, _ runtimeAdapters) int {
			return runQuery(args, common.ConfigPath, common.ArchivePath, common.ArchivePathExplicit, commonOutputMode(common), stdout, stderr)
		},
	},
	{
		Name:           "export",
		Short:          "Write a normalised dataset to CSV or JSONL.",
		Long:           "Render one of the curated normalised datasets (daily-steps, heart-rate-samples, resting-heart-rate-by-day, sleep-sessions, exercise-sessions, weight-samples) from the Health Archive. Exports are read-only; nothing in the archive is mutated.\n\nExactly one of `--output PATH` or `--stdout` must be supplied — the explicit destination prevents an accidental terminal dump of a long export.\n\n`--json` is a Common Flag Set synonym for `--format jsonl`; `--plain` is a synonym for `--format csv`. Passing a synonym alongside a contradictory `--format` value (`--json --format csv`, `--plain --format jsonl`) fails with a `--<synonym> conflicts with --format <value>` error. `--plain --json` together fails with the documented mutual-exclusion error from the Common Flag Set seam.",
		PositionalArgs: "<dataset>",
		Flags: withCommonOverrides(
			map[string]string{
				"json":     "synonym for --format jsonl",
				"plain":    "synonym for --format csv",
				"no-input": "accepted for uniformity; export does no prompting",
			},
			flagSpec{Name: "format", Type: "string", Default: "csv", Usage: "export format: csv or jsonl (synonyms: --json → jsonl, --plain → csv)"},
			flagSpec{Name: "output", Type: "string", Default: "", Usage: "write export to path"},
			flagSpec{Name: "stdout", Type: "bool", Default: "false", Usage: "write export data to stdout"},
		),
		CommonFlags: commonFlagNames(),
		// export reads ArchivePathExplicit identically to status / query; it
		// has its own --format / --output flag surface, plus the Common
		// Flag Set synonym mappings handled inside runExport. The runtime
		// adapter bundle is unused (export is read-only against the local
		// archive).
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, _ runtimeAdapters) int {
			return runExport(args, common.ConfigPath, common.ArchivePath, common.ArchivePathExplicit, stdout, stderr)
		},
	},
	{
		Name:           "raw",
		Short:          "Print raw provider JSON for endpoint exploration.",
		Long:           "Fetch a single upstream Google Health API response and print the raw body to stdout. Useful for endpoint exploration without committing the response to the Health Archive.\n\nFirst positional argument is `endpoint <name>` (for example `endpoint getIdentity`) or `data-type <data-type>` (for example `data-type steps --from 2026-01-01 --to 2026-01-02`). `--from` and `--to` constrain time ranges where the endpoint supports them; `--page-size` and `--page-token` drive pagination.\n\n`raw` is provider-shaped on purpose — the JSON you see is what the provider returns, not the normalised shape the archive stores.",
		PositionalArgs: "<target> [<args>...]",
		Flags: []flagSpec{
			{Name: "config", Type: "string", Default: "", Usage: "config file path"},
			{Name: "db", Type: "string", Default: "", Usage: "SQLite Health Archive path"},
			{Name: "from", Type: "string", Default: "", Usage: "inclusive time-range start (where supported by the endpoint)"},
			{Name: "to", Type: "string", Default: "", Usage: "exclusive time-range end (where supported by the endpoint)"},
			{Name: "page-size", Type: "int", Default: "", Usage: "pagination page size (positive integer; where supported by the endpoint)"},
			{Name: "page-token", Type: "string", Default: "", Usage: "pagination page token from a prior response"},
		},
		// raw's success output is the provider's raw bytes on stdout, so
		// --plain / --json / --no-input would have no useful effect. Its
		// CommonFlagSpec at the runtime layer (see runRawWithRuntime in
		// main.go) declares only {config, db}; CommonFlags here mirrors
		// that contract so the schema reflects the divergence honestly.
		CommonFlags: []string{"config", "db"},
		// raw owns its own --from / --to / --page-size / --page-token flag
		// surface and writes the provider's raw bytes. runRawWithRuntime's
		// signature already declares the outputMode arg unused (`_`), but
		// the adapter still passes commonOutputMode(common) so this entry
		// has the same call-site shape as every other registry adapter —
		// future code that promotes the mode out of unused-arg status will
		// not have to special-case `raw`.
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runRawWithRuntime(args, common.ConfigPath, common.ArchivePath, commonOutputMode(common), stdout, stderr, runtime)
		},
	},
	{
		Name:  "describe-schema",
		Short: "Self-describe the Health Archive for LLM consumption.",
		Long:  "Emit the archive's schema in one of two modes.\n\n`--sql` dumps live DDL straight from `sqlite_master`, excluding internal `sqlite_*` objects. Use this when you want the actual truth of what tables and views exist right now.\n\nThe JSON catalog is the success-mode default: it emits a curated document combining the Normalized Views Registry's per-view metadata (name, migration version, declared columns), the live table+column shape from `pragma_table_info`, the merged hand-curated narrative file, and a stable wire-shape version field. Downstream tools (a Claude skill, an MCP server, a dashboard) read the JSON catalog as the contract. The Common Flag Set `--json` flag is accepted for the uniform-flag contract but does not change behaviour — the catalog is emitted unless `--sql` overrides.\n\n`--plain` is accepted as a no-op — the schema catalog has no key/value plain shape, so `describe-schema --plain` emits the JSON catalog and surfaces a `// note: --plain is a no-op …` comment line on stderr; stdout stays valid JSON so users redirecting it to a file are unaffected. `--plain --json` together fails with the documented mutual-exclusion error.\n\nA drift test in CI fails when a public view exists in `sqlite_master` without a matching catalog entry — the JSON shape and the live schema cannot diverge silently.",
		Flags: withCommonOverrides(
			map[string]string{
				"json":     "accepted for uniformity; the JSON catalog is the success-mode default",
				"plain":    "no-op (schema catalog has no plain shape); emits JSON catalog + stderr note",
				"no-input": "accepted for uniformity; describe-schema does no prompting",
			},
			flagSpec{Name: "sql", Type: "bool", Default: "false", Usage: "dump live DDL from sqlite_master (excludes internal sqlite_* objects)"},
		),
		CommonFlags: commonFlagNames(),
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runDescribeSchemaWithRuntime(args, common.ConfigPath, common.ArchivePath, commonOutputMode(common), stdout, stderr, runtime)
		},
	},
	{
		Name:   "schema",
		Short:  "Emit the command registry as JSON (hidden — used by the Project Site build).",
		Long:   "Emit the binary's command registry as a stable JSON document. The Project Site's command-reference pages are generated from this output, so the JSON shape is part of the published contract.\n\nThe subcommand is hidden from `gohealthcli --help` because it is a build-time tool, not an end-user surface. Pass `--json` (the default and only mode) to receive the document on stdout.",
		Hidden: true,
		Flags: []flagSpec{
			{Name: "json", Type: "bool", Default: "true", Usage: "emit the registry as JSON (default and currently the only output mode)"},
		},
		// schema's Run adapter is wired below in init(): emitting the
		// registry-as-JSON means schema's Run closure has to reference
		// the very `commands` slice it lives in, which trips Go's
		// var-init cycle checker even though closures defer the actual
		// call. Wiring after the slice is fully initialised sidesteps
		// that without forcing the schema command out of the registry.
	},
}

// init wires the Run adapter for the `schema` entry after `commands` is
// fully initialised, sidestepping the var-init cycle that would otherwise
// fire when the closure references the very slice it lives in. The lookup
// walks the registry once at startup and is keyed on the canonical name so
// a reorder of `commands` does not break this binding.
func init() {
	for i := range commands {
		if commands[i].Name != "schema" {
			continue
		}
		commands[i].Run = func(args []string, _ CommonFlagValues, stdout, stderr io.Writer, _ runtimeAdapters) int {
			return runSchemaWithRegistry(args, commands, stdout, stderr)
		}
		return
	}
}

// commandRegistry is the typed view over the package-level commands slice that
// owns lookup behaviours such as `Suggest`. Defining it as a named slice type
// (rather than a wrapping struct) lets call sites pass the existing global in
// directly — `commandRegistry(commands).Suggest(typo)` — without copying
// entries or maintaining a parallel data structure.
type commandRegistry []commandDef

// suggestMaxDistance is the Levenshtein cutoff for "did you mean" suggestions.
// PRD #143 fixes this at 2: enough to catch a single transposition or two
// character edits ("stauts" → "status"), tight enough to avoid noisy matches
// when the user typed something genuinely unrelated ("xyz" → nothing).
const suggestMaxDistance = 2

// Suggest returns at most two non-hidden command names whose Levenshtein
// distance from `typo` is ≤ suggestMaxDistance, ordered by ascending distance
// and then by registry order for deterministic output. Hidden commands (the
// build-time `schema` entry) are filtered so they never surface to end users.
// An empty result means the unknown-command path should print no `Did you mean`
// line; the caller still prints the canonical help hint.
func (r commandRegistry) Suggest(typo string) []string {
	type candidate struct {
		name     string
		distance int
		order    int
	}
	var candidates []candidate
	for i, cmd := range r {
		if cmd.Hidden {
			continue
		}
		d := levenshteinDistance(typo, cmd.Name)
		if d <= suggestMaxDistance {
			candidates = append(candidates, candidate{name: cmd.Name, distance: d, order: i})
		}
	}
	if len(candidates) == 0 {
		// Return an empty slice rather than nil so callers (and the AC's
		// direct unit test) see a consistent shape regardless of input:
		// `json.Marshal(nil)` → `null` whereas `json.Marshal([]string{})`
		// → `[]`, and any downstream consumer that range-iterates is
		// untouched either way.
		return []string{}
	}
	// Sort by (distance asc, registry-order asc). The candidate slice is
	// bounded by the number of non-hidden commands (~14 today), so an
	// in-place insertion sort is dependency-free, easy to audit, and
	// trivially correct for that working-set size.
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0; j-- {
			a, b := candidates[j-1], candidates[j]
			if a.distance < b.distance || (a.distance == b.distance && a.order < b.order) {
				break
			}
			candidates[j-1], candidates[j] = b, a
		}
	}
	top := candidates
	if len(top) > 2 {
		top = top[:2]
	}
	out := make([]string, 0, len(top))
	for _, c := range top {
		out = append(out, c.name)
	}
	return out
}

// levenshteinDistance returns the classic edit distance between a and b
// using the two-row dynamic-programming variant (O(len(a)*len(b)) time,
// O(min(len(a),len(b))) space). Sub/insert/delete each cost 1; this is the
// metric the PRD specifies, so the helper stays local rather than pulling in
// a third-party module for a 25-line algorithm.
func levenshteinDistance(a, b string) int {
	if a == b {
		return 0
	}
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	// Keep the smaller dimension as the row width.
	if len(ar) < len(br) {
		ar, br = br, ar
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
