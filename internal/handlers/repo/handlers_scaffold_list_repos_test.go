package repo_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db/core"
	repo "github.com/reliant-labs/reliant/internal/handlers/repo"
)

func TestListRepos_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	scanned := time.Date(2026, 11, 18, 1, 2, 3, 0, time.UTC)
	store.ListReposByProjectFunc = func(_ context.Context, projectID string) ([]*core.Repo, error) {
		return []*core.Repo{
			{ID: "r-1", ProjectID: projectID, Name: "svc", RelativePath: "svc", ForgeAppsScannedAt: &scanned},
			// Never scanned (or only by a daemon older than the walk): unknown.
			{ID: "r-2", ProjectID: projectID, Name: "docs", RelativePath: "docs"},
		}, nil
	}
	store.ListRepoForgeAppsByProjectFunc = func(context.Context, string) ([]core.RepoForgeApp, error) {
		return []core.RepoForgeApp{
			{RepoID: "r-1", Subpath: "", Name: "svc"},
			{RepoID: "r-1", Subpath: "frontends/site", Name: "svc-site"},
		}, nil
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
				if got := resp.Msg.GetRepos(); len(got) != 2 || got[0].GetId() != "r-1" || got[0].GetProjectId() != "p-1" {
					t.Fatalf("repos = %v, want r-1 and r-2 in p-1", got)
				}
			},
		},
		{
			Name: "each repo carries its forge apps, and says whether they are known",
			Req:  connect.NewRequest(&pb.ListReposRequest{ProjectId: "p-1"}),
			Ctx:  repo.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListReposResponse]) {
				got := resp.Msg.GetRepos()
				apps := got[0].GetForgeApps()
				if len(apps) != 2 || apps[0].GetName() != "svc" || apps[0].GetSubpath() != "" ||
					apps[1].GetName() != "svc-site" || apps[1].GetSubpath() != "frontends/site" {
					t.Fatalf("r-1 forge apps = %v", apps)
				}
				if got[0].ForgeAppsScannedAt == nil || got[0].GetForgeAppsScannedAt() != "2026-11-18T01:02:03Z" {
					t.Fatalf("r-1 forge_apps_scanned_at = %v, want the scan time", got[0].ForgeAppsScannedAt)
				}
				if len(got[1].GetForgeApps()) != 0 || got[1].ForgeAppsScannedAt != nil {
					t.Fatalf("r-2 = %v, want no apps and no scan time: unknown", got[1])
				}
			},
		},
	}, svc.ListRepos)
}
