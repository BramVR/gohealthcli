package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// blobBase64MarkerKey is the documented JSON wrapper key that
// `--json` mode uses to mark a base64-encoded BLOB cell. Downstream
// consumers detect a BLOB column by checking for this key.
const blobBase64MarkerKey = "__blob_base64__"

// blobPlainPrefix is the documented `--plain` prefix that precedes a
// base64-encoded BLOB payload, keeping the row.N.M line parseable
// even when the underlying bytes contain control characters or
// invalid UTF-8 sequences.
const blobPlainPrefix = "<blob:base64>"

// blobDatabaseTypeName is SQLite's BLOB type-name token, the signal
// the encoders use to switch into base64 mode. SQLite reports type
// names in uppercase via DatabaseTypeName(); we match
// case-insensitively defensively.
const blobDatabaseTypeName = "BLOB"

type queryResult struct {
	Status      string   `json:"status"`
	ArchivePath string   `json:"archive_path"`
	Columns     []string `json:"columns,omitempty"`
	Rows        [][]any  `json:"rows,omitempty"`
	RowCount    int      `json:"row_count"`
	Message     string   `json:"message"`
}

func runQuery(args []string, configPath, archivePath string, configPathExplicit, archivePathExplicit bool, mode outputMode, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("query", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:          configPath,
		ArchivePath:         archivePath,
		JSONOutput:          mode.json,
		PlainOutput:         mode.plain,
		ArchivePathExplicit: archivePathExplicit,
		ConfigPathExplicit:  configPathExplicit,
	})
	// --raw-text opts out of the JSON-typed column passthrough that
	// `--json` enables by default. Plain mode never participates in
	// the passthrough, so the flag is a no-op there; we still accept
	// it so script authors can pass `--raw-text` defensively.
	var rawText bool
	flags.BoolVar(&rawText, "raw-text", false, "in JSON mode, return JSON-typed columns as strings instead of nested objects")

	if err := ParseCommon(flags, common, args); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 1 {
		return ReportFailure(FailureReport{
			Command: "query",
			Status:  StatusFlagInvalid,
			Message: "query requires exactly one SQL statement",
			Mode:    mode,
		}, stdout, stderr)
	}

	resolvedArchivePath, err := resolveReadArchivePath(*common)
	if err != nil {
		result := queryResult{Status: "query_failed", ArchivePath: common.ArchivePath, Message: err.Error()}
		if writeErr := writeQueryResult(result, mode, stdout); writeErr != nil {
			return ReportFailure(FailureReport{
				Command: "query",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 1
	}
	encoder := selectQueryRowEncoder(mode, rawText)
	result, err := querySetup(resolvedArchivePath, flags.Arg(0), encoder)
	if err != nil {
		result.Status = "query_failed"
		result.Message = err.Error()
		if writeErr := writeQueryResult(result, mode, stdout); writeErr != nil {
			return ReportFailure(FailureReport{
				Command: "query",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 1
	}
	if err := writeQueryResult(result, mode, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "query",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

func validateQueryStatement(statement string) error {
	trimmed := strings.TrimSpace(statement)
	if trimmed == "" {
		return errors.New("query requires a SQL statement")
	}
	body, err := singleSQLStatement(trimmed)
	if err != nil {
		return err
	}
	statementKind, err := queryStatementKind(body)
	if err != nil {
		return err
	}
	if statementKind != "select" {
		return errors.New("query accepts SELECT statements only")
	}
	return nil
}

func singleSQLStatement(statement string) (string, error) {
	inSingleQuote := false
	inDoubleQuote := false
	inLineComment := false
	inBlockComment := false
	statementEnd := -1
	for index := 0; index < len(statement); index++ {
		current := statement[index]
		var next byte
		if index+1 < len(statement) {
			next = statement[index+1]
		}
		if inLineComment {
			if current == '\n' || current == '\r' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if current == '*' && next == '/' {
				inBlockComment = false
				index++
			}
			continue
		}
		if inSingleQuote {
			if current == '\'' {
				if next == '\'' {
					index++
				} else {
					inSingleQuote = false
				}
			}
			continue
		}
		if inDoubleQuote {
			if current == '"' {
				if next == '"' {
					index++
				} else {
					inDoubleQuote = false
				}
			}
			continue
		}
		switch {
		case current == '-' && next == '-':
			inLineComment = true
			index++
		case current == '/' && next == '*':
			inBlockComment = true
			index++
		case current == '\'':
			inSingleQuote = true
		case current == '"':
			inDoubleQuote = true
		case current == ';':
			statementEnd = index
			if trimSQLSpaceAndComments(statement[index+1:]) != "" {
				return "", errors.New("query accepts one SELECT statement only")
			}
		}
	}
	if inSingleQuote || inDoubleQuote || inBlockComment {
		return "", errors.New("query SQL is incomplete")
	}
	if statementEnd >= 0 {
		return strings.TrimSpace(statement[:statementEnd]), nil
	}
	return statement, nil
}

func queryStatementKind(statement string) (string, error) {
	firstToken, rest := consumeSQLToken(statement)
	if firstToken != "with" {
		return firstToken, nil
	}
	token, rest := consumeSQLToken(rest)
	if token == "recursive" {
		token, rest = consumeSQLToken(rest)
	}
	for {
		if token == "" {
			return "", errors.New("query CTE is incomplete")
		}
		rest = trimSQLSpaceAndComments(rest)
		if strings.HasPrefix(rest, "(") {
			var err error
			rest, err = skipSQLParenthetical(rest)
			if err != nil {
				return "", err
			}
		}
		token, rest = consumeSQLToken(rest)
		if token != "as" {
			return "", errors.New("query CTE is incomplete")
		}
		rest = trimSQLSpaceAndComments(rest)
		token, afterOption := consumeSQLToken(rest)
		if token == "materialized" {
			rest = afterOption
		} else if token == "not" {
			token, rest = consumeSQLToken(afterOption)
			if token != "materialized" {
				return "", errors.New("query CTE is incomplete")
			}
		}
		rest = trimSQLSpaceAndComments(rest)
		if !strings.HasPrefix(rest, "(") {
			return "", errors.New("query CTE is incomplete")
		}
		var err error
		rest, err = skipSQLParenthetical(rest)
		if err != nil {
			return "", err
		}
		rest = trimSQLSpaceAndComments(rest)
		if strings.HasPrefix(rest, ",") {
			token, rest = consumeSQLToken(rest[1:])
			continue
		}
		token, _ = consumeSQLToken(rest)
		return token, nil
	}
}

func consumeSQLToken(statement string) (string, string) {
	trimmed := trimSQLSpaceAndComments(statement)
	for index, char := range trimmed {
		if index == 0 && !sqlIdentifierStart(char) {
			return "", trimmed
		}
		if !sqlIdentifierPart(char) {
			return strings.ToLower(trimmed[:index]), trimmed[index:]
		}
	}
	return strings.ToLower(trimmed), ""
}

func sqlIdentifierStart(char rune) bool {
	return char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

func sqlIdentifierPart(char rune) bool {
	return sqlIdentifierStart(char) || char >= '0' && char <= '9'
}

func trimSQLSpaceAndComments(statement string) string {
	trimmed := strings.TrimSpace(statement)
	for {
		switch {
		case strings.HasPrefix(trimmed, "--"):
			newline := strings.IndexAny(trimmed, "\r\n")
			if newline < 0 {
				return ""
			}
			trimmed = strings.TrimSpace(trimmed[newline+1:])
		case strings.HasPrefix(trimmed, "/*"):
			end := strings.Index(trimmed, "*/")
			if end < 0 {
				return ""
			}
			trimmed = strings.TrimSpace(trimmed[end+2:])
		default:
			return trimmed
		}
	}
}

func skipSQLParenthetical(statement string) (string, error) {
	if !strings.HasPrefix(statement, "(") {
		return statement, nil
	}
	depth := 0
	inSingleQuote := false
	inDoubleQuote := false
	inLineComment := false
	inBlockComment := false
	for index := 0; index < len(statement); index++ {
		current := statement[index]
		var next byte
		if index+1 < len(statement) {
			next = statement[index+1]
		}
		if inLineComment {
			if current == '\n' || current == '\r' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if current == '*' && next == '/' {
				inBlockComment = false
				index++
			}
			continue
		}
		if inSingleQuote {
			if current == '\'' {
				if next == '\'' {
					index++
				} else {
					inSingleQuote = false
				}
			}
			continue
		}
		if inDoubleQuote {
			if current == '"' {
				if next == '"' {
					index++
				} else {
					inDoubleQuote = false
				}
			}
			continue
		}
		switch {
		case current == '-' && next == '-':
			inLineComment = true
			index++
		case current == '/' && next == '*':
			inBlockComment = true
			index++
		case current == '\'':
			inSingleQuote = true
		case current == '"':
			inDoubleQuote = true
		case current == '(':
			depth++
		case current == ')':
			depth--
			if depth == 0 {
				return statement[index+1:], nil
			}
		}
	}
	return "", errors.New("query SQL is incomplete")
}

// queryRowEncoder owns per-mode conversion of a single scanned column
// value (`database/sql` hands raw scalars and []byte for TEXT/BLOB) into
// the value that lands in queryResult.Rows. Two adapters exist:
//
//   - jsonModeEncoder: JSON-typed columns pass through as nested
//     json.RawMessage so a stdlib JSON consumer sees an object, not an
//     escaped string. BLOB columns are wrapped in the documented
//     `{"__blob_base64__": "..."}` marker so raw bytes never reach
//     `encoding/json`'s UTF-8 path (which would mangle them with
//     replacement characters). Non-JSON, non-BLOB columns (and
//     JSON-typed columns whose payload is NULL, empty, or fails to
//     parse) fall back to today's string behaviour.
//   - plainModeEncoder: preserves today's escape-string behaviour
//     byte-for-byte for TEXT-shaped scan results, but base64-encodes
//     BLOB columns under a documented `<blob:base64>` prefix so the
//     row.N.M line stays parseable.
//
// databaseTypeName is the SQLite type name as reported by
// `sql.ColumnType.DatabaseTypeName()` (BLOB / TEXT / INTEGER / REAL /
// "" for typeless view columns). The encoders use it to detect BLOB
// columns; everything else routes by column name + scan value type
// per the slice 5 contract.
//
// The interface is the test seam: per-adapter unit tests cover the
// branching against a fixture-driven (column, type, value) input.
type queryRowEncoder interface {
	encode(column, databaseTypeName string, value any) any
}

// jsonModeEncoder implements the JSON-passthrough behaviour for
// `query --json`. The JSON-typed column allowlist is a curated set of
// raw column names plus any column whose name ends in `_json`; this
// matches every column the Health Archive's writer stores JSON into
// today and the suffix convention every future Normalized View follows.
type jsonModeEncoder struct{}

// jsonModeAllowlist enumerates the JSON-typed column names that are NOT
// captured by the `*_json` suffix rule. New JSON-typed columns whose
// name does not end in `_json` (e.g. timezone_metadata) belong here.
var jsonModeAllowlist = map[string]struct{}{
	"raw_json":             {},
	"data_source_json":     {},
	"timezone_metadata":    {},
	"token_metadata_json":  {},
	"google_identity_json": {},
}

func newJSONModeEncoder() *jsonModeEncoder { return &jsonModeEncoder{} }

// encode applies the JSON-typed passthrough rule plus the BLOB
// base64-wrap rule. NULLs and non-byte scalars flow through untouched
// so JSON Number/Bool/null shapes are preserved.
//
// BLOB columns take precedence over the JSON-typed column-name
// allowlist: a column named `raw_json` that comes back as a BLOB is
// base64-encoded under the `__blob_base64__` marker, never
// double-parsed as JSON. This stops `encoding/json` from mangling raw
// bytes through its UTF-8 path, which today produces silent
// replacement characters.
//
// Byte slices and strings on a JSON-typed column that parse as valid
// JSON become json.RawMessage; anything else falls back to a string
// so users see the literal stored bytes instead of a parse error.
//
// Both []byte and string need handling because the SQLite driver
// returns TEXT columns as Go strings while BLOB columns come back as
// []byte; the value is JSON-shaped either way.
func (e *jsonModeEncoder) encode(column, databaseTypeName string, value any) any {
	if value == nil {
		return nil
	}
	if isBLOBScanResult(databaseTypeName, value) {
		bytesValue, _ := value.([]byte)
		return map[string]string{
			blobBase64MarkerKey: base64.StdEncoding.EncodeToString(bytesValue),
		}
	}
	bytesValue, ok := bytesFromScanValue(value)
	if !ok {
		return value
	}
	if !isJSONTypedColumn(column) {
		return string(bytesValue)
	}
	if len(bytesValue) == 0 {
		return string(bytesValue)
	}
	if !json.Valid(bytesValue) {
		return string(bytesValue)
	}
	// Copy the bytes: database/sql reuses the scan buffer across rows
	// and we hand this slice to the JSON encoder later (after the next
	// row.Scan call, possibly).
	copied := make(json.RawMessage, len(bytesValue))
	copy(copied, bytesValue)
	return copied
}

// bytesFromScanValue normalises the TEXT/BLOB scan result to []byte.
// The SQLite driver hands TEXT back as string and BLOB as []byte; the
// JSON-passthrough rule treats both the same way.
func bytesFromScanValue(value any) ([]byte, bool) {
	switch typed := value.(type) {
	case []byte:
		return typed, true
	case string:
		return []byte(typed), true
	default:
		return nil, false
	}
}

// plainModeEncoder implements today's escape-string behaviour for
// TEXT-shaped scan results, plus the BLOB base64-prefix rule. The PRD
// wants `--plain` TEXT output byte-identical to the pre-change build,
// so this adapter does NOT consult the JSON-typed allowlist — every
// non-BLOB []byte value becomes a string, full stop. queryPlainValue
// still applies its control-character escaping downstream.
//
// BLOB columns (detected via the SQLite type name) are stamped with
// the documented `<blob:base64>` prefix and emitted as a single
// base64-encoded payload so the row.N.M line stays parseable. Without
// the prefix today's path produces `�` replacement characters and
// silent data loss.
type plainModeEncoder struct{}

func newPlainModeEncoder() *plainModeEncoder { return &plainModeEncoder{} }

func (e *plainModeEncoder) encode(_, databaseTypeName string, value any) any {
	if value == nil {
		return nil
	}
	if isBLOBScanResult(databaseTypeName, value) {
		bytesValue, _ := value.([]byte)
		return blobPlainPrefix + base64.StdEncoding.EncodeToString(bytesValue)
	}
	if bytesValue, ok := value.([]byte); ok {
		return string(bytesValue)
	}
	return value
}

// isBLOBScanResult returns true when a scanned column should be
// treated as a BLOB and base64-encoded. Two signals combine:
//
//  1. `sql.ColumnType.DatabaseTypeName()` reports "BLOB" — the strong
//     signal for schema-declared BLOB columns. Match is
//     case-insensitive: SQLite reports the token in uppercase today
//     but `DatabaseTypeName()` is driver-defined, so we normalize.
//  2. `DatabaseTypeName()` is empty (typeless: view columns, SQL
//     literals, or builtins like `randomblob()`) AND the scan value
//     is `[]byte`. modernc.org/sqlite (the driver this binary links)
//     hands TEXT cells back as Go strings and BLOB cells as []byte,
//     so a typeless column whose scan value is []byte is
//     observationally a BLOB.
//
// Signal (2) is the path the PRD's randomblob acceptance criterion
// goes through: `randomblob(8)` carries no declared type but the
// driver hands us 8 raw bytes, which the JSON encoder would otherwise
// UTF-8-mangle.
func isBLOBScanResult(databaseTypeName string, value any) bool {
	if strings.EqualFold(databaseTypeName, blobDatabaseTypeName) {
		return true
	}
	if databaseTypeName != "" {
		return false
	}
	_, ok := value.([]byte)
	return ok
}

// isJSONTypedColumn returns true when the column name is on the
// curated allowlist or ends in the `_json` suffix every JSON-typed
// Normalized View column uses. Match is case-insensitive: SQLite
// identifiers are case-insensitive and `rows.Columns()` preserves the
// query/alias casing, so `SELECT raw_json AS Raw_JSON ...` must still
// route through the JSON encoder.
func isJSONTypedColumn(column string) bool {
	lower := strings.ToLower(column)
	if _, ok := jsonModeAllowlist[lower]; ok {
		return true
	}
	return strings.HasSuffix(lower, "_json")
}

// selectQueryRowEncoder picks the encoder adapter for the requested
// output mode. `--raw-text` opts out of the JSON-passthrough even in
// JSON mode, giving users the literal stored bytes when they need
// them (regression-debugging, snapshotting an upstream payload).
// The no-flag default falls through to the plain-mode encoder per
// PRD #144 slice 7 (issue #168) — the legacy `Row N: k=v` shape is
// gone; the default now produces the same parseable bytes as --plain.
func selectQueryRowEncoder(mode outputMode, rawText bool) queryRowEncoder {
	if mode.json && !rawText {
		return newJSONModeEncoder()
	}
	return newPlainModeEncoder()
}

// writeQueryResult emits the query envelope in one of two shapes:
//
//   - JSON when mode.json is set.
//   - Plain key/value otherwise.
//
// PRD #144 slice 7 (issue #168) removed the legacy `Row N: column=value ...`
// default. The no-flag default now silently falls through to the plain shape:
// scripts and LLM consumers get parseable output by default, and the
// silent-footgun where `SELECT 'a b' AS x` produced unparseable
// `Row 1: x=a b` is structurally impossible. No stderr warning fires — the
// shape change is documented in the command's Long help (commands.go) and
// docs/commands/query.md.
func writeQueryResult(result queryResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
		return err
	}
	if result.ArchivePath != "" {
		if _, err := fmt.Fprintf(stdout, "archive_path: %s\n", result.ArchivePath); err != nil {
			return err
		}
	}
	if len(result.Columns) != 0 {
		if _, err := fmt.Fprintf(stdout, "columns: %s\n", strings.Join(result.Columns, ",")); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "row_count: %d\n", result.RowCount); err != nil {
		return err
	}
	for rowIndex, row := range result.Rows {
		for columnIndex, value := range row {
			if _, err := fmt.Fprintf(stdout, "row.%d.%d: %s\n", rowIndex+1, columnIndex+1, queryPlainValue(value)); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
	return err
}

func queryPlainValue(value any) string {
	if value == nil {
		return "null"
	}
	return escapePlainControlChars(fmt.Sprint(value))
}

// escapePlainControlChars renders a plain/default-mode string so that no
// C0 (0x00–0x1F) or C1 (0x7F–0x9F) control byte ever reaches the terminal
// raw — the fix for terminal escape-sequence injection (CWE-150, issue #244).
// Backslash is doubled first so the escaping is reversible, the familiar
// \r/\n/\t named forms are preserved (keeping the established
// line-parseability convention), and every other control byte — ESC (0x1b),
// BEL (0x07), DEL (0x7f), and the rest — renders as a visible \xHH escape.
// Bytes 0xA0–0xFF that are part of a valid multi-byte UTF-8 rune are left
// intact; only the C1 control range is escaped. JSON output mode never routes
// through here, so its \u escaping is untouched.
func escapePlainControlChars(value string) string {
	if !strings.ContainsFunc(value, isPlainControlRune) {
		return value
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '\r':
			builder.WriteString(`\r`)
		case '\n':
			builder.WriteString(`\n`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if isPlainControlRune(r) {
				fmt.Fprintf(&builder, `\x%02x`, r)
				continue
			}
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// isPlainControlRune reports whether r is a C0 (0x00–0x1F) or C1
// (0x7F–0x9F) control character that must be escaped before plain-mode
// output. Backslash and the \r/\n/\t whitespace runes are not flagged here
// because escapePlainControlChars handles their named/doubled forms ahead of
// the generic \xHH fallback.
func isPlainControlRune(r rune) bool {
	if r == '\\' {
		return true
	}
	return r <= 0x1f || (r >= 0x7f && r <= 0x9f)
}
