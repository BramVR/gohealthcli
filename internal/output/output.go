// Package output owns the shared stdout/stderr output contracts.
//
// The rule across all commands: machine-readable data goes to stdout, while
// human hints and warnings go to stderr. These helpers keep that split
// consistent and easy to test.
package output

import (
	"encoding/json"
	"fmt"
	"io"
)

// Mode selects how machine/human data is rendered to stdout.
type Mode int

const (
	// Human renders readable status lines for interactive use.
	Human Mode = iota
	// JSON renders a stable JSON document.
	JSON
	// Plain renders stable key/value lines.
	Plain
)

// Pair is a stable key/value line used by Plain mode.
type Pair struct {
	Key   string
	Value string
}

// WriteJSON writes v as indented JSON followed by a newline.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WritePlain writes stable "key: value" lines in order.
func WritePlain(w io.Writer, pairs []Pair) error {
	for _, p := range pairs {
		if _, err := fmt.Fprintf(w, "%s: %s\n", p.Key, p.Value); err != nil {
			return err
		}
	}
	return nil
}

// WriteHints writes human guidance lines to w (stderr). It is a no-op when
// there are no hints.
func WriteHints(w io.Writer, hints []string) {
	for _, h := range hints {
		fmt.Fprintln(w, h)
	}
}
