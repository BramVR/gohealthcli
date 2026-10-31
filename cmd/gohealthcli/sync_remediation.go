package main

import (
	"errors"
	"os"
)

// archiveFailureRemediation attaches setup recovery only when the error
// chain proves the requested Health Archive does not exist. Corrupt,
// incompatible, and otherwise unknown archives deliberately stay actionless.
func archiveFailureRemediation(err error) error {
	if err == nil || len(remediationFromError(err)) != 0 {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		return withRemediation(err, remediationRunDoctor, remediationInitialize)
	}
	return err
}

// syncFailureRemediation composes the reviewed Connection recovery catalog
// with local archive setup recovery. Both classifiers use typed causes only;
// constructing the result performs no Provider I/O or OAuth work.
func syncFailureRemediation(err error) error {
	return archiveFailureRemediation(connectionFailureRemediation(err))
}
