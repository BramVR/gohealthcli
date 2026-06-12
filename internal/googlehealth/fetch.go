package googlehealth

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

// RawRequest describes one raw Provider request: the endpoint-shaped
// descriptor the builders in this package produce and the
// fetchRawProvider seam carries. Fields are exported because the
// request crosses the package boundary: main's runtime adapters seam
// is typed over it, `raw` reads RequiredScopes for its scope check,
// and main's sync tests inspect URL / DataType / EndpointName on the
// requests their fake providers receive.
type RawRequest struct {
	EndpointName       string
	DataType           string
	Method             string
	URL                string
	Body               []byte
	RequiredScopes     []string
	SourceFamilyFilter string
}

func BuildRawRequest(target []string, from, to string, pageSize int64, pageToken string) (RawRequest, error) {
	if len(target) < 2 {
		return RawRequest{}, errors.New("requires `endpoint <name>` or `data-type <name>`")
	}
	switch target[0] {
	case "endpoint":
		if len(target) != 2 {
			return RawRequest{}, errors.New("endpoint mode requires exactly one endpoint name")
		}
		// Identity-style endpoints route through the catalog: URL
		// lookup comes from identityEndpointURLs, scopes
		// from identityEndpointScopes. PRD #142 slice 7
		// makes `raw endpoint <name>` and the matching introspection
		// command (`profile`, `settings`, `devices`, `irn-profile`)
		// share one source of truth, so a scope revision (slice 2) is
		// a one-row change.
		if endpointURL, ok := identityEndpointURLs[target[1]]; ok {
			requiredScopes, hasScopes := identityEndpointScopes[target[1]]
			if !hasScopes || len(requiredScopes) == 0 {
				return RawRequest{}, fmt.Errorf("internal: identity endpoint %q present in URL catalog but missing from scope catalog", target[1])
			}
			return RawRequest{
				EndpointName:   target[1],
				URL:            endpointURL,
				RequiredScopes: requiredScopes,
			}, nil
		}
		if strings.HasPrefix(target[1], "dataTypes.") && strings.HasSuffix(target[1], ".list") {
			dataType := strings.TrimSuffix(strings.TrimPrefix(target[1], "dataTypes."), ".list")
			return buildGoogleHealthDataTypeListRawRequest(dataType, from, to, pageSize, pageToken)
		}
		return RawRequest{}, fmt.Errorf("unsupported raw endpoint %q", target[1])
	case "data-type":
		if len(target) != 2 {
			return RawRequest{}, errors.New("data-type mode requires exactly one Data Type")
		}
		return buildGoogleHealthDataTypeListRawRequest(target[1], from, to, pageSize, pageToken)
	default:
		return RawRequest{}, fmt.Errorf("unsupported raw target %q", target[0])
	}
}

func buildGoogleHealthDataTypeListRawRequest(dataType, from, to string, pageSize int64, pageToken string) (RawRequest, error) {
	if err := validateRawGoogleHealthDataType(dataType); err != nil {
		return RawRequest{}, err
	}
	if from == "" {
		return RawRequest{}, errors.New("Data Type list raw calls require --from")
	}
	query := url.Values{}
	filter, err := googleHealthDataTypeListFilter(dataType, from, to)
	if err != nil {
		return RawRequest{}, err
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
	return RawRequest{
		EndpointName:   "dataTypes." + dataType + ".list",
		DataType:       dataType,
		Method:         http.MethodGet,
		URL:            requestURL,
		RequiredScopes: ScopesForDataType(dataType),
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

// FetchRaw is the single-attempt raw Provider fetch. The
// HTTP doer is injected (#281): production binds the shared timeout
// client via the fetchRawProvider seam and the runtime adapters; tests
// bind a fake doer to exercise this body directly. The request is
// scoped to ctx (#284), so canceling it aborts the in-flight call.
func FetchRaw(ctx context.Context, doer Doer, request RawRequest, accessToken string) ([]byte, error) {
	method := request.Method
	if method == "" {
		method = http.MethodGet
	}
	var requestBody io.Reader
	if len(request.Body) != 0 {
		requestBody = bytes.NewReader(request.Body)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, request.URL, requestBody)
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+accessToken)
	httpRequest.Header.Set("Accept", "application/json")
	if len(request.Body) != 0 {
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
		return nil, &HTTPError{
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

// HTTPError carries the upstream status code plus an optional
// Retry-After hint. The ingestion retry middleware uses these to decide
// whether to retry transient failures (429, 5xx) and how long to wait
// before doing so; the Provider error translation layer
// (errors.go) reads StatusCode via errors.As to
// detect auth rejections and provider_unreachable failures without
// matching on message text (issue #272). Other callers can still read
// the error string.
type HTTPError struct {
	StatusCode int
	RetryAfter time.Duration
	Body       []byte
	// Endpoint labels which Provider request failed ("identity",
	// "pairedDevices", ...) so each fetcher keeps its historical
	// user-facing message verbatim. Empty means the raw Provider fetch
	// path, whose message predates the label. Exported so main's tests
	// can fake labeled upstream failures.
	Endpoint string
}

func (err *HTTPError) Error() string {
	// Deliberately omit the response body — Google Health echoes the
	// bearer token in some error responses (covered by
	// TestFetchGoogleHealthRawUsesBearerAndHidesErrorBody). Callers that
	// need the body can read err.Body directly.
	label := err.Endpoint
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
