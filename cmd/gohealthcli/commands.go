package main

// commandDef describes a single gohealthcli subcommand for both documentation
// and (in a later slice) dispatch. The Project Site's command-reference pages
// are generated from the JSON encoding of this slice via `gohealthcli schema
// --json` — keep field names stable, because they are part of the contract
// downstream tooling reads.
type commandDef struct {
	Name           string     `json:"name"`
	Short          string     `json:"short"`
	Long           string     `json:"long"`
	Hidden         bool       `json:"hidden"`
	PositionalArgs string     `json:"positional_args,omitempty"`
	Flags          []flagSpec `json:"flags"`
}

// flagSpec describes one flag accepted by a subcommand. The string-typed
// Default field carries the canonical default value (the same text the binary's
// --help would print), so the Project Site renders the same value the user
// would see at the prompt.
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

// commands is the registry of every subcommand the binary exposes. The
// dispatch switch and the --help formatter continue to source their data
// inline for now; subsequent slices fold them onto this registry.
//
// Entries are listed in the order that they should appear in the Project
// Site sidebar and the auto-generated command-reference index.
var commands = []commandDef{
	{
		Name:  "doctor",
		Short: "Validate local setup and provider reachability.",
		Long:  "Run a diagnostic check against the local gohealthcli installation: config presence, Health Archive path, Credential Store status, schema version, and connection count.\n\nWith `--online`, also refresh stored tokens and verify Google Health API reachability. The command never writes health data; it only inspects local state and (with `--online`) performs a single read-only round trip to the provider.\n\nThe output is a structured report on stdout. Use `--json` for stable machine-readable output, `--plain` for terminal-friendly key/value lines.",
		Flags: []flagSpec{
			{Name: "config", Type: "string", Default: "", Usage: "config file path"},
			{Name: "db", Type: "string", Default: "", Usage: "SQLite Health Archive path"},
			{Name: "json", Type: "bool", Default: "false", Usage: "write stable JSON to stdout"},
			{Name: "plain", Type: "bool", Default: "false", Usage: "write plain key/value output to stdout"},
			{Name: "online", Type: "bool", Default: "false", Usage: "refresh tokens and check provider reachability"},
			{Name: "no-input", Type: "bool", Default: "false", Usage: "never prompt, never wait for browser input"},
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
