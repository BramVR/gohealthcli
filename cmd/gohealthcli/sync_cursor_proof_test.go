package main

import "testing"

type syncInvariant string

const (
	syncFinalizeOutcome     syncInvariant = "FinalizeOutcome"
	syncAtomicRun           syncInvariant = "AtomicRun"
	syncCompletedOnly       syncInvariant = "CompletedOnly"
	syncTransactionReleased syncInvariant = "TransactionReleased"
	syncCursorHighwater     syncInvariant = "CursorHighwater"
)

type syncVerdict struct {
	violated syncInvariant
	detail   string
}

func requireHolds(t *testing.T, verdict syncVerdict) {
	t.Helper()
	if verdict.violated != "" {
		t.Errorf("%s: %q", verdict.violated, verdict.detail)
	}
}
