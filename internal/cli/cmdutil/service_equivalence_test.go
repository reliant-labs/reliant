package cmdutil

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/naming"
)

func TestServiceDirectoryEquivalentSpellings(t *testing.T) {
	for _, tc := range []struct{ service, dir string }{
		{"ApiKeyService", "api_key"}, {"ApiKeyService", "apikey"},
		{"HttpProxyService", "http_proxy"}, {"UrlShortenerService", "url_shortener"},
		{"OauthClientService", "oauth_client"}, {"WorkOrderService", "workorder"},
	} {
		if err := ValidateServiceDirConsistency(tc.service, tc.dir); err != nil {
			t.Errorf("%s/%s: %v", tc.service, tc.dir, err)
		}
	}
}

func TestServiceDirectoryMismatchHintsChangeState(t *testing.T) {
	err := ValidateServiceDirConsistency("ApiKeyService", "orders")
	if err == nil {
		t.Fatal("unrelated directory accepted")
	}
	for _, noOp := range []string{`rename the proto service to "ApiKeyService"`, `rename the directory to "orders"`} {
		if strings.Contains(err.Error(), noOp) {
			t.Errorf("no-op hint: %v", err)
		}
	}
}

// The scaffolder writes ToPascalCase(dir)+"Service" into a fresh proto; a name
// that merely starts with an initialism must survive that round trip.
func TestScaffoldedServiceNameMatchesDirectory(t *testing.T) {
	for _, dir := range []string{"identity", "slack", "ramp", "idle", "uint_store", "api_key", "user_ids"} {
		if err := ValidateServiceDirConsistency(naming.ToPascalCase(dir)+"Service", dir); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
	if got := naming.ToPascalCase("identity") + "Service"; got != "IdentityService" {
		t.Errorf("identity scaffolds %q, want IdentityService", got)
	}
}

func TestServiceDirectoryMismatchHintNeverNamesCurrentState(t *testing.T) {
	for _, tc := range []struct{ service, dir string }{
		{"ApiKeyService", "orders"}, {"IdentityService", "access"}, {"OrdersService", "api_key"},
	} {
		err := ValidateServiceDirConsistency(tc.service, tc.dir)
		if err == nil {
			t.Fatalf("%s/%s accepted", tc.service, tc.dir)
		}
		for _, noOp := range []string{`rename the proto service to "` + tc.service + `"`, `rename the directory to "` + tc.dir + `"`} {
			if strings.Contains(err.Error(), noOp) {
				t.Errorf("%s/%s: no-op hint %q in %v", tc.service, tc.dir, noOp, err)
			}
		}
	}
}
