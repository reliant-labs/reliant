package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestGetProject_Generated(t *testing.T) {
	t.Parallel()

	unrouted := newService(t, ownedProjectRepo(t), nil)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectRequest, pb.GetProjectResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.GetProjectRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "without a daemon the stored git flag is kept",
			Req:  connect.NewRequest(&pb.GetProjectRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectResponse]) {
				if got := resp.Msg.GetProject(); got.GetId() != "p-1" || got.GetPath() != "/work" || !got.GetIsGitRepo() {
					t.Fatalf("project = %v, want p-1 at /work, a git repository", got)
				}
			},
		},
	}, unrouted.GetProject)

	// The daemon no longer sees a .git directory at the project path.
	store := ownedProjectRepo(t)
	var persisted *db.Project
	store.UpdateProjectFunc = func(_ context.Context, p *db.Project, _ string) error {
		persisted = p
		return nil
	}
	routed := newService(t, store, answering("project.check_git", `{"path":"/work"}`, `{"is_git_repo":false}`))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectRequest, pb.GetProjectResponse]{
		{
			Name: "the daemon's observation of the git directory wins and is persisted",
			Req:  connect.NewRequest(&pb.GetProjectRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectResponse]) {
				if resp.Msg.GetProject().GetIsGitRepo() {
					t.Fatal("project still reports a git repository the daemon no longer sees")
				}
				if persisted == nil || persisted.IsGitRepo {
					t.Fatalf("persisted = %v, want the project stored as not a git repository", persisted)
				}
			},
		},
	}, routed.GetProject)
}
