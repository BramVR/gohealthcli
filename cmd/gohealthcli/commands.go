package main

// commandDef describes a single gohealthcli subcommand for both documentation
// and (in a later slice) dispatch. The Project Site's command-reference pages
// are generated from the JSON encoding of this slice via `gohealthcli schema
// --json` — keep field names stable, because they are part of the contract
// downstream tooling reads.
//
// Fields on the published JSON contract:
//   - name           (string)              — the subcommand's invocation name
//   - short          (string)              — one-line description for the index
//   - long           (string)              — full prose for the per-page body
//   - hidden         (bool)                — hidden from --help and reference
//   - positional_args (string, optional)   — usage hint for trailing positional
//                                            arguments (e.g. "<SQL>"); omitted
//                                            entirely when empty
//   - flags          (array of flagSpec)   — flag specifications
type commandDef struct {
	Name           string     `json:"name"`
	Short          string     `json:"short"`
	Long           string     `json:"long"`
	Hidden         bool       `json:"hidden"`
	PositionalArgs string     `json:"positional_args,omitempty"`
	Flags          []flagSpec `json:"flags"`
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

// commands is the registry of every subcommand the binary exposes. The
// dispatch switch and the --help formatter continue to source their data
// inline for now; subsequent slices fold them onto this registry.
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
	},
	{
		Name:  "doctor",
		Short: "Validate local setup and provider reachability.",
		Long:  "Run a diagnostic check against the local gohealthcli installation: config presence, Health Archive path, Credential Store status, schema version, and connection count.\n\nWith `--online`, also refresh stored tokens and verify Google Health API reachability. The command never writes health data; it only inspects local state and (with `--online`) performs a single read-only round trip to the provider.\n\nThe output is a structured report on stdout. Use `--json` for stable machine-readable output, `--plain` for terminal-friendly key/value lines.",
		Flags: withCommon(
			flagSpec{Name: "online", Type: "bool", Default: "false", Usage: "refresh tokens and check provider reachability"},
		),
	},
	{
		Name:  "connect",
		Short: "Run the browser OAuth flow and anchor one Google Identity.",
		Long:  "Open the system browser, run the installed-app OAuth flow against the OAuth client supplied at `init`, and store the resulting tokens in the OS-native Credential Store (Keychain on macOS, Credential Manager on Windows, Secret Service on Linux).\n\nA Health Archive holds exactly one Connection. Running `connect` against an archive that already has a Connection refreshes the token material in place rather than adding a second identity.\n\n`--no-input` makes the command fail with a non-zero exit code if the browser flow would block (useful in CI smoke tests after the tokens are already provisioned).",
		Flags: withCommon(),
	},
	{
		Name:  "identity",
		Short: "Refresh the archived Google Identity metadata.",
		Long:  "Re-fetch the upstream Google Identity payload (Google Health user ID and legacy Fitbit user ID when present) and update the metadata stored alongside the Connection.\n\n`identity` does not change the OAuth tokens or move the Connection between archives — use `connect` for those. It is a low-cost, read-only operation against the provider.",
		Flags: withCommon(),
	},
	{
		Name:  "profile",
		Short: "Archive a Profile Snapshot from the provider.",
		Long:  "Fetch the upstream profile blob (units, time zone, demographic settings as exposed by the Google Health API) and append it to the Health Archive as a new Profile Snapshot. Each invocation creates a new dated snapshot rather than overwriting the prior one, so historical settings drift is preserved.\n\nA Profile Snapshot is not a Data Point. It is metadata about the consenting user's account and the unit conventions in force at the time of fetch.",
		Flags: withCommon(),
	},
	{
		Name:  "sync",
		Short: "Archive Google Health Data Points and supported Rollups.",
		Long:  "Pull raw Data Points for the requested Data Types within an inclusive `--from` / exclusive `--to` window and append them to the Health Archive. Sync is the primary write path; everything else in the binary either reads from the archive or refreshes metadata.\n\n`--types` accepts a comma-separated list (for example `steps,heart-rate,sleep`). `--rollup daily` switches the sync from raw Data Points to daily Rollup records for the same Data Types (where the provider supports it). `--source-family wearable` restricts the result set to Data Points whose Data Source family is a watch or tracker.\n\nA Sync Run is recorded for every invocation — succeeded or failed — so the archive carries an audit trail of attempts as well as records.",
		Flags: withCommon(
			flagSpec{Name: "types", Type: "string", Default: "steps", Usage: "comma-separated Data Types"},
			flagSpec{Name: "from", Type: "string", Default: "", Usage: "inclusive sync range start"},
			flagSpec{Name: "to", Type: "string", Default: "", Usage: "exclusive sync range end"},
			flagSpec{Name: "rollup", Type: "string", Default: "", Usage: "rollup kind to sync; supported: daily"},
			flagSpec{Name: "source-family", Type: "string", Default: "", Usage: "source family filter; supported: wearable"},
		),
	},
	{
		Name:  "status",
		Short: "Summarise archive counts and newest synced timestamps.",
		Long:  "Print a per-Data-Type summary of the Health Archive: how many Data Points are stored, the newest synced timestamp, and the most recent Sync Run status. Useful as a quick health check before or after a long sync.\n\n`status` does no provider I/O — it reads only the local Health Archive.",
		Flags: withCommon(),
	},
	{
		Name:           "query",
		Short:          "Run guarded read-only SQL over the Health Archive.",
		Long:           "Execute a single SQL statement against the Health Archive. The binary refuses anything that would write or alter the archive — `query` is for inspection, not maintenance.\n\nFlags must appear **before** the SQL argument because Go's `flag` parser stops at the first positional argument. To explore the schema, query the `sqlite_master` table or run `gohealthcli export` for the canonical normalised datasets.",
		PositionalArgs: "<sql>",
		Flags:          withCommon(),
	},
	{
		Name:           "export",
		Short:          "Write a normalised dataset to CSV or JSONL.",
		Long:           "Render one of the curated normalised datasets (daily-steps, heart-rate-samples, resting-heart-rate-by-day, sleep-sessions, exercise-sessions, weight-samples) from the Health Archive. Exports are read-only; nothing in the archive is mutated.\n\nExactly one of `--output PATH` or `--stdout` must be supplied — the explicit destination prevents an accidental terminal dump of a long export.",
		PositionalArgs: "<dataset>",
		Flags: []flagSpec{
			{Name: "config", Type: "string", Default: "", Usage: "config file path"},
			{Name: "db", Type: "string", Default: "", Usage: "SQLite Health Archive path"},
			{Name: "format", Type: "string", Default: "csv", Usage: "export format: csv or jsonl"},
			{Name: "output", Type: "string", Default: "", Usage: "write export to path"},
			{Name: "stdout", Type: "bool", Default: "false", Usage: "write export data to stdout"},
			{Name: "no-input", Type: "bool", Default: "false", Usage: "never prompt, never wait for browser input"},
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
	},
	{
		Name:   "schema",
		Short:  "Emit the command registry as JSON (hidden — used by the Project Site build).",
		Long:   "Emit the binary's command registry as a stable JSON document. The Project Site's command-reference pages are generated from this output, so the JSON shape is part of the published contract.\n\nThe subcommand is hidden from `gohealthcli --help` because it is a build-time tool, not an end-user surface. Pass `--json` (the default and only mode) to receive the document on stdout.",
		Hidden: true,
		Flags: []flagSpec{
			{Name: "json", Type: "bool", Default: "true", Usage: "emit the registry as JSON (default and currently the only output mode)"},
		},
	},
}
