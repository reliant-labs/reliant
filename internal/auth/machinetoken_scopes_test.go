package auth

import (
	"context"
	"errors"
	"testing"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
)

type fixedIntrospector struct{ p *fat.Principal }

func (f fixedIntrospector) Introspect(context.Context, string) (*fat.Principal, error) {
	return f.p, nil
}

func TestClaimsForAccessTokenAdmitsNarrowTokensButClaimsForAPITokenDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		scopes     []fat.Scope
		user       string
		wantAccess bool
		wantAPI    bool
	}{
		{"reliant:api", []fat.Scope{fat.ScopeReliantAPI}, "u1", true, true},
		{"runs:start", []fat.Scope{fat.ScopeRunsStart}, "u1", true, false},
		{"triggers:manage", []fat.Scope{fat.ScopeTriggersManage}, "u1", true, false},
		{"storage read is not product API authority", []fat.Scope{fat.ScopeStorageRead}, "u1", false, false},
		{"config read is not product API authority", []fat.Scope{fat.ScopeDeployConfigRead}, "u1", false, false},
		{"narrow with reliant:api", []fat.Scope{fat.ScopeRunsRead, fat.ScopeReliantAPI}, "u1", true, true},
		{"inbox:read", []fat.Scope{fat.ScopeInboxRead}, "u1", true, false},
		{"inbox:manage", []fat.Scope{fat.ScopeInboxManage}, "u1", true, false},
		{"questions:answer", []fat.Scope{fat.ScopeQuestionsAnswer}, "u1", true, false},
		{"daemon credential", []fat.Scope{fat.ScopeDaemonConnect}, "u1", false, false},
		{"connector credential", []fat.Scope{fat.ScopeMCPConnector}, "u1", false, false},
		{"no acting user", []fat.Scope{fat.ScopeReliantAPI}, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixedIntrospector{&fat.Principal{ActingUserID: tc.user, Scopes: fat.SetOf(tc.scopes...)}}
			_, _, err := ClaimsForAccessToken(context.Background(), in, "rlat_x")
			if (err == nil) != tc.wantAccess {
				t.Errorf("ClaimsForAccessToken err = %v, want admitted=%v", err, tc.wantAccess)
			}
			if err != nil && !errors.Is(err, ErrAccessTokenNotAPI) {
				t.Errorf("refusal = %v, want ErrAccessTokenNotAPI", err)
			}
			_, _, err = ClaimsForAPIToken(context.Background(), in, "rlat_x")
			if (err == nil) != tc.wantAPI {
				t.Errorf("ClaimsForAPIToken err = %v, want admitted=%v", err, tc.wantAPI)
			}
		})
	}
}
