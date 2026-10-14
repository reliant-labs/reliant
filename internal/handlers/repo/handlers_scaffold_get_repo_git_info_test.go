package repo_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	repo "github.com/reliant-labs/reliant/internal/handlers/repo"
)

func TestGetRepoGitInfo_Generated(t *testing.T) {
	t.Parallel()

	unrouted := repo.NewTestRepo(t, repo.WithRepoDeps(repo.Deps{Repo: ownedProjectRepo(t)}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetRepoGitInfoRequest, pb.GetRepoGitInfoResponse]{
		{
			Name:    "an unknown repo is not found",
			Req:     connect.NewRequest(&pb.GetRepoGitInfoRequest{RepoId: "r-2"}),
			Ctx:     repo.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "without a daemon router the git status cannot be read",
			Req:     connect.NewRequest(&pb.GetRepoGitInfoRequest{RepoId: "r-1"}),
			Ctx:     repo.AuthedContext(t),
			WantErr: connect.CodeInternal,
		},
	}, unrouted.GetRepoGitInfo)

	// The daemon reads git status at the repo's absolute path.
	routed := repo.NewTestRepo(t, repo.WithRepoDeps(repo.Deps{
		Repo: ownedProjectRepo(t),
		Router: daemonFunc(func(commandType string, payload []byte) ([]byte, error) {
			if commandType != "project.git_info" || string(payload) != `{"path":"/work/svc"}` {
				return nil, connect.NewError(connect.CodeInvalidArgument, nil)
			}
			return []byte(`{"current_branch":"main","ahead":2}`), nil
		}),
	}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetRepoGitInfoRequest, pb.GetRepoGitInfoResponse]{
		{
			Name: "the repo's git status is returned",
			Req:  connect.NewRequest(&pb.GetRepoGitInfoRequest{RepoId: "r-1"}),
			Ctx:  repo.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetRepoGitInfoResponse]) {
				if resp.Msg.GetRepoId() != "r-1" || resp.Msg.GetCurrentBranch() != "main" || resp.Msg.GetAhead() != 2 {
					t.Fatalf("git info = %v, want r-1 on main, 2 ahead", resp.Msg)
				}
			},
		},
	}, routed.GetRepoGitInfo)
}
