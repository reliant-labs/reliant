package launch

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
)

func TestChatOrg(t *testing.T) {
	org := func(s string) *string { return &s }
	cases := map[string]struct {
		spec    Spec
		project *db.Project
		want    string // "" is no org
		refused bool
	}{
		"the project's org decides":             {spec: Spec{DefaultOrgID: "home"}, project: &db.Project{OrgID: org("acme")}, want: "acme"},
		"naming the project's org":              {spec: Spec{OrgID: "acme"}, project: &db.Project{OrgID: org("acme")}, want: "acme"},
		"naming another org than the project's": {spec: Spec{OrgID: "home"}, project: &db.Project{OrgID: org("acme")}, refused: true},
		"a project with no org: the named org":  {spec: Spec{OrgID: "acme", DefaultOrgID: "home"}, project: &db.Project{}, want: "acme"},
		"nothing named: the default org":        {spec: Spec{DefaultOrgID: "home"}, project: &db.Project{}, want: "home"},
		"no orgs at all":                        {spec: Spec{}, project: &db.Project{}, want: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := chatOrg(tc.spec, tc.project)
			if tc.refused {
				if svcerr.Code(err) != connect.CodeFailedPrecondition {
					t.Fatalf("err = %v, want FailedPrecondition", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (tc.want == "") || (got != nil && *got != tc.want) {
				t.Fatalf("org = %v, want %q", got, tc.want)
			}
		})
	}
}

// orgSource: alice is in home only (removed from acme); home is her default.
type orgSource struct{}

func (orgSource) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: userID == "alice" && orgID == "home"}, nil
}

// A new chat that inherits its project's org needs live authority in it:
// owning the project is not membership, and a request naming no org does not
// skip the check.
func TestAuthorizeInheritedOrg(t *testing.T) {
	l := &Launcher{orgs: orgs.New(orgSource{})}
	acme, home := "acme", "home"
	ctx := context.Background()

	err := l.authorizeInheritedOrg(ctx, "alice", Spec{DefaultOrgID: "home"}, &acme)
	if svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("removed member, no org named: %v, want PermissionDenied", err)
	}
	if err := l.authorizeInheritedOrg(ctx, "alice", Spec{}, &home); err != nil {
		t.Fatalf("member: %v", err)
	}
	machine := auth.WithMachineToken(ctx, &fat.Principal{OrgID: "home", ActingUserID: "alice"})
	if err := l.authorizeInheritedOrg(machine, "alice", Spec{}, &acme); svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("credential bound to another org, no org named: %v, want PermissionDenied", err)
	}
	// The org the request named was verified already (orgs.Named).
	if err := l.authorizeInheritedOrg(ctx, "alice", Spec{OrgID: "acme"}, &acme); err != nil {
		t.Fatalf("named org: %v", err)
	}
	// No orgs here: the project's org is inherited on ownership, as before.
	if err := (&Launcher{}).authorizeInheritedOrg(ctx, "alice", Spec{}, &acme); err != nil {
		t.Fatalf("no org authority: %v", err)
	}
}
