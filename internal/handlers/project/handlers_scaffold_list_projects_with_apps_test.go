// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
// TestListProjectsWithApps_Generated: rows for `tdd.RunRPCCases`. The scaffold row is SELF-DESTRUCTING: it matches only
// forge's stub sentinel (not a bare CodeUnimplemented), so it fails once you implement ListProjectsWithApps —
// replace it then with Check / WantErr rows. Depth: `forge skill load testing/patterns`.
package project_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

// TestListProjectsWithApps_Generated runs the canonical scaffold rows for ListProjectsWithApps.
// Add more rows by appending tdd.RPCCase entries — never write a new
// Test<X> function for what is really a new row in this table.
func TestListProjectsWithApps_Generated(t *testing.T) {
	t.Parallel()

	// project.NewTestProject (helpers_gen_test.go) returns the
	// project *Service wired with stubbed dependencies. Override one
	// with project.WithProjectDeps(...), or per-row via Case.Setup.
	svc := project.NewTestProject(t, project.WithProjectDeps(project.Deps{Repo: &project.MockRepository{
		ListProjectsFunc:                  func(context.Context, db.ProjectFilters) ([]*db.Project, error) { return nil, nil },
		ListReposByProjectsFunc:           func(context.Context, []string) ([]*core.Repo, error) { return nil, nil },
		ListRepoForgeAppsByProjectsFunc:   func(context.Context, []string) ([]core.RepoForgeApp, error) { return nil, nil },
		ListProjectDaemonsForProjectsFunc: func(context.Context, []string) ([]*core.ProjectDaemon, error) { return nil, nil },
	}, Projects: projectaccesstest.For(&project.MockRepository{
		ListProjectsFunc:                  func(context.Context, db.ProjectFilters) ([]*db.Project, error) { return nil, nil },
		ListReposByProjectsFunc:           func(context.Context, []string) ([]*core.Repo, error) { return nil, nil },
		ListRepoForgeAppsByProjectsFunc:   func(context.Context, []string) ([]core.RepoForgeApp, error) { return nil, nil },
		ListProjectDaemonsForProjectsFunc: func(context.Context, []string) ([]*core.ProjectDaemon, error) { return nil, nil },
	})}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListProjectsWithAppsRequest, pb.ListProjectsWithAppsResponse]{
		{
			// A deployment with no orgs and no control plane: the caller's
			// own projects (none here), and their apps unavailable.
			Name: "no_orgs_no_control_plane",
			Req:  connect.NewRequest(&pb.ListProjectsWithAppsRequest{}),
			Ctx:  project.AuthedContext(t), // authenticated test claims (UserID "test-user", role "admin")
			Check: func(t *testing.T, resp *connect.Response[pb.ListProjectsWithAppsResponse]) {
				if resp.Msg.GetOrgId() != "" || len(resp.Msg.GetProjects()) != 0 ||
					resp.Msg.GetHostedAppsState() != pb.HostedAppsState_HOSTED_APPS_STATE_UNAVAILABLE {
					t.Fatalf("response %+v", resp.Msg)
				}
			},
		},
	}, svc.ListProjectsWithApps)
}
