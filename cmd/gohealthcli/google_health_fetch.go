package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const googleHealthBaseURL = "https://health.googleapis.com/v4"

const googleHealthRawResponseLimit = 10 << 20

type rawProviderRequest struct {
	endpointName       string
	dataType           string
	method             string
	url                string
	body               []byte
	requiredScopes     []string
	sourceFamilyFilter string
}

func buildGoogleHealthRawRequest(target []string, from, to string, pageSize int64, pageToken string) (rawProviderRequest, error) {
	if len(target) < 2 {
		return rawProviderRequest{}, errors.New("requires `endpoint <name>` or `data-type <name>`")
	}
	switch target[0] {
	case "endpoint":
		if len(target) != 2 {
			return rawProviderRequest{}, errors.New("endpoint mode requires exactly one endpoint name")
		}
		// Identity-style endpoints route through the catalog: URL
		// lookup comes from googleHealthIdentityEndpointURLs, scopes
		// from googleHealthIdentityEndpointScopes. PRD #142 slice 7
		// makes `raw endpoint <name>` and the matching introspection
		// command (`profile`, `settings`, `devices`, `irn-profile`)
		// share one source of truth, so a scope revision (slice 2) is
		// a one-row change.
		if endpointURL, ok := googleHealthIdentityEndpointURLs[target[1]]; ok {
			requiredScopes, hasScopes := googleHealthIdentityEndpointScopes[target[1]]
			if !hasScopes || len(requiredScopes) == 0 {
				return rawProviderRequest{}, fmt.Errorf("internal: identity endpoint %q present in URL catalog but missing from scope catalog", target[1])
			}
			return rawProviderRequest{
				endpointName:   target[1],
				url:            endpointURL,
				requiredScopes: requiredScopes,
			}, nil
		}
		if strings.HasPrefix(target[1], "dataTypes.") && strings.HasSuffix(target[1], ".list") {
			dataType := strings.TrimSuffix(strings.TrimPrefix(target[1], "dataTypes."), ".list")
			return buildGoogleHealthDataTypeListRawRequest(dataType, from, to, pageSize, pageToken)
		}
		return rawProviderRequest{}, fmt.Errorf("unsupported raw endpoint %q", target[1])
	case "data-type":
		if len(target) != 2 {
			return rawProviderRequest{}, errors.New("data-type mode requires exactly one Data Type")
		}
		return buildGoogleHealthDataTypeListRawRequest(target[1], from, to, pageSize, pageToken)
	default:
		return rawProviderRequest{}, fmt.Errorf("unsupported raw target %q", target[0])
	}
}

func buildGoogleHealthDataTypeListRawRequest(dataType, from, to string, pageSize int64, pageToken string) (rawProviderRequest, error) {
	if err := validateRawGoogleHealthDataType(dataType); err != nil {
		return rawProviderRequest{}, err
	}
	if from == "" {
		return rawProviderRequest{}, errors.New("Data Type list raw calls require --from")
	}
	query := url.Values{}
	filter, err := googleHealthDataTypeListFilter(dataType, from, to)
	if err != nil {
		return rawProviderRequest{}, err
	}
	query.Set("filter", filter)
	if pageSize > 0 {
		query.Set("pageSize", strconv.FormatInt(pageSize, 10))
	}
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	requestURL := googleHealthBaseURL + "/users/me/dataTypes/" + url.PathEscape(dataType) + "/dataPoints"
	if encoded := query.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}
	return rawProviderRequest{
		endpointName:   "dataTypes." + dataType + ".list",
		dataType:       dataType,
		method:         http.MethodGet,
		url:            requestURL,
		requiredScopes: googleHealthScopesForDataType(dataType),
	}, nil
}

func validateRawGoogleHealthDataType(dataType string) error {
	if dataType == "" {
		return errors.New("Data Type must not be empty")
	}
	for _, char := range dataType {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return fmt.Errorf("Data Type %q must use kebab-case provider names", dataType)
	}
	return nil
}

func googleHealthDataTypeListFilter(dataType, from, to string) (string, error) {
	field, err := googleHealthDataTypeListFilterField(dataType)
	if err != nil {
		return "", err
	}
	filterFrom, err := googleHealthFilterValue(field, from)
	if err != nil {
		return "", fmt.Errorf("--from: %w", err)
	}
	clauses := []string{fmt.Sprintf("%s >= %s", field, filterFrom)}
	if to != "" {
		filterTo, err := googleHealthFilterValue(field, to)
		if err != nil {
			return "", fmt.Errorf("--to: %w", err)
		}
		clauses = append(clauses, fmt.Sprintf("%s < %s", field, filterTo))
	}
	return strings.Join(clauses, " AND "), nil
}

func googleHealthFilterValue(field, value string) (string, error) {
	if strings.HasSuffix(field, ".date") {
		if _, err := time.Parse("2006-01-02", value); err != nil {
			return "", errors.New("expected YYYY-MM-DD")
		}
		return strconv.Quote(value), nil
	}
	if strings.Contains(field, ".civil_") {
		if _, err := time.Parse("2006-01-02", value); err == nil {
			return strconv.Quote(value), nil
		}
		if _, err := time.Parse("2006-01-02T15:04:05", value); err == nil {
			return strconv.Quote(value), nil
		}
		return "", errors.New("expected YYYY-MM-DD or YYYY-MM-DDTHH:mm:ss")
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return strconv.Quote(parsed.UTC().Format(time.RFC3339Nano)), nil
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return strconv.Quote(parsed.UTC().Format("2006-01-02T00:00:00Z")), nil
	}
	return "", errors.New("expected YYYY-MM-DD or RFC3339")
}

// fetchGoogleHealthRaw is the single-attempt raw Provider fetch. The
// HTTP doer is injected (#281): production binds the shared timeout
// client via the fetchRawProvider seam and the runtime adapters; tests
// bind a fake doer to exercise this body directly. The request is
// scoped to ctx (#284), so canceling it aborts the in-flight call.
func fetchGoogleHealthRaw(ctx context.Context, doer httpDoer, request rawProviderRequest, accessToken string) ([]byte, error) {
	method := request.method
	if method == "" {
		method = http.MethodGet
	}
	var requestBody io.Reader
	if len(request.body) != 0 {
		requestBody = bytes.NewReader(request.body)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, request.url, requestBody)
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+accessToken)
	httpRequest.Header.Set("Accept", "application/json")
	if len(request.body) != 0 {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := doer.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, tooLarge, err := readLimitedBody(response.Body, googleHealthRawResponseLimit)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &googleHealthHTTPError{
			StatusCode: response.StatusCode,
			RetryAfter: parseRetryAfter(response.Header.Get("Retry-After")),
			Body:       body,
		}
	}
	if tooLarge {
		return nil, fmt.Errorf("Google Health raw response exceeds %d bytes; narrow the raw request", googleHealthRawResponseLimit)
	}
	return body, nil
}

// googleHealthHTTPError carries the upstream status code plus an optional
// Retry-After hint. The ingestion retry middleware uses these to decide
// whether to retry transient failures (429, 5xx) and how long to wait
// before doing so; the Provider error translation layer
// (provider_error_normalization.go) reads StatusCode via errors.As to
// detect auth rejections and provider_unreachable failures without
// matching on message text (issue #272). Other callers can still read
// the error string.
type googleHealthHTTPError struct {
	StatusCode int
	RetryAfter time.Duration
	Body       []byte
	// endpoint labels which Provider request failed ("identity",
	// "pairedDevices", ...) so each fetcher keeps its historical
	// user-facing message verbatim. Empty means the raw Provider fetch
	// path, whose message predates the label.
	endpoint string
}

func (err *googleHealthHTTPError) Error() string {
	// Deliberately omit the response body — Google Health echoes the
	// bearer token in some error responses (covered by
	// TestFetchGoogleHealthRawUsesBearerAndHidesErrorBody). Callers that
	// need the body can read err.Body directly.
	label := err.endpoint
	if label == "" {
		label = "raw"
	}
	return fmt.Sprintf("Google Health %s request failed with HTTP %d", label, err.StatusCode)
}

// parseRetryAfter parses the Retry-After header. RFC 7231 allows either
// an HTTP-date or a delta-seconds. We accept the delta-seconds form (the
// only form Google Health emits in practice) and ignore the date form so
// the middleware never blocks for hours on a malformed header.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

func readLimitedBody(reader io.Reader, limit int64) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > limit {
		return nil, true, nil
	}
	return body, false, nil
}
