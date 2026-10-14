package repo_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"
	"github.com/reliant-labs/forge/pkg/testkit"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	repo "github.com/reliant-labs/reliant/internal/handlers/repo"
)

func TestRemoveRepo_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	store.DeleteRepoFunc = func(context.Context, string) error { return nil }
	svc := repo.NewTestRepo(t, repo.WithRepoDeps(repo.Deps{Repo: store}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.RemoveRepoRequest, pb.RemoveRepoResponse]{
		{
			Name:    "an unknown repo is not found",
			Req:     connect.NewRequest(&pb.RemoveRepoRequest{RepoId: "r-2"}),
			Ctx:     repo.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's repo is not found",
			Req:     connect.NewRequest(&pb.RemoveRepoRequest{RepoId: "r-1"}),
			Ctx:     repo.AuthedContext(t, testkit.WithUserID("someone-else")),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the caller's repo is removed",
			Req:  connect.NewRequest(&pb.RemoveRepoRequest{RepoId: "r-1"}),
			Ctx:  repo.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.RemoveRepoResponse]) {
				if !resp.Msg.GetSuccess() {
					t.Fatal("success = false")
				}
				if n := store.CallCount("DeleteRepo"); n != 1 {
					t.Fatalf("DeleteRepo calls = %d, want 1", n)
				}
			},
		},
	}, svc.RemoveRepo)
}
