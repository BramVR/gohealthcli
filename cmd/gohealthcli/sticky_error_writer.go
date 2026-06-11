package main

import (
	"fmt"
	"io"
)

// stickyWriter wraps a result writer's io.Writer and latches the first
// write error (#274). Result writers print every field unconditionally
// through Printf/Println; once a write fails, later prints become
// no-ops, and the caller checks Err once at the end instead of wrapping
// every printed field in a three-line write-error check.
type stickyWriter struct {
	writer io.Writer
	err    error
}

func newStickyWriter(writer io.Writer) *stickyWriter {
	return &stickyWriter{writer: writer}
}

// Printf prints like fmt.Fprintf unless an earlier write already
// failed.
func (sticky *stickyWriter) Printf(format string, args ...any) {
	if sticky.err != nil {
		return
	}
	_, sticky.err = fmt.Fprintf(sticky.writer, format, args...)
}

// Println prints like fmt.Fprintln unless an earlier write already
// failed.
func (sticky *stickyWriter) Println(args ...any) {
	if sticky.err != nil {
		return
	}
	_, sticky.err = fmt.Fprintln(sticky.writer, args...)
}

// Write lets layered writers (the sync --status tabwriter) print
// through the same latch. The error is both latched and returned, so
// the layered writer can stop early and a later Err call still reports
// it once.
func (sticky *stickyWriter) Write(payload []byte) (int, error) {
	if sticky.err != nil {
		return 0, sticky.err
	}
	written, err := sticky.writer.Write(payload)
	if err != nil {
		sticky.err = err
	}
	return written, err
}

// Err reports the first write error, or nil when every print landed.
func (sticky *stickyWriter) Err() error {
	return sticky.err
}
