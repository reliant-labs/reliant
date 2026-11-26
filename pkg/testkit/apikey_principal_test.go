package testkit_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// A key caller built from options looks to a handler exactly as one forge's
// Authenticator admitted: the unforgeable key credential with its lookup
// prefix, no person, and the role and tenant the key row carries — whatever
// order the options come in.
func TestWithAPIKey_BuildsAKeyAuthenticatedPrincipal(t *testing.T) {
	t.Parallel()
	for name, opts := range map[string][]testkit.ClaimsOption{
		"key first":  {testkit.WithAPIKey("abcd1234"), testkit.WithRoles("dataplane"), testkit.WithOrgID("job-1")},
		"key last":   {testkit.WithRoles("dataplane"), testkit.WithOrgID("job-1"), testkit.WithAPIKey("abcd1234")},
		"key middle": {testkit.WithRoles("dataplane"), testkit.WithAPIKey("abcd1234"), testkit.WithOrgID("job-1")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			claims, ok := localClaimsFrom(testkit.AuthedContext(t, localWithClaims, opts...))
			if !ok {
				t.Fatal("no claims installed")
			}
			if !claims.IsAPIKey() || claims.Credential.KeyPrefix() != "abcd1234" {
				t.Fatalf("credential = %v (prefix %q), want an API key with prefix abcd1234", claims.Credential.Kind(), claims.Credential.KeyPrefix())
			}
			if claims.UserID != "apikey:abcd1234" || claims.Email != "" {
				t.Errorf("UserID=%q Email=%q, want apikey:abcd1234 and no email", claims.UserID, claims.Email)
			}
			if claims.Role != "dataplane" || claims.OrgID != "job-1" {
				t.Errorf("Role=%q OrgID=%q, want dataplane / job-1", claims.Role, claims.OrgID)
			}
		})
	}
}

// The default principal stays a person's token-less test identity: WithAPIKey
// is opt-in.
func TestWithAPIKey_DefaultPrincipalIsNotAKey(t *testing.T) {
	t.Parallel()
	claims, _ := localClaimsFrom(testkit.AuthedContext(t, localWithClaims))
	if claims.IsAPIKey() {
		t.Fatal("the default test principal must not be an API key")
	}
}
