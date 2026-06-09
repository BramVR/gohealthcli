package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

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
//     escaped string. Non-JSON columns (and JSON-typed columns whose
//     payload is NULL, empty, or fails to parse) fall back to today's
//     string behaviour.
//   - plainModeEncoder: preserves today's escape-string behaviour
//     byte-for-byte, regardless of column name.
//
// The interface is the test seam: per-adapter unit tests cover the
// branching, and the BLOB slice (PRD #144 issue #167) adds a sibling
// adapter method without touching the call sites.
type queryRowEncoder interface {
	encode(column string, value any) any
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

// encode applies the JSON-typed passthrough rule. NULLs and non-byte
// scalars flow through untouched so JSON Number/Bool/null shapes are
// preserved. Byte slices and strings on a JSON-typed column that parse
// as valid JSON become json.RawMessage; anything else falls back to a
// string so users see the literal stored bytes instead of a parse
// error.
//
// Both []byte and string need handling because the SQLite driver
// returns TEXT columns as Go strings while BLOB columns come back as
// []byte; the value is JSON-shaped either way.
func (e *jsonModeEncoder) encode(column string, value any) any {
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

// plainModeEncoder implements today's escape-string behaviour. The
// PRD wants `--plain` output byte-identical to the pre-change build,
// so this adapter does NOT consult the JSON-typed allowlist — every
// []byte value becomes a string, full stop. queryPlainValue still
// applies its control-character escaping downstream.
type plainModeEncoder struct{}

func newPlainModeEncoder() *plainModeEncoder { return &plainModeEncoder{} }

func (e *plainModeEncoder) encode(_ string, value any) any {
	if bytesValue, ok := value.([]byte); ok {
		return string(bytesValue)
	}
	return value
}

// isJSONTypedColumn returns true when the column name is on the
// curated allowlist or ends in the `_json` suffix every JSON-typed
// Normalized View column uses.
func isJSONTypedColumn(column string) bool {
	if _, ok := jsonModeAllowlist[column]; ok {
		return true
	}
	return strings.HasSuffix(column, "_json")
}

// selectQueryRowEncoder picks the encoder adapter for the requested
// output mode. `--raw-text` opts out of the JSON-passthrough even in
// JSON mode, giving users the literal stored bytes when they need
// them (regression-debugging, snapshotting an upstream payload).
// Default mode (the unparseable `Row N: k=v` shape) keeps today's
// string-cast behaviour via the plain-mode encoder; PRD #144 slice 8
// removes the default mode itself.
func selectQueryRowEncoder(mode outputMode, rawText bool) queryRowEncoder {
	if mode.json && !rawText {
		return newJSONModeEncoder()
	}
	return newPlainModeEncoder()
}

func writeQueryResult(result queryResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if mode.plain {
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
	if result.Status == "query_completed" {
		if _, err := fmt.Fprintf(stdout, "Query completed: %d rows\n", result.RowCount); err != nil {
			return err
		}
		if len(result.Columns) != 0 {
			if _, err := fmt.Fprintf(stdout, "Columns: %s\n", strings.Join(result.Columns, ", ")); err != nil {
				return err
			}
		}
		for rowIndex, row := range result.Rows {
			if _, err := fmt.Fprintf(stdout, "Row %d:", rowIndex+1); err != nil {
				return err
			}
			for columnIndex, value := range row {
				column := fmt.Sprintf("column_%d", columnIndex+1)
				if columnIndex < len(result.Columns) {
					column = result.Columns[columnIndex]
				}
				if _, err := fmt.Fprintf(stdout, " %s=%s", column, queryPlainValue(value)); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(stdout); err != nil {
				return err
			}
		}
	} else if _, err := fmt.Fprintln(stdout, "Query failed"); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "Message: %s\n", result.Message)
	return err
}

func queryPlainValue(value any) string {
	if value == nil {
		return "null"
	}
	return strings.NewReplacer(
		`\`, `\\`,
		"\r", `\r`,
		"\n", `\n`,
		"\t", `\t`,
	).Replace(fmt.Sprint(value))
}
