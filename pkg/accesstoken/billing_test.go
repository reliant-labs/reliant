package accesstoken

import (
	"errors"
	"testing"
	"time"
)

func TestBillingReadIndependentAuthority(t *testing.T) {
	scope, ok := ParseScope("billing:read")
	if !ok || scope != ScopeBillingRead {
		t.Fatal("billing read missing from closed registry")
	}
	for _, other := range AllScopes {
		if other == ScopeBillingRead {
			continue
		}
		if SetOf(other).Permits(ScopeBillingRead) || SetOf(ScopeBillingRead).Permits(other) {
			t.Fatalf("billing authority leaked to/from %s", other)
		}
	}
	if RequiresActingUser(scope) {
		t.Fatal("org automation may read costs without impersonation")
	}
}

func TestBillingReadGrantBindingAndCeiling(t *testing.T) {
	for _, kind := range []ResourceKind{"", ResourceDaemon, ResourceEnvironment, ResourcePort, ResourceConnector} {
		t.Run(string(kind), func(t *testing.T) {
			g := Grant{OrgID: "org", Name: "cost reader", Scopes: SetOf(ScopeBillingRead), GrantorScopes: SetOf(ScopeBillingRead)}
			if kind != "" {
				g.Resource = &Resource{Kind: kind, ID: "resource"}
			}
			err := g.Validate(time.Now())
			if kind == "" || kind == ResourceDaemon {
				if err != nil {
					t.Fatal(err)
				}
				g.GrantorScopes = SetOf(ScopeObsRead, ScopeDeployWrite, ScopeTokenWrite)
				if !errors.Is(g.Validate(time.Now()), ErrEscalation) {
					t.Fatal("delegation exceeded billing grant ceiling")
				}
			} else if !errors.Is(err, ErrInvalidGrant) {
				t.Fatalf("unexpected bound grant result: %v", err)
			}
		})
	}
}
