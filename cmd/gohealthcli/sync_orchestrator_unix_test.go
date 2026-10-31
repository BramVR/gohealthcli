//go:build unix

package main

import (
	"syscall"
	"testing"
	"time"
)

func TestInstallSyncCancelContextCancelsOnSIGINT(t *testing.T) {
	// NOT t.Parallel(): this test SIGINTs the whole test process.
	// Any concurrently-running test that has a signal.NotifyContext
	// installed — every `sync` / `raw` dispatch — would observe the
	// signal and flake as sync_canceled.
	ctx, stop := installSyncCancelContext()
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT to self: %v", err)
	}

	select {
	case <-ctx.Done():
		// expected — signal handler canceled the context
	case <-time.After(2 * time.Second):
		t.Fatal("context did not cancel within 2s after SIGINT")
	}
}
