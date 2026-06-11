package main

import (
	"errors"
	"net/http"
	"net/url"
)

// This file is the one Provider error translation layer (issue #272,
// ADR-0007 "Provider error normalization"). Every Provider-touching
// command routes its upstream failure through these helpers so that:
//
//   - auth rejections are detected via errors.As on the typed
//     googleHealthHTTPError carrying the status code — never by
//     matching on error message text;
//   - the original cause chain stays reachable end-to-end (errors.As
//     keeps working on the returned error);
//   - non-auth Provider HTTP/network failures classify under the
//     documented provider_unreachable failure status, so JSON
//     consumers can distinguish a Provider outage from local
//     misconfiguration.

// isProviderUnreachableError reports whether err is a non-auth
// Provider HTTP or network failure — the provider_unreachable
// category. A typed upstream HTTP error counts unless it is the 401
// auth rejection (that is a Connection problem the user fixes with
// `connect`, not an outage); a *url.Error is net/http's transport-
// level failure shape (dial refused, DNS, TLS, deadline) and always
// counts.
func isProviderUnreachableError(err error) bool {
	var httpErr *googleHealthHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode != http.StatusUnauthorized
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// isUnauthorizedHTTPError reports whether err is an upstream HTTP 401.
// 401 is the only status that means "access token no longer valid";
// 403 is a scope/authorization problem a fresh token cannot fix.
func isUnauthorizedHTTPError(err error) bool {
	var httpErr *googleHealthHTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
}

// normalizeProviderError translates an upstream Provider failure into
// the user-facing error category every Provider-touching command
// shares. A typed HTTP 401 becomes the
// errCurrentConnectionProviderUnauthorized "run `gohealthcli connect`
// again" category with the original cause kept in the chain; every
// other error passes through unchanged. Detection is errors.As on the
// typed googleHealthHTTPError only — message text never participates.
func normalizeProviderError(err error) error {
	if err == nil {
		return nil
	}
	if isUnauthorizedHTTPError(err) {
		return &providerUnauthorizedError{cause: err}
	}
	return err
}

// providerUnauthorizedError is the normalized Provider auth rejection.
// Its message is the errCurrentConnectionProviderUnauthorized sentinel
// text verbatim (the JSON message contract predates issue #272), while
// Unwrap exposes BOTH the sentinel — so errors.Is keeps matching the
// category — and the typed cause, so errors.As can still reach the
// googleHealthHTTPError carrying the status code end-to-end.
type providerUnauthorizedError struct {
	cause error
}

func (err *providerUnauthorizedError) Error() string {
	return errCurrentConnectionProviderUnauthorized.Error()
}

func (err *providerUnauthorizedError) Unwrap() []error {
	return []error{errCurrentConnectionProviderUnauthorized, err.cause}
}
