package googlehealth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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

// RawRequestOptions carries the complete raw request contract, including the
// flag provenance needed to reject range options on identity endpoints.
type RawRequestOptions struct {
	Target               []string
	ID                   string
	IDProvided           bool
	From                 string
	To                   string
	Timezone             string
	FromProvided         bool
	ToProvided           bool
	TimezoneProvided     bool
	ResolvedAt           time.Time
	PageSize             int64
	PageToken            string
	PageSizeProvided     bool
	PageTokenProvided    bool
	SourceFamily         string
	SourceFamilyProvided bool
	// TimezoneFallback resolves the command's non-secret configured timezone.
	// The Provider calls it only for a Data Type target with no explicit
	// timezone. Identity targets and explicit timezones never invoke it.
	TimezoneFallback func() (string, error)
}

// RawRequestDescription is the complete secret-free description shared by
// raw execution and planning. Request is the production request descriptor;
// Headers contains only headers that do not depend on credentials.
type RawRequestDescription struct {
	Request           RawRequest
	Range             *ResolvedRange
	PageSize          int64
	PageTokenProvided bool
	Headers           map[string]string
	SanitizedURL      string
}

// RawTargetKind is the first positional discriminator accepted by
// BuildRawRequest.
type RawTargetKind string

const (
	RawTargetEndpoint RawTargetKind = "endpoint"
	RawTargetDataType RawTargetKind = "data-type"
)

var rawTargetKinds = []RawTargetKind{RawTargetDataType, RawTargetEndpoint}

// RawTargetNames returns the sorted first positional values accepted by
// BuildRawRequest.
func RawTargetNames() []string {
	targets := make([]string, 0, len(rawTargetKinds))
	for _, target := range rawTargetKinds {
		targets = append(targets, string(target))
	}
	sort.Strings(targets)
	return targets
}

type rawRequestTarget struct {
	endpointName   string
	endpointURL    string
	requiredScopes []string
	dataType       string
	rangeTarget    RangeTarget
	lowerBoundOnly bool
	family         endpointFamily
}

// ValidateRawRequestOptions performs target/flag validation that must happen
// before raw inspects config or opens the Health Archive.
func ValidateRawRequestOptions(options RawRequestOptions) error {
	target, err := parseRawRequestTarget(options)
	if err != nil {
		return err
	}
	if target.family == endpointFamilyList && options.ToProvided {
		support, supportErr := googleHealthDataTypeFilterSupport(target.dataType, endpointFamilyList)
		if supportErr != nil {
			return supportErr
		}
		if support.LowerBoundOnly {
			return fmt.Errorf("raw %s supports only --from because the Provider exposes no ECG upper-bound filter", target.dataType)
		}
	}
	if target.family == endpointFamilyGet {
		if !options.IDProvided {
			return fmt.Errorf("raw data-type %s get requires --id", target.dataType)
		}
		if options.ID == "" {
			return errors.New("--id requires a non-empty Provider ID")
		}
		for _, provided := range []struct {
			name string
			set  bool
		}{
			{name: "--from", set: options.FromProvided},
			{name: "--to", set: options.ToProvided},
			{name: "--timezone", set: options.TimezoneProvided},
			{name: "--page-size", set: options.PageSizeProvided},
			{name: "--page-token", set: options.PageTokenProvided},
		} {
			if provided.set {
				return fmt.Errorf("raw data-type %s get does not support %s", target.dataType, provided.name)
			}
		}
	} else if options.IDProvided {
		return errors.New("--id is supported only by raw data-type <data-type> get")
	}
	if target.family == endpointFamilyReconcile {
		if options.From == "" {
			return fmt.Errorf("raw data-type %s reconcile requires --from", target.dataType)
		}
		if options.SourceFamily == "" {
			if !options.SourceFamilyProvided {
				return fmt.Errorf("raw data-type %s reconcile requires --source-family", target.dataType)
			}
			return errors.New("--source-family requires a non-empty source family")
		}
		if _, ok := sourceFamilyCatalog[options.SourceFamily]; !ok {
			return fmt.Errorf("raw --source-family currently supports only %s", supportedSourceFamilyList())
		}
	} else if options.SourceFamilyProvided {
		return errors.New("--source-family is supported only by raw data-type <data-type> reconcile")
	}
	if target.family == "" {
		for _, provided := range []struct {
			name string
			set  bool
		}{
			{name: "--page-size", set: options.PageSizeProvided},
			{name: "--page-token", set: options.PageTokenProvided},
		} {
			if provided.set {
				return fmt.Errorf("raw endpoint %s does not support %s", target.endpointName, provided.name)
			}
		}
	}
	return nil
}

func BuildRawRequest(options RawRequestOptions) (RawRequest, error) {
	description, err := DescribeRawRequest(options)
	if err != nil {
		return RawRequest{}, err
	}
	return description.Request, nil
}

// DescribeRawRequest builds the same request returned by BuildRawRequest and
// adds the resolved, non-secret facts needed by raw --plan.
func DescribeRawRequest(options RawRequestOptions) (RawRequestDescription, error) {
	target, err := parseRawRequestTarget(options)
	if err != nil {
		return RawRequestDescription{}, err
	}
	var request RawRequest
	var resolvedRange *ResolvedRange
	pageSize := options.PageSize
	switch target.family {
	case "":
		request = RawRequest{
			EndpointName:   target.endpointName,
			Method:         http.MethodGet,
			URL:            target.endpointURL,
			RequiredScopes: target.requiredScopes,
		}
	case endpointFamilyGet:
		request, err = buildGoogleHealthDataPointGetRawRequest(target.dataType, options.ID)
		if err != nil {
			return RawRequestDescription{}, err
		}
	default:
		if options.ResolvedAt.IsZero() {
			return RawRequestDescription{}, errors.New("raw Data Type range resolution requires a captured clock")
		}
		timezone := options.Timezone
		if timezone == "" && options.TimezoneFallback != nil {
			timezone, err = options.TimezoneFallback()
			if err != nil {
				return RawRequestDescription{}, err
			}
		}
		resolved, resolveErr := ResolveRawRange(options.From, options.To, timezone, options.ResolvedAt, target.rangeTarget)
		if resolveErr != nil {
			return RawRequestDescription{}, resolveErr
		}
		if target.family == endpointFamilyReconcile && pageSize == 0 {
			pageSize = dataPointReadPageSize(target.dataType)
		}
		request, err = buildGoogleHealthDataPointReadRawRequest(target.dataType, resolved.From, resolved.To, options.SourceFamily, pageSize, options.PageToken)
		if err != nil {
			return RawRequestDescription{}, err
		}
		// The shared resolver still captures the default upper boundary, but
		// lower-bound-only Provider filters do not apply it. The description
		// reports the range the production request actually carries.
		if target.lowerBoundOnly {
			resolved.To = ""
			resolved.ToInstant = time.Time{}
			resolved.ToNamed = false
		}
		resolvedRange = &resolved
	}
	sanitizedURL, err := sanitizeRawRequestURL(request)
	if err != nil {
		return RawRequestDescription{}, err
	}
	return RawRequestDescription{
		Request:           request,
		Range:             resolvedRange,
		PageSize:          pageSize,
		PageTokenProvided: options.PageTokenProvided,
		Headers:           rawRequestHeaders(request),
		SanitizedURL:      sanitizedURL,
	}, nil
}

// sanitizeRawRequestURL removes sensitive opaque inputs from a
// production-built request URL while preserving its non-sensitive shape.
func sanitizeRawRequestURL(request RawRequest) (string, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return "", fmt.Errorf("sanitize raw request URL: %w", err)
	}
	if request.DataType != "" && strings.HasSuffix(request.EndpointName, ".get") {
		escapedPath := parsed.EscapedPath()
		lastSlash := strings.LastIndex(escapedPath, "/")
		if lastSlash < 0 {
			return "", errors.New("sanitize raw request URL: get request path has no Data Point ID segment")
		}
		escapedPath = escapedPath[:lastSlash+1] + "REDACTED"
		parsed.Path, err = url.PathUnescape(escapedPath)
		if err != nil {
			return "", fmt.Errorf("sanitize raw request URL path: %w", err)
		}
		parsed.RawPath = escapedPath
	}
	query := parsed.Query()
	if query.Has("pageToken") {
		query.Set("pageToken", "REDACTED")
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}

func rawRequestHeaders(request RawRequest) map[string]string {
	headers := map[string]string{"Accept": "application/json"}
	if len(request.Body) != 0 {
		headers["Content-Type"] = "application/json"
	}
	return headers
}

func parseRawRequestTarget(options RawRequestOptions) (rawRequestTarget, error) {
	target := options.Target
	if len(target) < 2 {
		return rawRequestTarget{}, errors.New("requires `endpoint <name>` or `data-type <name>`")
	}
	switch RawTargetKind(target[0]) {
	case RawTargetEndpoint:
		if len(target) != 2 {
			return rawRequestTarget{}, errors.New("endpoint mode requires exactly one endpoint name")
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
				return rawRequestTarget{}, fmt.Errorf("internal: identity endpoint %q present in URL catalog but missing from scope catalog", target[1])
			}
			for _, provided := range []struct {
				name string
				set  bool
			}{
				{name: "--from", set: options.FromProvided},
				{name: "--to", set: options.ToProvided},
				{name: "--timezone", set: options.TimezoneProvided},
			} {
				if provided.set {
					return rawRequestTarget{}, fmt.Errorf("raw endpoint %s does not support %s", target[1], provided.name)
				}
			}
			return rawRequestTarget{endpointName: target[1], endpointURL: endpointURL, requiredScopes: requiredScopes}, nil
		}
		if strings.HasPrefix(target[1], "dataTypes.") && strings.HasSuffix(target[1], ".list") {
			dataType := strings.TrimSuffix(strings.TrimPrefix(target[1], "dataTypes."), ".list")
			return parseRawDataTypeTarget(dataType, endpointFamilyList)
		}
		return rawRequestTarget{}, fmt.Errorf("unsupported raw endpoint %q", target[1])
	case RawTargetDataType:
		if len(target) == 2 {
			return parseRawDataTypeTarget(target[1], endpointFamilyList)
		}
		if len(target) == 3 && target[2] == "get" {
			return parseRawDataTypeTarget(target[1], endpointFamilyGet)
		}
		if len(target) == 3 && target[2] == "reconcile" {
			return parseRawDataTypeTarget(target[1], endpointFamilyReconcile)
		}
		return rawRequestTarget{}, errors.New("data-type mode requires a Data Type and optional get or reconcile operation")
	default:
		return rawRequestTarget{}, fmt.Errorf("unsupported raw target %q", target[0])
	}
}

func parseRawDataTypeTarget(dataType string, family endpointFamily) (rawRequestTarget, error) {
	if family == endpointFamilyGet {
		if _, err := googleHealthDataTypeEndpointSupport(dataType, endpointFamilyGet); err != nil {
			return rawRequestTarget{}, err
		}
		return rawRequestTarget{dataType: dataType, family: endpointFamilyGet}, nil
	}
	reconcile := family == endpointFamilyReconcile
	rangeTarget, err := SyncRangeTarget(dataType, nil, reconcile)
	if err != nil {
		return rawRequestTarget{}, err
	}
	support, err := googleHealthDataTypeFilterSupport(dataType, family)
	if err != nil {
		return rawRequestTarget{}, err
	}
	return rawRequestTarget{dataType: dataType, rangeTarget: rangeTarget, lowerBoundOnly: support.LowerBoundOnly, family: family}, nil
}

func buildGoogleHealthDataPointGetRawRequest(dataType, providerID string) (RawRequest, error) {
	if err := validateRawGoogleHealthDataType(dataType); err != nil {
		return RawRequest{}, err
	}
	if providerID == "" {
		return RawRequest{}, errors.New("Provider ID must not be empty")
	}
	if _, err := googleHealthDataTypeEndpointSupport(dataType, endpointFamilyGet); err != nil {
		return RawRequest{}, err
	}
	return RawRequest{
		EndpointName:   "dataTypes." + dataType + ".get",
		DataType:       dataType,
		Method:         http.MethodGet,
		URL:            googleHealthBaseURL + "/users/me/dataTypes/" + url.PathEscape(dataType) + "/dataPoints/" + url.PathEscape(providerID),
		RequiredScopes: ScopesForDataType(dataType),
	}, nil
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
	return googleHealthDataTypeFilter(dataType, endpointFamilyList, from, to)
}

func googleHealthDataTypeFilter(dataType string, family endpointFamily, from, to string) (string, error) {
	support, err := googleHealthDataTypeFilterSupport(dataType, family)
	if err != nil {
		return "", err
	}
	field := support.FilterField
	filterFrom, err := googleHealthFilterValue(field, from)
	if err != nil {
		return "", fmt.Errorf("--from: %w", err)
	}
	clauses := []string{fmt.Sprintf("%s >= %s", field, filterFrom)}
	if to != "" && !support.LowerBoundOnly {
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
	for name, value := range rawRequestHeaders(request) {
		httpRequest.Header.Set(name, value)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+accessToken)
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
