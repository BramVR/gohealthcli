package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// captureQuery runs the binary entry point against a real temp archive
// keyed by configPath and returns stdout / stderr as strings.
// queryArgs are the flags + SQL that follow `query` on the command
// line. Tests assert exit code 0 inside this helper so callers can
// focus on the output shape, which is what the slice 5 invariants
// are about.
func captureQuery(t *testing.T, configPath string, queryArgs ...string) (stdout, stderr string) {
	t.Helper()
	stdoutBuf := new(bytes.Buffer)
	stderrBuf := new(bytes.Buffer)
	args := append([]string{"query", "--config", configPath}, queryArgs...)
	code := run(args, stdoutBuf, stderrBuf)
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderrBuf.String(), stdoutBuf.String())
	}
	return stdoutBuf.String(), stderrBuf.String()
}

// TestJSONModeEncoderJSONTypedColumnPassesThrough asserts a JSON-typed
// column whose value parses as valid JSON is emitted as a nested
// json.RawMessage so downstream JSON consumers see an object, not an
// escaped string.
func TestJSONModeEncoderJSONTypedColumnPassesThrough(t *testing.T) {
	encoder := newJSONModeEncoder()
	value := encoder.encode("raw_json", "TEXT", []byte(`{"steps":{"count":"512"}}`))
	raw, ok := value.(json.RawMessage)
	if !ok {
		t.Fatalf("encoded value type = %T, want json.RawMessage", value)
	}
	row := map[string]any{"raw_json": raw}
	marshalled, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(marshalled, &got); err != nil {
		t.Fatalf("unmarshal row: %v", err)
	}
	var nested map[string]any
	if err := json.Unmarshal(got["raw_json"], &nested); err != nil {
		t.Fatalf("nested raw_json is not a JSON object: %v\nraw: %s", err, got["raw_json"])
	}
	if _, ok := nested["steps"].(map[string]any); !ok {
		t.Fatalf("nested.steps = %T(%v), want object", nested["steps"], nested["steps"])
	}
}

// TestJSONModeEncoderJSONTypedColumnNULLStaysNull asserts that a NULL
// value in a JSON-typed column stays nil — the encoder must not
// invent a string or empty object.
func TestJSONModeEncoderJSONTypedColumnNULLStaysNull(t *testing.T) {
	encoder := newJSONModeEncoder()
	value := encoder.encode("raw_json", "TEXT", nil)
	if value != nil {
		t.Fatalf("encoded NULL = %T(%v), want nil", value, value)
	}
}

// TestJSONModeEncoderJSONTypedColumnInvalidJSONFallsBackToString
// asserts the encoder falls back to today's string behaviour when the
// payload is not valid JSON, so users still see the literal bytes
// instead of an error.
func TestJSONModeEncoderJSONTypedColumnInvalidJSONFallsBackToString(t *testing.T) {
	encoder := newJSONModeEncoder()
	value := encoder.encode("raw_json", "TEXT", []byte("not json {"))
	got, ok := value.(string)
	if !ok {
		t.Fatalf("encoded value type = %T, want string", value)
	}
	if got != "not json {" {
		t.Fatalf("encoded value = %q, want literal payload", got)
	}
}

// TestJSONModeEncoderJSONTypedColumnEmptyBytesFallsBackToString asserts
// that empty []byte (not NULL — empty TEXT) becomes the empty string,
// matching today's behaviour. We don't want empty bytes to surface as
// invalid JSON errors.
func TestJSONModeEncoderJSONTypedColumnEmptyBytesFallsBackToString(t *testing.T) {
	encoder := newJSONModeEncoder()
	value := encoder.encode("raw_json", "TEXT", []byte{})
	got, ok := value.(string)
	if !ok {
		t.Fatalf("encoded value type = %T, want string", value)
	}
	if got != "" {
		t.Fatalf("encoded value = %q, want empty string", got)
	}
}

// TestJSONModeEncoderNonJSONColumnWithSpacesStaysString asserts a value
// whose column name is not on the JSON allowlist is encoded as a
// literal string — even when the payload happens to contain whitespace
// or "JSON-shaped" characters.
func TestJSONModeEncoderNonJSONColumnWithSpacesStaysString(t *testing.T) {
	encoder := newJSONModeEncoder()
	value := encoder.encode("greeting", "TEXT", []byte("hello world"))
	got, ok := value.(string)
	if !ok {
		t.Fatalf("encoded value type = %T(%v), want string", value, value)
	}
	if got != "hello world" {
		t.Fatalf("encoded value = %q, want %q", got, "hello world")
	}
}

// TestJSONModeEncoderJSONSuffixedColumnPassesThrough asserts the
// `*_json` suffix rule kicks in for column names not on the curated
// allowlist (e.g. a custom view that aliases a JSON column as
// `payload_json`).
func TestJSONModeEncoderJSONSuffixedColumnPassesThrough(t *testing.T) {
	encoder := newJSONModeEncoder()
	value := encoder.encode("payload_json", "TEXT", []byte(`{"ok":true}`))
	if _, ok := value.(json.RawMessage); !ok {
		t.Fatalf("encoded value type = %T(%v), want json.RawMessage", value, value)
	}
}

// TestJSONModeEncoderAllowlistedColumnNames asserts every documented
// JSON-typed column name on the allowlist participates in passthrough.
func TestJSONModeEncoderAllowlistedColumnNames(t *testing.T) {
	encoder := newJSONModeEncoder()
	for _, name := range []string{
		"raw_json",
		"data_source_json",
		"timezone_metadata",
		"token_metadata_json",
		"google_identity_json",
	} {
		value := encoder.encode(name, "TEXT", []byte(`{"k":"v"}`))
		if _, ok := value.(json.RawMessage); !ok {
			t.Fatalf("column %q encoded as %T(%v), want json.RawMessage", name, value, value)
		}
	}
}

// TestJSONModeEncoderNonByteScalarsUnchanged asserts numeric / bool
// values flow through untouched — only []byte (TEXT/BLOB scan result)
// gets the JSON-passthrough treatment.
func TestJSONModeEncoderNonByteScalarsUnchanged(t *testing.T) {
	encoder := newJSONModeEncoder()
	if got := encoder.encode("count", "INTEGER", int64(42)); got != int64(42) {
		t.Fatalf("encode(int64) = %T(%v), want int64(42)", got, got)
	}
	if got := encoder.encode("ratio", "REAL", 1.5); got != 1.5 {
		t.Fatalf("encode(float64) = %T(%v), want float64(1.5)", got, got)
	}
	if got := encoder.encode("flag", "INTEGER", true); got != true {
		t.Fatalf("encode(bool) = %T(%v), want true", got, got)
	}
}

// TestPlainModeEncoderPreservesEscapeStringBehaviour asserts the plain
// encoder keeps today's string-cast behaviour byte-for-byte: control
// characters get escaped by queryPlainValue downstream, but the
// encoder's job is the raw []byte → string conversion.
func TestPlainModeEncoderPreservesEscapeStringBehaviour(t *testing.T) {
	encoder := newPlainModeEncoder()
	// JSON-typed column: still a plain string (no passthrough).
	value := encoder.encode("raw_json", "TEXT", []byte(`{"steps":{"count":"512"}}`))
	got, ok := value.(string)
	if !ok {
		t.Fatalf("encoded value type = %T(%v), want string", value, value)
	}
	if got != `{"steps":{"count":"512"}}` {
		t.Fatalf("encoded value = %q, want literal JSON string", got)
	}
	// NULL stays nil.
	if encoder.encode("raw_json", "TEXT", nil) != nil {
		t.Fatalf("encode(NULL) != nil")
	}
	// Non-byte scalars unchanged.
	if got := encoder.encode("count", "INTEGER", int64(7)); got != int64(7) {
		t.Fatalf("encode(int64) = %T(%v), want int64(7)", got, got)
	}
}

// TestJSONModeEncoderEndToEndQueryRawJSONReturnsNestedObject is the
// vertical tracer-bullet test: run the binary entry point against a
// real temp archive, parse the stdout, and assert
// rows[0][0] is a JSON object — the exact acceptance criterion the
// issue calls out.
func TestJSONModeEncoderEndToEndQueryRawJSONReturnsNestedObject(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout, stderr := captureQuery(t, configPath, "--json", "SELECT raw_json FROM data_points WHERE data_type = 'steps' ORDER BY end_time_utc LIMIT 1")
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var got struct {
		Rows [][]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if len(got.Rows) != 1 || len(got.Rows[0]) != 1 {
		t.Fatalf("rows shape = %v, want [[<raw_json>]]", got.Rows)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(got.Rows[0][0])), "{") {
		t.Fatalf("rows[0][0] = %s, want a JSON object literal", got.Rows[0][0])
	}
	var nested map[string]any
	if err := json.Unmarshal(got.Rows[0][0], &nested); err != nil {
		t.Fatalf("rows[0][0] is not a JSON object: %v\nraw: %s", err, got.Rows[0][0])
	}
	if _, ok := nested["steps"].(map[string]any); !ok {
		t.Fatalf("nested.steps = %T(%v), want object", nested["steps"], nested["steps"])
	}
}

// TestJSONModeEncoderEndToEndQueryDataSourceJSONReturnsNestedObject
// covers the second allowlisted column the issue calls out by name.
func TestJSONModeEncoderEndToEndQueryDataSourceJSONReturnsNestedObject(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	// data_source_json is "{}" in the fixture — still a valid JSON
	// object that should pass through as an object, not a string.
	// ORDER BY rowid keeps LIMIT 1 deterministic regardless of insertion
	// order or future planner changes.
	stdout, stderr := captureQuery(t, configPath, "--json", "SELECT data_source_json FROM data_points ORDER BY rowid LIMIT 1")
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var got struct {
		Rows [][]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if len(got.Rows) != 1 || len(got.Rows[0]) != 1 {
		t.Fatalf("rows shape = %v, want [[<data_source_json>]]", got.Rows)
	}
	trimmed := strings.TrimSpace(string(got.Rows[0][0]))
	if !strings.HasPrefix(trimmed, "{") {
		t.Fatalf("rows[0][0] = %s, want a JSON object literal", got.Rows[0][0])
	}
}

// TestJSONModeRawTextFlagPreservesStringEncoding asserts the
// --raw-text opt-out flag selects today's escape-string behaviour
// even in JSON mode, so users who want the literal stored bytes can
// disable the passthrough.
func TestJSONModeRawTextFlagPreservesStringEncoding(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout, stderr := captureQuery(t, configPath, "--json", "--raw-text", "SELECT raw_json FROM data_points WHERE data_type = 'steps' ORDER BY end_time_utc LIMIT 1")
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var got struct {
		Rows [][]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if len(got.Rows) != 1 || len(got.Rows[0]) != 1 {
		t.Fatalf("rows shape = %v, want one row with one column", got.Rows)
	}
	raw, ok := got.Rows[0][0].(string)
	if !ok {
		t.Fatalf("rows[0][0] = %T(%v), want string", got.Rows[0][0], got.Rows[0][0])
	}
	if !strings.HasPrefix(raw, "{") {
		t.Fatalf("rows[0][0] = %q, want a JSON-shaped string literal", raw)
	}
}

// TestJSONModeNonJSONColumnReturnsString asserts a SELECT that does
// not project a JSON-typed column round-trips as a plain string
// (literal text), not a JSON object — the PRD's "non-JSON columns
// unaffected" invariant.
func TestJSONModeNonJSONColumnReturnsString(t *testing.T) {
	tempDir := t.TempDir()
	configPath, _, _ := initializeFileCredentialSetup(t, tempDir)

	stdout, stderr := captureQuery(t, configPath, "--json", "SELECT 'hello world' AS greeting")
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var got struct {
		Rows [][]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if len(got.Rows) != 1 || len(got.Rows[0]) != 1 {
		t.Fatalf("rows shape = %v, want one row with one column", got.Rows)
	}
	greeting, ok := got.Rows[0][0].(string)
	if !ok {
		t.Fatalf("rows[0][0] = %T(%v), want string", got.Rows[0][0], got.Rows[0][0])
	}
	if greeting != "hello world" {
		t.Fatalf("rows[0][0] = %q, want %q", greeting, "hello world")
	}
}

// TestJSONModeEncoderBLOBColumnBase64RoundTrip asserts a BLOB column
// (signalled via the sql.ColumnType.DatabaseTypeName() == "BLOB" path)
// is wrapped in the documented `{"__blob_base64__": "..."}` marker and
// the payload base64-decodes back to the original bytes. The marker
// shape is the contract `docs/commands/query.md` documents.
func TestJSONModeEncoderBLOBColumnBase64RoundTrip(t *testing.T) {
	encoder := newJSONModeEncoder()
	payload := []byte{0x00, 0xFF, 0x10, 0xAB, 0xCD, 0xEF, 0x01, 0x7F}
	value := encoder.encode("b", "BLOB", payload)
	wrapper, ok := value.(map[string]string)
	if !ok {
		t.Fatalf("encoded value type = %T(%v), want map[string]string wrapper", value, value)
	}
	encoded, ok := wrapper[blobBase64MarkerKey]
	if !ok {
		t.Fatalf("encoded wrapper = %v, want key %q", wrapper, blobBase64MarkerKey)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v (payload=%q)", err, encoded)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded bytes = %v, want %v", decoded, payload)
	}
}

// TestJSONModeEncoderBLOBColumnRoundTripsThroughJSON asserts the
// marker shape survives a marshal/unmarshal cycle as a JSON object
// with a single string field, which is the contract every downstream
// consumer reads.
func TestJSONModeEncoderBLOBColumnRoundTripsThroughJSON(t *testing.T) {
	encoder := newJSONModeEncoder()
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	value := encoder.encode("b", "BLOB", payload)
	marshalled, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(marshalled, &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, marshalled)
	}
	encoded, ok := got[blobBase64MarkerKey]
	if !ok {
		t.Fatalf("unmarshalled = %v, want key %q", got, blobBase64MarkerKey)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded = %v, want %v", decoded, payload)
	}
}

// TestJSONModeEncoderBLOBColumnNULLStaysNull asserts a NULL BLOB
// column stays nil — the marker wrapper is reserved for actual byte
// payloads, never invented from thin air.
func TestJSONModeEncoderBLOBColumnNULLStaysNull(t *testing.T) {
	encoder := newJSONModeEncoder()
	if got := encoder.encode("b", "BLOB", nil); got != nil {
		t.Fatalf("encode(NULL BLOB) = %T(%v), want nil", got, got)
	}
}

// TestJSONModeEncoderBLOBColumnEmptyBytesEncodesEmptyMarker asserts an
// empty BLOB (zero-length but not NULL) still surfaces as the marker
// wrapper with an empty base64 payload so consumers can distinguish
// "stored zero bytes" from NULL.
func TestJSONModeEncoderBLOBColumnEmptyBytesEncodesEmptyMarker(t *testing.T) {
	encoder := newJSONModeEncoder()
	value := encoder.encode("b", "BLOB", []byte{})
	wrapper, ok := value.(map[string]string)
	if !ok {
		t.Fatalf("encoded value type = %T(%v), want marker wrapper", value, value)
	}
	encoded, ok := wrapper[blobBase64MarkerKey]
	if !ok {
		t.Fatalf("wrapper = %v, want key %q", wrapper, blobBase64MarkerKey)
	}
	if encoded != "" {
		t.Fatalf("encoded = %q, want empty base64 payload", encoded)
	}
}

// TestJSONModeEncoderBLOBOverridesJSONAllowlist asserts the BLOB
// type-name signal takes precedence over the JSON-typed column-name
// allowlist: a column named `raw_json` that comes back as a BLOB (e.g.
// because someone stored raw bytes in it) is base64-encoded, never
// double-parsed as JSON.
func TestJSONModeEncoderBLOBOverridesJSONAllowlist(t *testing.T) {
	encoder := newJSONModeEncoder()
	payload := []byte{0x01, 0x02, 0x03}
	value := encoder.encode("raw_json", "BLOB", payload)
	wrapper, ok := value.(map[string]string)
	if !ok {
		t.Fatalf("encoded value type = %T(%v), want marker wrapper", value, value)
	}
	if _, ok := wrapper[blobBase64MarkerKey]; !ok {
		t.Fatalf("wrapper = %v, want key %q", wrapper, blobBase64MarkerKey)
	}
}

// TestPlainModeEncoderBLOBColumnPrefixedBase64 asserts the plain
// encoder stamps the `<blob:base64>` prefix on a BLOB column so the
// line stays parseable (no replacement characters from raw bytes
// passed through fmt.Sprint). The payload after the prefix
// base64-decodes back to the original bytes.
func TestPlainModeEncoderBLOBColumnPrefixedBase64(t *testing.T) {
	encoder := newPlainModeEncoder()
	payload := []byte{0x00, 0xFF, 0x10, 0xAB, 0xCD, 0xEF, 0x01, 0x7F}
	value := encoder.encode("b", "BLOB", payload)
	got, ok := value.(string)
	if !ok {
		t.Fatalf("encoded value type = %T(%v), want string", value, value)
	}
	if !strings.HasPrefix(got, blobPlainPrefix) {
		t.Fatalf("encoded = %q, want prefix %q", got, blobPlainPrefix)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatalf("encoded = %q, contains replacement character", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, blobPlainPrefix))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded = %v, want %v", decoded, payload)
	}
}

// TestPlainModeEncoderBLOBColumnNULLStaysNull asserts a NULL BLOB
// column stays nil — the prefix is reserved for byte payloads.
func TestPlainModeEncoderBLOBColumnNULLStaysNull(t *testing.T) {
	encoder := newPlainModeEncoder()
	if got := encoder.encode("b", "BLOB", nil); got != nil {
		t.Fatalf("encode(NULL BLOB) = %T(%v), want nil", got, got)
	}
}

// TestJSONModeEncoderEndToEndQueryRandomBlobBase64RoundTrip is the
// vertical tracer-bullet test: run the binary against a real archive,
// project `randomblob(8)`, and assert the resulting cell carries an
// 8-byte base64 payload under the documented marker key.
func TestJSONModeEncoderEndToEndQueryRandomBlobBase64RoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	configPath, _, _ := initializeFileCredentialSetup(t, tempDir)

	stdout, stderr := captureQuery(t, configPath, "--json", "SELECT randomblob(8) AS b")
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var got struct {
		Rows [][]map[string]string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if len(got.Rows) != 1 || len(got.Rows[0]) != 1 {
		t.Fatalf("rows shape = %v, want one row with one column", got.Rows)
	}
	encoded, ok := got.Rows[0][0][blobBase64MarkerKey]
	if !ok {
		t.Fatalf("rows[0][0] = %v, want key %q", got.Rows[0][0], blobBase64MarkerKey)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v (payload=%q)", err, encoded)
	}
	if len(decoded) != 8 {
		t.Fatalf("decoded len = %d, want 8", len(decoded))
	}
}

// TestPlainModeEncoderEndToEndQueryRandomBlobPrefixedBase64 asserts
// the `--plain` path emits a single parseable line carrying the
// `<blob:base64>` prefix and an 8-byte base64 payload — no replacement
// characters, no embedded newlines.
func TestPlainModeEncoderEndToEndQueryRandomBlobPrefixedBase64(t *testing.T) {
	tempDir := t.TempDir()
	configPath, _, _ := initializeFileCredentialSetup(t, tempDir)

	stdout, stderr := captureQuery(t, configPath, "--plain", "SELECT randomblob(8) AS b")
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if strings.ContainsRune(stdout, '�') {
		t.Fatalf("stdout contains replacement character\nstdout: %s", stdout)
	}
	var line string
	for _, candidate := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(candidate, "row.1.1: ") {
			line = strings.TrimPrefix(candidate, "row.1.1: ")
			break
		}
	}
	if line == "" {
		t.Fatalf("stdout missing row.1.1 line\nstdout: %s", stdout)
	}
	if !strings.HasPrefix(line, blobPlainPrefix) {
		t.Fatalf("row.1.1 = %q, want prefix %q", line, blobPlainPrefix)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, blobPlainPrefix))
	if err != nil {
		t.Fatalf("base64 decode: %v (payload=%q)", err, line)
	}
	if len(decoded) != 8 {
		t.Fatalf("decoded len = %d, want 8", len(decoded))
	}
}
