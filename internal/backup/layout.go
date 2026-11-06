package backup

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A logical table is stored as one encrypted file, data/<table>.jsonl.gz.age,
// when its compressed JSONL fits under the part target. A larger table is
// split at row boundaries into data/<table>.part-NNNN.jsonl.gz.age files, so
// no Git blob reaches the 100 MB per-file limit GitHub enforces.
const (
	shardPathSuffix = ".jsonl.gz.age"
	shardPartInfix  = ".part-"
	shardPartDigits = 4
	maxShardParts   = 9999
)

type shardLimits struct {
	partTargetBytes int
	maxBlobBytes    int64
}

var productionShardLimits = shardLimits{partTargetBytes: 50_000_000, maxBlobBytes: 100_000_000}

type jsonlPart struct {
	Rows  int
	JSONL []byte
	Gzip  []byte
}

func singleShardPath(table string) string {
	return "data/" + table + shardPathSuffix
}

func physicalShardPath(table string, part, parts int) string {
	if parts == 1 {
		return singleShardPath(table)
	}
	return fmt.Sprintf("data/%s%s%0*d%s", table, shardPartInfix, shardPartDigits, part, shardPathSuffix)
}

// parseShardPath returns the table a generated shard path belongs to and its
// 1-based part number, or part 0 for the single-file layout.
func parseShardPath(shardPath string) (string, int, bool) {
	name, hasPrefix := strings.CutPrefix(shardPath, "data/")
	name, hasSuffix := strings.CutSuffix(name, shardPathSuffix)
	if !hasPrefix || !hasSuffix {
		return "", 0, false
	}
	table, partText, split := strings.Cut(name, shardPartInfix)
	if !isKnownGeneratedShardTable(table) {
		return "", 0, false
	}
	if !split {
		return table, 0, true
	}
	if len(partText) != shardPartDigits || strings.Trim(partText, "0123456789") != "" {
		return "", 0, false
	}
	part, err := strconv.Atoi(partText)
	if err != nil || part < 1 {
		return "", 0, false
	}
	return table, part, true
}

// shardLayoutRows checks that every table in the manifest is either one
// single-file shard or parts 1..n (n >= 2) in path order, and returns the total
// rows declared per table. Callers check canonical path order themselves.
func shardLayoutRows(manifest Manifest) (map[string]int, error) {
	rows := make(map[string]int)
	lastPart := make(map[string]int)
	for _, shard := range manifest.Shards {
		table, part, ok := parseShardPath(shard.Path)
		if !ok || table != shard.Table {
			return nil, fmt.Errorf("backup shard %q has invalid path %q", shard.Table, shard.Path)
		}
		previous, seen := lastPart[table]
		if !seen && part > 1 || seen && (previous == 0 || part != previous+1) {
			return nil, fmt.Errorf("backup shard %q parts are not consecutive at %q", table, shard.Path)
		}
		lastPart[table] = part
		rows[table] += shard.Rows
	}
	for table, last := range lastPart {
		if last == 1 {
			return nil, fmt.Errorf("backup shard %q has a single part file; want %q", table, singleShardPath(table))
		}
	}
	return rows, nil
}

// splitJSONLParts gzips JSONL rows into consecutive parts, starting a new part
// once the compressed output of the current one reaches partTargetBytes.
// The cut depends only on the plaintext, so unchanged rows yield unchanged
// parts and their ciphertext can be reused. A single row larger than the
// target still forms one part; the caller enforces the hard blob limit.
func splitJSONLParts(jsonl []byte, partTargetBytes int) ([]jsonlPart, error) {
	var parts []jsonlPart
	compressed := new(bytes.Buffer)
	writer := newShardGzipWriter(compressed)
	start, rows := 0, 0
	finish := func(end int) error {
		if err := writer.Close(); err != nil {
			return err
		}
		parts = append(parts, jsonlPart{Rows: rows, JSONL: jsonl[start:end], Gzip: compressed.Bytes()})
		compressed = new(bytes.Buffer)
		writer = newShardGzipWriter(compressed)
		start, rows = end, 0
		return nil
	}
	for offset := 0; offset < len(jsonl); {
		if rows > 0 && compressed.Len() >= partTargetBytes {
			if err := finish(offset); err != nil {
				return nil, err
			}
		}
		end := offset + bytes.IndexByte(jsonl[offset:], '\n') + 1
		if _, err := writer.Write(jsonl[offset:end]); err != nil {
			return nil, err
		}
		rows++
		offset = end
	}
	if err := finish(len(jsonl)); err != nil {
		return nil, err
	}
	return parts, nil
}

func newShardGzipWriter(compressed *bytes.Buffer) *gzip.Writer {
	writer := gzip.NewWriter(compressed)
	writer.ModTime = time.Unix(0, 0).UTC()
	return writer
}
