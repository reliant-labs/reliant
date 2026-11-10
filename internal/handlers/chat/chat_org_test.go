package chat

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// orgSource: alice is a live member of home only; home is her default.
type orgSource struct{}

func (orgSource) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: userID == "alice"}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: userID == "alice" && orgID == "home"}, nil
}

func orgService(t *testing.T, projectOrg *string) *Service {
	t.Helper()
	repo := &MockRepository{
		GetProjectWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Project, error) {
			return &db.Project{ID: id, UserID: userID, OrgID: projectOrg}, nil
		},
	}
	return NewTestChat(t, WithChatDeps(Deps{Repo: repo, Orgs: orgs.New(orgSource{})}))
}

// A chat in an org's project belongs to that org, so even a request that
// names no org (an old client) must come from a live member of it: owning the
// project row is not authority in its org.
func TestNewChatOrgs_ProjectOrgNeedsLiveMembership(t *testing.T) {
	ctx := context.Background()
	acme, home := "acme", "home"

	_, _, err := orgService(t, &acme).newChatOrgs(ctx, "alice", "p-1", "")
	if svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("a project of an org the caller left, no org named: %v, want PermissionDenied", err)
	}
	named, _, err := orgService(t, &home).newChatOrgs(ctx, "alice", "p-1", "")
	if err != nil || named != "home" {
		t.Fatalf("a project of a member org: %q, %v; want home", named, err)
	}
	named, def, err := orgService(t, nil).newChatOrgs(ctx, "alice", "p-1", "")
	if err != nil || named != "" || def != "home" {
		t.Fatalf("a project with no org: named %q def %q, %v; want the default org", named, def, err)
	}
}

func TestCrossesOrgs(t *testing.T) {
	acme, home := "acme", "home"
	cases := map[string]struct {
		from, to *string
		want     bool
	}{
		"same org":       {&acme, &acme, false},
		"different orgs": {&acme, &home, true},
		"legacy chat into the default org's project":         {nil, &home, false},
		"legacy chat into another org's project":             {nil, &acme, true},
		"assigned chat into a legacy project of its org":     {&home, nil, false},
		"assigned chat into a legacy project of another org": {&acme, nil, true},
		"both legacy": {nil, nil, false},
	}
	for name, tc := range cases {
		if got := crossesOrgs(tc.from, tc.to, "home"); got != tc.want {
			t.Errorf("%s: crossesOrgs = %v, want %v", name, got, tc.want)
		}
	}
}
