package repo_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db/core"
	repo "github.com/reliant-labs/reliant/internal/handlers/repo"
)

func TestListRepos_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	store.ListReposByProjectFunc = func(_ context.Context, projectID string) ([]*core.Repo, error) {
		return []*core.Repo{{ID: "r-1", ProjectID: projectID, Name: "svc", RelativePath: "svc"}}, nil
	}
	svc := repo.NewTestRepo(t, repo.WithRepoDeps(repo.Deps{Repo: store}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListReposRequest, pb.ListReposResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.ListReposRequest{ProjectId: "p-2"}),
			Ctx:     repo.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the project's repos are listed",
			Req:  connect.NewRequest(&pb.ListReposRequest{ProjectId: "p-1"}),
			Ctx:  repo.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListReposResponse]) {
				if got := resp.Msg.GetRepos(); len(got) != 1 || got[0].GetId() != "r-1" || got[0].GetProjectId() != "p-1" {
					t.Fatalf("repos = %v, want r-1 in p-1", got)
				}
			},
		},
	}, svc.ListRepos)
}
