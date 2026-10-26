package googlehealth

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRawUnsupportedEndpointErrorPreservesName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"getIdentty", "", "dataTypes.bogus.list", "dataTypes..list", "dataTypes.total-calories.list"} {
		t.Run(name, func(t *testing.T) {
			err := ValidateRawRequestOptions(RawRequestOptions{Target: []string{"endpoint", name}})
			var unsupported *UnsupportedRawEndpointError
			if !errors.As(err, &unsupported) || unsupported.Name != name {
				t.Fatalf("error=%v, want UnsupportedRawEndpointError with name %q", err, name)
			}
			if want := fmt.Sprintf("unsupported raw endpoint %q", name); err.Error() != want {
				t.Fatalf("error=%q, want %q", err, want)
			}
		})
	}
}

func TestRawEndpointNamesPassCatalogValidation(t *testing.T) {
	t.Parallel()
	for _, name := range RawEndpointNames() {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRawRequestOptions(RawRequestOptions{Target: []string{"endpoint", name}}); err != nil {
				t.Fatalf("advertised endpoint %q rejected: %v", name, err)
			}
		})
	}
}

func TestRawNonEndpointErrorsKeepOriginalContracts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		target []string
		want   string
	}{
		{target: []string{"data-type", "bogus"}, want: `raw Data Type "bogus" is not in the catalog`},
		{target: []string{"data-type", "total-calories"}, want: `raw Data Type "total-calories" is not supported by dataPoints.list`},
		{target: []string{"endpoint", "getIdentity", "extra"}, want: "endpoint mode requires exactly one endpoint name"},
		{target: []string{"endpoint"}, want: "requires `endpoint <name>` or `data-type <name>`"},
	} {
		t.Run(strings.Join(test.target, "_"), func(t *testing.T) {
			err := ValidateRawRequestOptions(RawRequestOptions{Target: test.target})
			var unsupported *UnsupportedRawEndpointError
			if err == nil || err.Error() != test.want || errors.As(err, &unsupported) {
				t.Fatalf("error=%v, want original error %q", err, test.want)
			}
		})
	}
}
