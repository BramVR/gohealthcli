package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// This file is the one shared Provider GET module (issue #280,
// ADR-0007 "deepen the Provider HTTP path"). Every Identity Snapshot
// fetcher — paired devices, settings, IRN profile, identity, profile —
// is a thin call site over fetchProviderJSON: URL + per-fetch label in,
// validated JSON out. The module owns the bearer auth header, the
// response size limit, the shared timeout client (#271), and typed
// status errors carrying the per-fetch label (#272), so transport
// behavior cannot drift between Identity Snapshot fetches.

// providerGETResponseLimit bounds how much of an Identity Snapshot
// response body the module buffers — the historical 1 MiB cap every
// fetcher carried. Identity-level payloads are small; a body this
// large means a misbehaving Provider, and the truncated read fails the
// JSON validity check below instead of exhausting memory. (The raw
// Provider fetch keeps its own larger googleHealthRawResponseLimit —
// raw pages legitimately reach megabytes.)
const providerGETResponseLimit = 1 << 20

// fetchProviderJSON is the module's production entry point: one
// Provider GET against url, labeled per fetch for error messages.
func fetchProviderJSON(url, label, accessToken string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := providerHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, providerGETResponseLimit))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Typed so the translation layer can branch on the status code
		// via errors.As instead of message text (issue #272). The
		// per-fetch label keeps the historical message verbatim.
		return nil, &googleHealthHTTPError{
			StatusCode: response.StatusCode,
			endpoint:   label,
		}
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("Google Health %s response is not valid JSON", label)
	}
	return body, nil
}
