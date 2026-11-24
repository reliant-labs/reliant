package repo

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// =============================================================================
// ListRepos: returns persisted repos for a project.
// =============================================================================

func (s *Service) ListRepos(
	ctx context.Context,
	req *connect.Request[reliantv1.ListReposRequest],
) (*connect.Response[reliantv1.ListReposResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if _, err := s.authorizeProject(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	repos, err := s.deps.Repos.ListReposByProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repos"))
	}
	apps, err := s.deps.Repo.ListRepoForgeAppsByProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list the repos' forge apps"))
	}
	appsByRepo := make(map[string][]core.RepoForgeApp, len(repos))
	for _, app := range apps {
		appsByRepo[app.RepoID] = append(appsByRepo[app.RepoID], app)
	}

	out := make([]*reliantv1.Repo, len(repos))
	for i, r := range repos {
		out[i] = repoToProto(r, appsByRepo[r.ID])
	}
	return connect.NewResponse(&reliantv1.ListReposResponse{Repos: out}), nil
}
