package repo_test

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	repo "github.com/reliant-labs/reliant/internal/handlers/repo"
)

func TestDiscoverRepos_Generated(t *testing.T) {
	t.Parallel()

	// Without a daemon router nothing can scan the filesystem.
	unrouted := repo.NewTestRepo(t, repo.WithRepoDeps(repo.Deps{Repo: ownedProjectRepo(t)}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DiscoverReposRequest, pb.DiscoverReposResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.DiscoverReposRequest{ProjectId: "p-2"}),
			Ctx:     repo.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "without a daemon router the scan is unavailable",
			Req:     connect.NewRequest(&pb.DiscoverReposRequest{ProjectId: "p-1"}),
			Ctx:     repo.AuthedContext(t),
			WantErr: connect.CodeUnavailable,
		},
	}, unrouted.DiscoverRepos)

	// The daemon scans the project's own path and finds one nested repo.
	var scanned string
	routed := repo.NewTestRepo(t, repo.WithRepoDeps(repo.Deps{
		Repo: ownedProjectRepo(t),
		Router: daemonFunc(func(commandType string, payload []byte) ([]byte, error) {
			var p struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(payload, &p)
			scanned = commandType + " " + p.Path
			return []byte(`{"discovered":[{"relative_path":"svc","name":"svc"}]}`), nil
		}),
	}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DiscoverReposRequest, pb.DiscoverReposResponse]{
		{
			Name: "the daemon's findings are returned, not persisted",
			Req:  connect.NewRequest(&pb.DiscoverReposRequest{ProjectId: "p-1"}),
			Ctx:  repo.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.DiscoverReposResponse]) {
				if scanned != "repo.discover /work" {
					t.Fatalf("daemon command = %q, want repo.discover /work", scanned)
				}
				if got := resp.Msg.GetDiscovered(); len(got) != 1 || got[0].GetRelativePath() != "svc" || got[0].GetId() != "" {
					t.Fatalf("discovered = %v, want svc with no id", got)
				}
			},
		},
	}, routed.DiscoverRepos)
}
