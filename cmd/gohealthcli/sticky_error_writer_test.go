package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// writerFailingAfter accepts the first `successes` writes and rejects
// every later one, counting how many write attempts reach it.
type writerFailingAfter struct {
	successes int
	attempts  int
	failure   error
	buffer    bytes.Buffer
}

func (writer *writerFailingAfter) Write(payload []byte) (int, error) {
	writer.attempts++
	if writer.attempts > writer.successes {
		return 0, writer.failure
	}
	return writer.buffer.Write(payload)
}

// The sticky-error writer is the result writers' replacement for
// per-line write-error checks (#274): writers print unconditionally,
// the first write error latches, and the caller checks once at the end.

func TestStickyWriterForwardsPrintsToTheUnderlyingWriter(t *testing.T) {
	buffer := new(bytes.Buffer)
	writer := newStickyWriter(buffer)

	writer.Printf("status: %s\n", "ok")
	writer.Println("Health Archive status")

	if got, want := buffer.String(), "status: ok\nHealth Archive status\n"; got != want {
		t.Fatalf("underlying writer got %q, want %q", got, want)
	}
	if err := writer.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil after successful writes", err)
	}
}

func TestStickyWriterLatchesTheFirstWriteErrorAndStopsWriting(t *testing.T) {
	firstFailure := errors.New("broken pipe")
	underlying := &writerFailingAfter{successes: 1, failure: firstFailure}
	writer := newStickyWriter(underlying)

	writer.Printf("status: %s\n", "ok")
	writer.Printf("archive_path: %s\n", "/tmp/archive.sqlite")
	writer.Println("message: never attempted")

	if got := writer.Err(); !errors.Is(got, firstFailure) {
		t.Fatalf("Err() = %v, want the latched first write error %v", got, firstFailure)
	}
	if underlying.attempts != 2 {
		t.Fatalf("underlying write attempts = %d, want 2 (no writes after the first error)", underlying.attempts)
	}
	if got, want := underlying.buffer.String(), "status: ok\n"; got != want {
		t.Fatalf("underlying writer got %q, want only the pre-error bytes %q", got, want)
	}
}

// The sync --status human table renders through a tabwriter layered on
// the result writer, so the sticky writer must also latch errors that
// arrive through its io.Writer face.
func TestStickyWriterLatchesErrorsFromLayeredWriterFlushes(t *testing.T) {
	flushFailure := errors.New("disk full")
	underlying := &writerFailingAfter{successes: 0, failure: flushFailure}
	writer := newStickyWriter(underlying)

	if _, err := writer.Write([]byte("ID\tSTATUS\n")); !errors.Is(err, flushFailure) {
		t.Fatalf("Write error = %v, want %v surfaced to the layered writer", err, flushFailure)
	}
	writer.Printf("Message: %s\n", "never attempted")

	if got := writer.Err(); !errors.Is(got, flushFailure) {
		t.Fatalf("Err() = %v, want the latched flush error %v", got, flushFailure)
	}
	if underlying.attempts != 1 {
		t.Fatalf("underlying write attempts = %d, want 1 (prints stop after a layered-write error)", underlying.attempts)
	}
}

// shortWriter violates the io.Writer contract by reporting fewer bytes
// written than requested without an error.
type shortWriter struct{}

func (shortWriter) Write(payload []byte) (int, error) {
	return len(payload) - 1, nil
}

func TestStickyWriterLatchesShortWritesAsErrShortWrite(t *testing.T) {
	writer := newStickyWriter(shortWriter{})

	written, err := writer.Write([]byte("ID\tSTATUS\n"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Write error = %v, want io.ErrShortWrite for a truncated write", err)
	}
	if written != 9 {
		t.Fatalf("Write reported %d bytes, want the underlying writer's 9", written)
	}
	if got := writer.Err(); !errors.Is(got, io.ErrShortWrite) {
		t.Fatalf("Err() = %v, want the latched io.ErrShortWrite", got)
	}
}
