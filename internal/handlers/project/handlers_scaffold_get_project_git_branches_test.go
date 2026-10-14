package project_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestGetProjectGitBranches_Generated(t *testing.T) {
	t.Parallel()

	unrouted := newService(t, ownedProjectRepo(t), nil)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectGitBranchesRequest, pb.GetProjectGitBranchesResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.GetProjectGitBranchesRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "an unreachable daemon lists no branches rather than failing",
			Req:  connect.NewRequest(&pb.GetProjectGitBranchesRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectGitBranchesResponse]) {
				if got := resp.Msg.GetBranches(); len(got) != 0 {
					t.Fatalf("branches = %v, want none", got)
				}
			},
		},
	}, unrouted.GetProjectGitBranches)

	routed := newService(t, ownedProjectRepo(t), answering("project.git_branches", `{"path":"/work"}`,
		`{"branches":[{"name":"main","is_current":true,"commit_sha":"abc123"},{"name":"origin/main","is_remote":true}]}`))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectGitBranchesRequest, pb.GetProjectGitBranchesResponse]{
		{
			Name: "the daemon's branches are returned",
			Req:  connect.NewRequest(&pb.GetProjectGitBranchesRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectGitBranchesResponse]) {
				got := resp.Msg.GetBranches()
				if len(got) != 2 || got[0].GetName() != "main" || !got[0].GetIsCurrent() || got[0].GetCommitSha() != "abc123" || !got[1].GetIsRemote() {
					t.Fatalf("branches = %v, want main (current) and origin/main (remote)", got)
				}
			},
		},
	}, routed.GetProjectGitBranches)
}
