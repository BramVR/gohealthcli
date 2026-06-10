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
//     arguments (e.g. "<SQL>"); omitted
//     entirely when empty
//   - flags           (array of flagSpec)   — flag specifications
//   - common_flags    (array of strings, optional) — subset of the five shared
//     flag names that the subcommand
//     accepts via the runtime
//     CommonFlagSet module (issue #166).
//     Omitted entirely when empty so
//     the wire shape stays additive.
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

// commandIndex keys the registry by Name so dispatch is a map lookup
// (#75) rather than a linear scan. Values are indices into `commands`
// — not copied commandDef values — because the `schema` entry's Run
// adapter is wired by init() after the slice literal is initialised;
// storing indices keeps the index coherent with that late binding (and
// with tests that temporarily swap an entry's Run) regardless of init
// order. Built once at startup by the init() below.
var commandIndex = make(map[string]int, len(commands))

// init builds commandIndex from the registry. A duplicate Name panics:
// two entries sharing a name would make one unreachable from dispatch,
// which is a registry-build programmer error, not a runtime condition —
// the same convention withCommonSubset / withCommonOverrides follow.
// This panic is the actual enforcement: it fires during package init,
// before any test runs. TestCommandNamesAreUnique documents and pins
// the same invariant in the suite.
func init() {
	for i, cmd := range commands {
		if _, dup := commandIndex[cmd.Name]; dup {
			panic("duplicate command name in registry: " + cmd.Name)
		}
		commandIndex[cmd.Name] = i
	}
}

// lookupCommand returns the registry entry whose Name matches the given
// subcommand. It deliberately INCLUDES hidden entries: the `help <cmd>` verb
// (PRD #143 slice 2) must surface the prose for hidden build-time commands
// like `schema` when asked explicitly, even though they are filtered from the
// top-level `--help` listing.
func lookupCommand(name string) (commandDef, bool) {
	i, ok := commandIndex[name]
	if !ok {
		return commandDef{}, false
	}
	return commands[i], true
}

// withCommon projects commonFlagsSpec (the issue #76 single source of
// truth for the five shared flags, defined in common_flags.go) plus the
// subcommand-specific extras into a registry entry's Flags slice. The
// runtime side of the same spec is RegisterCommon, so the schema and the
// FlagSet a subcommand actually parses cannot disagree.
func withCommon(extra ...flagSpec) []flagSpec {
	out := make([]flagSpec, 0, len(commonFlagsSpec)+len(extra))
	out = append(out, commonFlagsSpec...)
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

// rawCommonFlagNames returns the subset of common flag names `raw`
// accepts. raw's success output is the provider's raw bytes on stdout,
// so --json / --plain / --no-input would have no useful effect; the
// Common Flag Set's pre-Parse scan rejects them with the targeted
// "--<flag> is not supported by raw" wording instead of letting them
// silently lose values. The registry entry's Flags / CommonFlags and
// runRawWithRuntime's CommonFlagSpec all read this one function, so the
// schema and the runtime contract cannot disagree. Returned fresh each
// call to mirror commonFlagNames so per-entry slices stay independent.
func rawCommonFlagNames() []string {
	return []string{"config", "db"}
}

// withCommonSubset is the per-subcommand variant of withCommon that
// projects only the named shared flags (in the canonical commonFlagsSpec
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
	known := make(map[string]bool, len(commonFlagsSpec))
	for _, flag := range commonFlagsSpec {
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
	for _, flag := range commonFlagsSpec {
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
	out := make([]flagSpec, 0, len(commonFlagsSpec)+len(extra))
	for _, flag := range commonFlagsSpec {
		if override, ok := overrides[flag.Name]; ok {
			flag.Usage = override
		}
		out = append(out, flag)
	}
	for name := range overrides {
		found := false
		for _, flag := range commonFlagsSpec {
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
// commonFlagsSpec order. Registry entries for subcommands whose runtime flag
// setup goes through the CommonFlagSet module (issue #166) carry this
// slice as their `common_flags` schema field, so downstream tooling can
// see at a glance which subset of shared flags each subcommand accepts.
func commonFlagNames() []string {
	names := make([]string, 0, len(commonFlagsSpec))
	for _, f := range commonFlagsSpec {
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
		Long:  "Open the system browser, run the installed-app OAuth flow against the OAuth client supplied at `init`, and store the resulting tokens in the OS-native Credential Store (Keychain on macOS, Credential Manager on Windows, Secret Service on Linux).\n\nA Health Archive holds exactly one Connection. Running `connect` against an archive that already has a Connection refreshes the token material in place rather than adding a second identity.\n\n`--add-scopes` extends an existing grant with optional scope keywords (`irn`, `ecg`, `nutrition`, `tcx`, `settings`) without re-running setup; Google's `include_granted_scopes=true` makes the resulting token cover the union of prior + new scopes. Use `connect --add-scopes irn` to unlock `gohealthcli irn-profile` and Tier 2 ECG / IRN Data Types; use `connect --add-scopes nutrition` to unlock hydration-log; use `connect --add-scopes tcx` to unlock TCX route archival on exercise sync (grants `googlehealth.location.readonly`, required on top of `activity_and_fitness.readonly` for Google's `exportExerciseTcx` endpoint); use `connect --add-scopes settings` to unlock `gohealthcli settings` and `gohealthcli devices` (grants `googlehealth.settings.readonly`, which Google requires for `users.getSettings` and `users.pairedDevices.list`).\n\n`--no-input` makes the command fail with a non-zero exit code if the browser flow would block (useful in CI smoke tests after the tokens are already provisioned).",
		Flags: withCommon(
			// Usage is rendered from connectAddScopeKeywords (same source
			// as the runtime flag registration and the unknown-keyword
			// error) so the schema --json contract and the generated
			// docs/commands/connect.md page cannot drift from the
			// accepted keyword set (#148).
			flagSpec{Name: "add-scopes", Type: "string", Default: "", Usage: connectAddScopesUsage()},
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
		Long:  "Fetch the upstream `users.getSettings` payload and append it to the Health Archive as a new Identity Snapshot of kind `settings`. The `current_settings` Normalized View projects the latest snapshot's measurement system, timezone, and stride-length type into columns for `query` and `export`.\n\n`settings` is read-only against the provider and writes the raw response to the archive; the JSON shape stays the source of truth, so new fields can be projected into the view without a re-sync.\n\nRequires the `settings.readonly` OAuth scope (PRD #142 #176 confirmed empirically — `profile.readonly` alone returns HTTP 403). If the scope is missing, `settings` exits with status `settings_scope_missing` and a remediation hint; run `gohealthcli connect --add-scopes settings` once to grant it. No second base-set browser sign-in is needed.",
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
		Long:  "Fetch the upstream `users.pairedDevices.list` payload and append it to the Health Archive as a new Identity Snapshot of kind `paired-devices`. The `paired_devices` Normalized View explodes the latest snapshot via `json_each`, returning one row per device with `device_type`, `model`, `manufacturer`, `battery_percentage`, `last_sync_time`, and `features`.\n\nThis is the LLM's path to questions like \"which Pixel Watch synced last?\" or \"what's my Fitbit battery?\" — every projection is read-only against the raw snapshot, so new fields can be added without re-syncing.\n\nRequires the `settings.readonly` OAuth scope (PRD #142 #176 confirmed empirically — `profile.readonly` alone returns HTTP 403). If the scope is missing, `devices` exits with status `devices_scope_missing` and a remediation hint; run `gohealthcli connect --add-scopes settings` once to grant it. No second base-set browser sign-in is needed.",
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
		Long:  "Pull raw Data Points for the requested Data Types within an inclusive `--from` / exclusive `--to` window and append them to the Health Archive. Sync is the primary write path; everything else in the binary either reads from the archive or refreshes metadata.\n\n`--types` accepts a comma-separated list (for example `steps,heart-rate,sleep`); multi-type invocations fan out into one Sync Run per Data Type, each with its own outcome and Sync Cursor. When neither `--types` nor `--all` is set, `sync` falls back to a single-type run against `steps`. `--all` is shorthand for every default Data Type in the catalog. Per-type failures stay isolated: one Data Type erroring does not stop the others. `--rollup` switches the sync from raw Data Points to upstream Rollup records: `daily` calls the `dailyRollUp` endpoint (civil-time windows), `hourly` / `weekly` / `window=<duration>` call the windowed `rollUp` endpoint (RFC3339 windows) with a 1h / 7d / parsed-duration window size respectively. Unsupported combinations error with the Data Type's actual `SupportedEndpoints` quoted in the message. `--source-family wearable` restricts the result set to Data Points whose Data Source family is a watch or tracker.\n\n`--from` and `--to` accept both civil dates (`YYYY-MM-DD`, interpreted as start-of-UTC-day) and RFC3339 timestamps. The emitted shape is per rollup kind:\n\n- `daily`: emits civil dates (`YYYY-MM-DD`). RFC3339 inputs are projected to their UTC calendar day so the upstream `dailyRollUp` body carries the catalog-required civil interval.\n- `hourly` / `weekly` / `window=<duration>`: emits RFC3339 so the windowed `rollUp` body carries the upstream-required RFC3339 range.\n\nShape-rejection messages name both supported forms per rollup kind so operators no longer see an opaque upstream HTTP 400 for civil-on-hourly or similar.\n\n`--from` is optional once an initial backfill has succeeded — subsequent runs read the durable Sync Cursor for the same `(connection_id, data_type, source_family_filter, rollup_kind)` key and resume from it. Each rollup kind (`daily` / `hourly` / `weekly` / `window=<duration>`) carries its own cursor, so syncing weekly aggregates does not disturb the hourly cursor for the same Data Type. The cursor advances only when a Sync Run finishes with `sync_completed`, so failed or cancelled runs re-read the same window on the next attempt (ADR-0008). The terminal Sync Run status and the cursor advance are written in one SQLite transaction, so a crash between them cannot leave the audit trail and the cursor disagreeing.\n\nA Sync Run row is recorded for every invocation that reaches upstream — succeeded, failed, or cancelled — so the archive carries an audit trail of attempts as well as records. Every `--json` envelope carries a non-empty `status` from the enum `sync_completed | sync_failed | sync_canceled`; the empty string is structurally impossible because every code path emits a non-empty status.\n\nPreflight failures exit before contacting the provider and do NOT write a `sync_runs` audit row. The full list of no-audit-row rejections is:\n\n- Unparseable `--from` or `--to` (range parse).\n- Inverted range (`--from > --to`).\n- Zero-width range (`--from == --to`).\n- Unsupported `--rollup` kind (parse failure).\n- `--rollup <kind>` requested for a Data Type whose catalog entry does not support that kind (e.g. `--rollup hourly --types daily-resting-heart-rate`).\n- Unsupported Data Type (not syncable yet).\n- Source-family vs Data Type mismatch.\n- `--rollup` combined with `--source-family` (mutually exclusive).\n- No Connection on file (connection lookup failure).\n- `--all` combined with `--types` (mutually exclusive).\n- Duplicate entries in `--types`.\n- `--all` expanding to zero supported Data Types.\n- SIGINT received before any Data Type has started its audit row (no run is in flight to mark).\n\nSIGINT (Ctrl-C) during a fan-out marks the in-flight Sync Run `sync_canceled`, leaves its Sync Cursor un-advanced, and stops cleanly; prior Data Types remain `sync_completed`.\n\nTerminal writes are resilient to SQLite contention: on `SQLITE_BUSY`, the terminal write retries with bounded exponential backoff plus full jitter. If the retry budget is exhausted, the run surfaces as `sync_failed` with a contention-aware message and a separate short-transaction recovery write drives the row to a terminal state under the same retry budget so a `sync_running` row never lingers. `sync_canceled` outcomes are preserved through the recovery path — they are never reclassified as `sync_failed`.\n\nLive progress (#236): before every page fetch the Sync Run heartbeats — the counts archived so far plus a `last_progress_at` timestamp land on the `sync_runs` row as a best-effort autocommit write — so a concurrent reader can watch progress from another terminal while the run is in flight, and a slow first page (large backfill, 429 retry backoff) still shows a live heartbeat from second zero. Heartbeats are advisory; the finalize transaction's terminal counts stay authoritative, and a heartbeat write failure never fails the sync.\n\n`sync --status` is that concurrent reader, packaged: it lists recent Sync Runs from the local archive — one row per run with id, Data Types, status, counts, duration, heartbeat age, and a truncated error summary — and performs no provider I/O. Finished runs are listed when they finished inside `--window` (Go duration, default `15m`, max `24h`); `sync_running` rows are window-exempt, so a long in-flight run never ages out of the default view. `--status` cannot be combined with `--types`, `--all`, `--from`, `--to`, `--rollup`, or `--source-family`, and `--window` requires `--status`. The shared `--json` / `--plain` flags shape the output like every other read command.\n\nAbandoned-run fencing: on entry to `sync`, `sync --status`, and `status`, any `sync_running` row whose heartbeat (or `started_at`, for rows that died before their first page) is older than 5 minutes is flipped to `sync_failed` with `error_summary` `abandoned (no heartbeat for 5m)` and `finished_at` set — so orphans from killed processes stop reading as alive without manual SQL. The fence is idempotent and never touches the Sync Cursor (ADR-0008: only a completed finalize advances it). Because it keys on heartbeat staleness rather than wall-clock age, a multi-hour backfill with a fresh heartbeat is never mis-flagged; and if a fenced process turns out to be alive after all, its eventual finalize overwrites the fence so the row converges to its true terminal status.\n\nHow long does a sync take? A cursor-resumed incremental sync finishes in seconds — a steps delta covering ~7 hours archived 97 Data Points in 7s. An explicit backfill window costs time in proportion to how many Data Points it covers: sustained throughput on large completed runs measures roughly 2,000–5,000 Data Points per minute (plan with ~2,000/min), so the Data Type's density per day decides the wall-clock. Densities measured 2026-06-10 from a real archive backed by a Pixel Watch 4 (continuous heart-rate sampling), and what two weeks of data costs at the conservative rate: heart-rate ~27,500 points/day, so two weeks is ~385,000 points and 1.5–3 hours of syncing; time-in-heart-rate-zone ~960/day, ~13,400 points, ~5 minutes; active-energy-burned ~630/day, ~8,800 points, ~4 minutes; oxygen-saturation ~480/day, ~6,700 points, ~3 minutes; steps ~260/day, ~3,600 points, ~2 minutes; sleep and the daily-* types are a point or so per day and finish in seconds. Density is account-specific — a phone-only account with no continuously-sampling wearable runs far lower across the board. Runs longer than an access token's ~1-hour lifetime survive it: a mid-run upstream 401 triggers a single token refresh and a retry of the failed request, and the refreshed token carries the rest of the run — at most one refresh per fetch, so a revoked grant still fails, and 403 is never retried because a fresh token cannot fix a missing scope. Mid-run refresh requires a Connection that supports auto-refresh (the standard `init --oauth-client-file` setup does); without it, a run that outlives its token keeps the historical behavior — it fails with `Google Health rejected stored Connection token` and leaves the Sync Cursor un-advanced — so chunk such backfills into `--from`/`--to` windows under ~100,000 points (2–3 days of continuously-sampled heart-rate). While a long run is in flight, `sync --status` from a second terminal shows the live counts and heartbeat age.",
		Flags: withCommon(
			flagSpec{Name: "types", Type: "string", Default: "", Usage: "comma-separated Data Types; defaults to \"steps\" when neither --types nor --all is set"},
			flagSpec{Name: "all", Type: "bool", Default: "false", Usage: "sync every default Data Type"},
			flagSpec{Name: "from", Type: "string", Default: "", Usage: "inclusive sync range start; optional once a Sync Cursor exists"},
			flagSpec{Name: "to", Type: "string", Default: "", Usage: "exclusive sync range end"},
			flagSpec{Name: "rollup", Type: "string", Default: "", Usage: "rollup kind to sync; supported: daily | hourly | weekly | window=<duration>"},
			flagSpec{Name: "source-family", Type: "string", Default: "", Usage: "source family filter; supported: wearable"},
			flagSpec{Name: "status", Type: "bool", Default: "false", Usage: "list recent Sync Runs from the local archive instead of syncing"},
			flagSpec{Name: "window", Type: "string", Default: "", Usage: "with --status: how far back to list finished Sync Runs (Go duration, default 15m, max 24h)"},
		),
		CommonFlags: commonFlagNames(),
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runSyncWithRuntime(args, common.ConfigPath, common.ArchivePath, commonOutputMode(common), stdout, stderr, runtime)
		},
	},
	{
		Name:        "status",
		Short:       "Summarise archive counts and newest synced timestamps.",
		Long:        "Print a per-Data-Type summary of the Health Archive: how many Data Points are stored, the newest synced timestamp, and the most recent Sync Run status. Useful as a quick health check before or after a long sync.\n\nAlso reports identity-metadata freshness: a `paired_device_count` line (when a `paired-devices` snapshot is archived) and an `identity_snapshot.<kind>.fetched_at` line per Identity Snapshot kind that has at least one row (`profile`, `settings`, `paired-devices`, `irn-profile`). In `--json` these surface under an `identity_snapshots_freshness` block — omitted entirely when no snapshots exist. `paired_device_count` is also emitted as a top-level JSON key so `--plain` and `--json` carry the same field; the nested `identity_snapshots_freshness.paired_device_count` is preserved for back-compat.\n\n`--plain` and `--json` carry the same information. The plain `known_data_types: a,b,c` line maps to a top-level `known_data_types` JSON array. Plain `data_type.<name>.*` and `identity_snapshot.<kind>.*` lines flatten the JSON `data_types[]` and `identity_snapshots_freshness` blocks, and the `latest_successful_sync_run_*` / `latest_failed_sync_run_*` lines flatten the matching JSON objects.\n\nAlso reports Tier 2 coverage: `electrocardiogram_event_count` and `irregular_rhythm_notification_count` (plain) appear only when the corresponding scope has been granted via `connect --add-scopes ecg,irn`. In `--json` these surface under a `tier_2` block alongside `electrocardiogram_scope_granted` / `irregular_rhythm_notification_scope_granted` flags, both counts defaulting to 0 when the scope is not granted.\n\n`status` does no provider I/O — it reads only the local Health Archive. On entry it fences abandoned Sync Runs: any `sync_running` row whose heartbeat is older than 5 minutes is flipped to `sync_failed` with `error_summary` `abandoned (no heartbeat for 5m)` (see `sync --status` for the full fencing rule), so the summary never reports a killed process as still running.",
		Flags:       withCommon(),
		CommonFlags: commonFlagNames(),
		// status's underlying signature carries an explicit ArchivePathExplicit
		// flag (so its own --db can win over the config-recorded path); the
		// adapter sources it from CommonFlagValues, which the dispatch path
		// populates from the FlagSet Visit pass.
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, _ runtimeAdapters) int {
			return runStatus(args, common.ConfigPath, common.ArchivePath, common.ConfigPathExplicit, common.ArchivePathExplicit, commonOutputMode(common), stdout, stderr)
		},
	},
	{
		Name:           "query",
		Short:          "Run guarded read-only SQL over the Health Archive.",
		Long:           "Execute a single SQL statement against the Health Archive. The binary refuses anything that would write or alter the archive — `query` is for inspection, not maintenance.\n\nFlags must appear **before** the SQL argument because Go's `flag` parser stops at the first positional argument. To explore the schema, query the `sqlite_master` table or run `gohealthcli export` for the canonical normalised datasets.\n\nThe default no-flag output is the `--plain` key/value shape: every cell appears on its own `row.<row>.<column>: <value>` line, with `\\n` / `\\t` / `\\r` / `\\\\` escaped so a downstream parser can split on the first `: ` and recover the value verbatim. The legacy `Row N: column=value column=value` shape — which silently broke on values containing spaces or `=` — was removed in PRD #144 slice 7; the default now produces byte-identical output to `--plain`, with no stderr warning, so scripted and LLM consumers get a parseable shape by default.\n\nIn `--json` mode, JSON-typed columns pass through as nested JSON objects so downstream consumers can read them with one parse instead of two. The recognised columns are `raw_json`, `data_source_json`, `timezone_metadata`, `token_metadata_json`, `google_identity_json`, and any column whose name ends in `_json`. Pass `--raw-text` to opt out and receive the literal stored string instead. NULL JSON-typed cells stay `null`; invalid JSON payloads fall back to the stored string so no row ever fails the query.\n\nBLOB columns in `--json` mode are wrapped in a `{\"__blob_base64__\": \"<base64>\"}` marker object so raw bytes survive the JSON path without UTF-8 replacement-character corruption. Detection covers both schema-declared BLOB columns (`sql.ColumnType.DatabaseTypeName() == \"BLOB\"`) and typeless expressions whose scan result is a byte slice (e.g. `SELECT randomblob(8)`). Decode the payload with any base64 decoder (`jq -r '.rows[0][0].__blob_base64__' | base64 -d`). The BLOB marker takes precedence over the JSON-typed allowlist, so a `raw_json` column that comes back as a BLOB is base64-encoded, never double-parsed. NULL BLOB cells stay `null`.\n\nBLOB columns in `--plain` mode are emitted as a `<blob:base64><payload>` string so the `row.N.M:` line stays parseable; without the prefix today's path emits the raw bytes and prints `\\ufffd` replacement characters wherever the bytes are not valid UTF-8.",
		PositionalArgs: "<sql>",
		Flags: withCommon(
			// --raw-text registered on the runtime FlagSet inside
			// runQuery; mirrored here so docs-commands regen + the
			// `schema --json` contract list it alongside the common
			// flags. Keep the usage string in sync with the BoolVar in
			// runQuery (PRD #144 slice 5).
			flagSpec{Name: "raw-text", Type: "bool", Default: "false", Usage: "in JSON mode, return JSON-typed columns as strings instead of nested objects"},
		),
		CommonFlags: commonFlagNames(),
		// query, like status, reads ArchivePathExplicit so a --db passed
		// on the global side (before the subcommand) still wins. query
		// hits the archive read-only, so the runtime adapter bundle is
		// not needed.
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, _ runtimeAdapters) int {
			return runQuery(args, common.ConfigPath, common.ArchivePath, common.ConfigPathExplicit, common.ArchivePathExplicit, commonOutputMode(common), stdout, stderr)
		},
	},
	{
		Name:           "export",
		Short:          "Write a normalised dataset to CSV or JSONL.",
		Long:           "Render one of the curated normalised datasets (daily-steps, heart-rate-samples, resting-heart-rate-by-day, sleep-sessions, exercise-sessions, weight-samples, and many more) from the Health Archive. Exports are read-only; nothing in the archive is mutated.\n\nRun `gohealthcli export --help` to see the full list of supported datasets, sorted alphabetically. If you pass a name that does not exist, the error message includes the closest matches (Levenshtein ≤ 3, top 3) and a pointer back to `export --help`.\n\nExactly one of `--output PATH` or `--stdout` must be supplied — the explicit destination prevents an accidental terminal dump of a long export.\n\n`--json` is a Common Flag Set synonym for `--format jsonl`; `--plain` is a synonym for `--format csv`. Passing a synonym alongside a contradictory `--format` value (`--json --format csv`, `--plain --format jsonl`) fails with a `--<synonym> conflicts with --format <value>` error. `--plain --json` together fails with the documented mutual-exclusion error from the Common Flag Set seam.",
		PositionalArgs: "<dataset>",
		Flags: withCommonOverrides(
			exportCommonFlagUsageOverrides,
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
			return runExport(args, common.ConfigPath, common.ArchivePath, common.ConfigPathExplicit, common.ArchivePathExplicit, stdout, stderr)
		},
	},
	{
		Name:           "raw",
		Short:          "Print raw provider JSON for endpoint exploration.",
		Long:           "Fetch a single upstream Google Health API response and print the raw body to stdout. Useful for endpoint exploration without committing the response to the Health Archive.\n\nFirst positional argument is `endpoint <name>` (for example `endpoint getIdentity`) or `data-type <data-type>` (for example `data-type steps --from 2026-01-01 --to 2026-01-02`). `--from` and `--to` constrain time ranges where the endpoint supports them; `--page-size` and `--page-token` drive pagination.\n\n`raw` is provider-shaped on purpose — the JSON you see is what the provider returns, not the normalised shape the archive stores.",
		PositionalArgs: "<target> [<args>...]",
		Flags: withCommonSubset(rawCommonFlagNames(),
			flagSpec{Name: "from", Type: "string", Default: "", Usage: "inclusive time-range start (where supported by the endpoint)"},
			flagSpec{Name: "to", Type: "string", Default: "", Usage: "exclusive time-range end (where supported by the endpoint)"},
			flagSpec{Name: "page-size", Type: "int", Default: "", Usage: "pagination page size (positive integer; where supported by the endpoint)"},
			flagSpec{Name: "page-token", Type: "string", Default: "", Usage: "pagination page token from a prior response"},
		),
		// raw's success output is the provider's raw bytes on stdout, so
		// --plain / --json / --no-input would have no useful effect. Its
		// CommonFlagSpec at the runtime layer (see runRawWithRuntime in
		// main.go) declares the same rawCommonFlagNames() subset;
		// CommonFlags here mirrors that contract so the schema reflects
		// the divergence honestly.
		CommonFlags: rawCommonFlagNames(),
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
		Long:  "Emit the archive's schema in one of two modes.\n\n`--sql` dumps live DDL straight from `sqlite_master`, excluding internal `sqlite_*` objects. Use this when you want the actual truth of what tables and views exist right now.\n\nThe JSON catalog is the success-mode default: it emits a curated document combining the Normalized Views Registry's per-view metadata (name, migration version, declared columns), the live table+column shape from `pragma_table_info`, the merged hand-curated narrative file, and a stable wire-shape version field. Downstream tools (a Claude skill, an MCP server, a dashboard) read the JSON catalog as the contract. The Common Flag Set `--json` flag is accepted for the uniform-flag contract but does not change behaviour — the catalog is emitted unless `--sql` overrides.\n\n`--plain` is accepted as a no-op — the schema catalog has no key/value plain shape, so `describe-schema --plain` emits the JSON catalog and surfaces a `// note: --plain is a no-op …` comment line on stderr; stdout stays valid JSON so users redirecting it to a file are unaffected. `--plain --json` together fails with the documented mutual-exclusion error.\n\n`--db <path>` is honoured on its own — passing a Health Archive path without `--config` opens that archive directly, matching the other read commands (PRD #144 slice 1). When `--config` is left at its default and only `--db` is explicit, `--db` wins without an agreement check; when both `--config` and `--db` are explicit and disagree, the error names `--db` and `--config` rather than the internal `archive_path` field.\n\nA drift test in CI fails when a public view exists in `sqlite_master` without a matching catalog entry — the JSON shape and the live schema cannot diverge silently.\n\n### Normalized View column types\n\nSQLite views don't carry declared column types. `pragma_table_info` reports the type of each view column from the underlying expression's affinity, which for any non-trivial JSON projection comes back as either an empty string or the literal `BLOB`. The JSON catalog is read as a contract by LLM consumers, so a column like `daily_steps.step_count` (an INTEGER projection over `data_points`) being reported as `BLOB` actively poisons agent reasoning.\n\nThe catalog rewrites those misleading values to the literal `\"unknown\"`. Every entry in `views[*].columns_detailed[*].type` is therefore either a real SQL type (`TEXT`, `INTEGER`, `REAL`, `NUMERIC`, …) or the literal `\"unknown\"` — never `BLOB`, never empty. Treat `\"unknown\"` as \"the runtime type is opaque from the catalog alone — inspect a row or consult the view DDL\".\n\nTable columns (in `tables[*].columns`) are unaffected: real `BLOB` columns on real tables still report `BLOB`. The fallback is view-only.",
		Flags: withCommonOverrides(
			describeSchemaCommonFlagUsageOverrides,
			flagSpec{Name: "sql", Type: "bool", Default: "false", Usage: "dump live DDL from sqlite_master (excludes internal sqlite_* objects)"},
		),
		CommonFlags: commonFlagNames(),
		Run: func(args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
			return runDescribeSchemaWithRuntime(args, common.ConfigPath, common.ArchivePath, common.ConfigPathExplicit, common.ArchivePathExplicit, commonOutputMode(common), stdout, stderr, runtime)
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
	{
		// PRD #144 slice 4 (issue #165): hidden build-time verb that
		// rewrites the README's "Normalized export datasets" block from
		// the registry. Hidden for the same reason as `schema`: it is
		// invoked by `make docs-export-datasets`, never by an end user.
		// Listing it in the registry (rather than as a one-off main()
		// program) keeps the dispatch surface uniform — the drift test
		// can reuse runDocsExportDatasets directly, no second binary.
		Name:   "docs-export-datasets",
		Short:  "Rewrite README export-datasets block from the registry (hidden — used by `make docs-export-datasets`).",
		Long:   "Rewrite the auto-generated bullet list in `README.md` between the `<!-- export-datasets:start -->` and `<!-- export-datasets:end -->` markers from `exportDatasetCatalogSingleton.Names()`.\n\nInvoked by `make docs-export-datasets`; the drift guard in `docs_export_datasets_test.go` fails CI when the committed README does not match a fresh regeneration. Pass `--readme PATH` to point at the file to rewrite (no default — an empty path is rejected so a misconfigured target cannot silently overwrite the wrong file).",
		Hidden: true,
		Flags: []flagSpec{
			{Name: "readme", Type: "string", Default: "", Usage: "path to README.md to rewrite in place"},
		},
		// docs-export-datasets reads / writes one file and has no
		// archive or provider dependency; the runtime adapter bundle
		// and the CommonFlagValues block are ignored.
		Run: func(args []string, _ CommonFlagValues, stdout, stderr io.Writer, _ runtimeAdapters) int {
			return runDocsExportDatasets(args, stdout, stderr)
		},
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
