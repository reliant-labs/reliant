package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestListRepositoriesForDaemon_Generated(t *testing.T) {
	t.Parallel()

	// d-1 holds the caller's p-1, with no branch of its own recorded, and
	// another user's p-2.
	store := ownedProjectRepo(t)
	store.ListProjectDaemonsForDaemonFunc = func(_ context.Context, daemonID string) ([]*core.ProjectDaemon, error) {
		return []*core.ProjectDaemon{
			{ProjectID: "p-1", DaemonID: daemonID, Path: "/clone"},
			{ProjectID: "p-2", DaemonID: daemonID, Path: "/theirs"},
		}, nil
	}
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListRepositoriesForDaemonRequest, pb.ListRepositoriesForDaemonResponse]{
		{
			Name:    "daemon_id is required",
			Req:     connect.NewRequest(&pb.ListRepositoriesForDaemonRequest{}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "the caller's clones are joined with their project",
			Req:  connect.NewRequest(&pb.ListRepositoriesForDaemonRequest{DaemonId: "d-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListRepositoriesForDaemonResponse]) {
				got := resp.Msg.GetRepositories()
				if len(got) != 1 {
					t.Fatalf("repositories = %v, want only p-1", got)
				}
				r := got[0]
				if r.GetProjectId() != "p-1" || r.GetName() != "proj" || r.GetPath() != "/clone" ||
					r.GetBranch() != "main" || r.GetRemoteUrl() != "git@example.com:acme/proj.git" {
					t.Fatalf("repository = %v, want p-1 at /clone on the project's main", r)
				}
			},
		},
	}, svc.ListRepositoriesForDaemon)
}
