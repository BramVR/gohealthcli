package main

import (
	"database/sql"
	"errors"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func setupFailureRemediation(cause error, message string) error {
	return withRemediationMessage(cause, message, remediationRunDoctor, remediationInitialize)
}

func missingConnectionRemediation(cause error) error {
	return withRemediationMessage(cause, "no Connection found; run `gohealthcli connect` first", remediationRunDoctor, remediationReconnect)
}

func connectionFailureRemediation(err error) error {
	if err == nil || len(remediationFromError(err)) != 0 {
		return err
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return missingConnectionRemediation(err)
	case isCurrentConnectionTokenMissing(err), errors.Is(err, errCurrentConnectionTokenExpired), errors.Is(err, googlehealth.ErrUnauthorized):
		return withRemediation(err, remediationDoctorOnline, remediationReconnect)
	case isCurrentConnectionIdentityMismatch(err), errors.Is(err, errHealthArchiveIdentityMismatch):
		return withRemediation(err, remediationDoctorOnline, remediationInitHelp)
	default:
		return err
	}
}
