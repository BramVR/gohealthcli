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

func runQuery(args []string, configPath, archivePath string, archivePathExplicit bool, mode outputMode, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("query", flag.ContinueOnError)
	flags.SetOutput(stderr)

	queryConfigPath := flags.String("config", configPath, "config file path")
	queryArchivePath := flags.String("db", archivePath, "SQLite Health Archive path")
	queryJSONOutput := flags.Bool("json", mode.json, "write stable JSON to stdout")
	queryPlainOutput := flags.Bool("plain", mode.plain, "write plain key/value output to stdout")
	flags.Bool("no-input", false, "never prompt, never wait for browser input")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "query requires exactly one SQL statement")
		return 1
	}

	mode = outputMode{json: *queryJSONOutput, plain: *queryPlainOutput}
	resolvedArchivePath, err := resolveConfiguredArchivePath(*queryConfigPath, *queryArchivePath, archivePathExplicit || flagWasProvided(flags, "db"))
	if err != nil {
		result := queryResult{Status: "query_failed", ArchivePath: *queryArchivePath, Message: err.Error()}
		if writeErr := writeQueryResult(result, mode, stdout); writeErr != nil {
			fmt.Fprintf(stderr, "write output: %v\n", writeErr)
			return 1
		}
		return 1
	}
	result, err := querySetup(resolvedArchivePath, flags.Arg(0))
	if err != nil {
		result.Status = "query_failed"
		result.Message = err.Error()
		if writeErr := writeQueryResult(result, mode, stdout); writeErr != nil {
			fmt.Fprintf(stderr, "write output: %v\n", writeErr)
			return 1
		}
		return 1
	}
	if err := writeQueryResult(result, mode, stdout); err != nil {
		fmt.Fprintf(stderr, "write output: %v\n", err)
		return 1
	}
	return 0
}

func querySetup(archivePath, statement string) (queryResult, error) {
	result := queryResult{
		Status:      "query_failed",
		ArchivePath: archivePath,
	}
	if err := validateQueryStatement(statement); err != nil {
		return result, err
	}
	if _, err := inspectArchive(archivePath, false); err != nil {
		return result, fmt.Errorf("Health Archive check failed: %w", err)
	}
	db, err := openArchiveReadOnly(archivePath)
	if err != nil {
		return result, err
	}
	defer db.Close()

	rows, err := db.Query(statement)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return result, err
	}
	result.Columns = columns
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return result, err
		}
		for index, value := range values {
			values[index] = queryOutputValue(value)
		}
		result.Rows = append(result.Rows, values)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	result.Status = "query_completed"
	result.RowCount = len(result.Rows)
	result.Message = "Query completed"
	return result, nil
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

func queryOutputValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	default:
		return typed
	}
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
	return fmt.Sprint(value)
}
