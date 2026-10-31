package googlehealth

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestBuildRawDataPointGetRequestForEveryCatalogSupportedDataType(t *testing.T) {
	t.Parallel()

	wantDataTypes := []string{
		"blood-glucose",
		"body-fat",
		"core-body-temperature",
		"exercise",
		"height",
		"hydration-log",
		"nutrition-log",
		"sleep",
		"weight",
	}
	if got := GettableDataTypes(); !slices.Equal(got, wantDataTypes) {
		t.Fatalf("GettableDataTypes = %v, want %v", got, wantDataTypes)
	}

	const providerID = "synthetic/id?#% value"
	for _, dataType := range wantDataTypes {
		dataType := dataType
		t.Run(dataType, func(t *testing.T) {
			t.Parallel()
			request, err := BuildRawRequest(RawRequestOptions{
				Target:     []string{"data-type", dataType, "get"},
				ID:         providerID,
				IDProvided: true,
			})
			if err != nil {
				t.Fatalf("BuildRawRequest: %v", err)
			}
			if request.EndpointName != "dataTypes."+dataType+".get" {
				t.Fatalf("EndpointName = %q", request.EndpointName)
			}
			if request.Method != http.MethodGet || len(request.Body) != 0 {
				t.Fatalf("request = %+v, want bodyless GET", request)
			}
			wantURL := googleHealthBaseURL + "/users/me/dataTypes/" + dataType + "/dataPoints/synthetic%2Fid%3F%23%25%20value"
			if request.URL != wantURL {
				t.Fatalf("URL = %q, want %q", request.URL, wantURL)
			}
			if strings.Contains(request.URL, providerID) {
				t.Fatalf("URL contains unescaped Provider ID: %q", request.URL)
			}
			if !slices.Equal(request.RequiredScopes, ScopesForDataType(dataType)) {
				t.Fatalf("RequiredScopes = %v, want catalog scopes %v", request.RequiredScopes, ScopesForDataType(dataType))
			}
			description, err := DescribeRawRequest(RawRequestOptions{
				Target:     []string{"data-type", dataType, "get"},
				ID:         providerID,
				IDProvided: true,
			})
			if err != nil {
				t.Fatalf("DescribeRawRequest: %v", err)
			}
			wantSanitizedURL := googleHealthBaseURL + "/users/me/dataTypes/" + dataType + "/dataPoints/REDACTED"
			if description.SanitizedURL != wantSanitizedURL {
				t.Fatalf("SanitizedURL = %q, want %q", description.SanitizedURL, wantSanitizedURL)
			}
			if strings.Contains(description.SanitizedURL, "synthetic") {
				t.Fatalf("SanitizedURL exposes Provider ID: %q", description.SanitizedURL)
			}
		})
	}
}

func TestCatalogDescriptionProjectsDataPointGetOperation(t *testing.T) {
	t.Parallel()

	description, err := CatalogDataTypeDescription("weight")
	if err != nil {
		t.Fatalf("CatalogDataTypeDescription: %v", err)
	}
	for _, endpoint := range description.Compiled.EndpointFamilies {
		if endpoint.Name != "get" {
			continue
		}
		if endpoint.RangeShape != "none" || endpoint.PagePolicy.Pagination != "none" || endpoint.PagePolicy.PageSizePolicy != "not_applicable" {
			t.Fatalf("get endpoint = %+v, want no range or pagination", endpoint)
		}
		return
	}
	t.Fatal("weight catalog description omitted get endpoint family")
}
