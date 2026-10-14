package repo_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db/core"
	repo "github.com/reliant-labs/reliant/internal/handlers/repo"
)

func TestAddRepo_Generated(t *testing.T) {
	t.Parallel()

	// Without a daemon router the path cannot be checked.
	unrouted := repo.NewTestRepo(t, repo.WithRepoDeps(repo.Deps{Repo: ownedProjectRepo(t)}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.AddRepoRequest, pb.AddRepoResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.AddRepoRequest{ProjectId: "p-2", RelativePath: "svc"}),
			Ctx:     repo.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "without a daemon router the path cannot be validated",
			Req:     connect.NewRequest(&pb.AddRepoRequest{ProjectId: "p-1", RelativePath: "svc"}),
			Ctx:     repo.AuthedContext(t),
			WantErr: connect.CodeUnavailable,
		},
	}, unrouted.AddRepo)

	// svc is a git repository on disk and not yet registered; docs is not a
	// git repository.
	store := ownedProjectRepo(t)
	store.GetRepoByProjectAndPathFunc = func(context.Context, string, string) (*core.Repo, error) {
		return nil, errors.New("repo not found")
	}
	store.CreateRepoFunc = func(context.Context, *core.Repo) error { return nil }
	routed := repo.NewTestRepo(t, repo.WithRepoDeps(repo.Deps{
		Repo: store,
		Router: daemonFunc(func(_ string, payload []byte) ([]byte, error) {
			if string(payload) == `{"max_depth":1,"path":"/work/svc"}` {
				return []byte(`{"discovered":[{"relative_path":"","remote_url":"git@example.com:svc.git"}]}`), nil
			}
			return []byte(`{"discovered":[]}`), nil
		}),
	}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.AddRepoRequest, pb.AddRepoResponse]{
		{
			Name:    "a path that is not a git repository is a failed precondition",
			Req:     connect.NewRequest(&pb.AddRepoRequest{ProjectId: "p-1", RelativePath: "docs"}),
			Ctx:     repo.AuthedContext(t),
			WantErr: connect.CodeFailedPrecondition,
		},
		{
			Name: "a git repository is registered under its directory name",
			Req:  connect.NewRequest(&pb.AddRepoRequest{ProjectId: "p-1", RelativePath: "svc"}),
			Ctx:  repo.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.AddRepoResponse]) {
				got := resp.Msg.GetRepo()
				if got.GetName() != "svc" || got.GetRemoteUrl() != "git@example.com:svc.git" || got.GetId() == "" {
					t.Fatalf("repo = %v, want svc with its remote and an id", got)
				}
			},
		},
	}, routed.AddRepo)
}
