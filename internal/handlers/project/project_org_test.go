package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// orgSource: test-user is in acme and home; home is their default.
type orgSource struct{}

func (orgSource) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	if userID != "test-user" {
		return accesstokenclient.OrgMembership{OrgID: orgID}, nil
	}
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: orgID == "acme" || orgID == "home"}, nil
}

func TestListProjects_ByOrg(t *testing.T) {
	t.Parallel()
	var filters db.ProjectFilters
	var stamped []string
	store := &project.MockRepository{
		ListProjectsFunc: func(_ context.Context, f db.ProjectFilters) ([]*db.Project, error) {
			filters = f
			return nil, nil
		},
		AssignUnassignedOrgFunc: func(_ context.Context, userID, orgID string) error {
			stamped = append(stamped, userID+"→"+orgID)
			return nil
		},
	}
	svc := project.NewTestProject(t, project.WithProjectDeps(project.Deps{Repo: store, Orgs: orgs.New(orgSource{})}))
	ctx := project.AuthedContext(t)
	list := func(org *string) error {
		_, err := svc.ListProjects(ctx, connect.NewRequest(&pb.ListProjectsRequest{OrgId: org}))
		return err
	}
	str := func(s string) *string { return &s }

	if err := list(nil); err != nil || filters.Org != nil {
		t.Fatalf("no org named: err %v, scope %+v; want every project the caller owns", err, filters.Org)
	}
	if err := list(str("acme")); err != nil || filters.Org == nil || filters.Org.OrgID != "acme" || filters.Org.IncludeUnassigned {
		t.Fatalf("acme: err %v, scope %+v", err, filters.Org)
	}
	if len(stamped) != 0 {
		t.Fatalf("stamped %v listing a non-default org", stamped)
	}
	if err := list(str("home")); err != nil || filters.Org == nil || !filters.Org.IncludeUnassigned {
		t.Fatalf("home, the default: err %v, scope %+v; want unassigned rows included", err, filters.Org)
	}
	if len(stamped) != 1 || stamped[0] != "test-user→home" {
		t.Fatalf("stamped %v, want the caller's unassigned rows given their default org", stamped)
	}
	if err := list(str("globex")); svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("an org the caller is not in: %v, want PermissionDenied", err)
	}
}

// A deployment with no control plane has a nil resolver: org_id is ignored.
func TestListProjects_NoOrgsIgnoresTheOrg(t *testing.T) {
	t.Parallel()
	var filters db.ProjectFilters
	store := &project.MockRepository{ListProjectsFunc: func(_ context.Context, f db.ProjectFilters) ([]*db.Project, error) {
		filters = f
		return nil, nil
	}}
	svc := project.NewTestProject(t, project.WithProjectDeps(project.Deps{Repo: store, Orgs: orgs.New(nil)}))
	org := "acme"
	if _, err := svc.ListProjects(project.AuthedContext(t), connect.NewRequest(&pb.ListProjectsRequest{OrgId: &org})); err != nil {
		t.Fatal(err)
	}
	if filters.Org != nil {
		t.Fatalf("scope %+v, want none without orgs", filters.Org)
	}
}

// A machine credential lists its own org whether or not the request names
// it: omitting org_id never lists its acting user's other orgs.
func TestListProjects_MachineCredentialListsItsOrg(t *testing.T) {
	t.Parallel()
	var filters db.ProjectFilters
	store := &project.MockRepository{
		ListProjectsFunc: func(_ context.Context, f db.ProjectFilters) ([]*db.Project, error) {
			filters = f
			return nil, nil
		},
		AssignUnassignedOrgFunc: func(context.Context, string, string) error { return nil },
	}
	svc := project.NewTestProject(t, project.WithProjectDeps(project.Deps{Repo: store, Orgs: orgs.New(orgSource{})}))
	ctx := auth.WithMachineToken(project.AuthedContext(t), &fat.Principal{OrgID: "acme", ActingUserID: "test-user"})
	str := func(s string) *string { return &s }

	for _, org := range []*string{nil, str("acme")} {
		filters = db.ProjectFilters{}
		if _, err := svc.ListProjects(ctx, connect.NewRequest(&pb.ListProjectsRequest{OrgId: org})); err != nil {
			t.Fatal(err)
		}
		if filters.Org == nil || filters.Org.OrgID != "acme" || filters.Org.IncludeUnassigned {
			t.Fatalf("org %v: scope %+v, want acme only", org, filters.Org)
		}
	}
	_, err := svc.ListProjects(ctx, connect.NewRequest(&pb.ListProjectsRequest{OrgId: str("home")}))
	if svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("another org: %v, want PermissionDenied", err)
	}
}
