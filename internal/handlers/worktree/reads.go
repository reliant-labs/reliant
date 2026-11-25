package worktree

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The reads below keep the empty-id refusal the *db.Repo facade made before
// these handlers read through their own roles: an rpc handed an empty id
// fails the same way it always has, without a query.

func (s *Service) getWorktree(ctx context.Context, id string) (*core.Worktree, error) {
	if id == "" {
		return nil, errors.New("worktree ID cannot be empty")
	}
	return s.deps.Rows.GetWorktree(ctx, id)
}

func (s *Service) getProject(ctx context.Context, id string) (*core.Project, error) {
	if id == "" {
		return nil, errors.New("project ID cannot be empty")
	}
	return s.deps.ProjectRows.GetProject(ctx, id)
}

func (s *Service) listRepos(ctx context.Context, projectID string) ([]*core.Repo, error) {
	if projectID == "" {
		return nil, errors.New("project ID cannot be empty")
	}
	return s.deps.Repos.ListReposByProject(ctx, projectID)
}

func (s *Service) getDaemon(ctx context.Context, id string) (*core.Daemon, error) {
	if id == "" {
		return nil, errors.New("daemon ID cannot be empty")
	}
	d, err := s.deps.Daemons.GetDaemon(ctx, id)
	if err != nil && !svcerr.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get daemon %s: %w", id, err)
	}
	return d, err
}
