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
