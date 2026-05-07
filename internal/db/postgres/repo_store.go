// Copyright (c) 2025 Reliant Labs
package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type repoStore struct{ q pgdb.Querier }

// NewRepoStore creates the Postgres repo store implementation. Backs the
// nested-repo persistence used by the multi-repo project model.
func NewRepoStore(q pgdb.Querier) core.RepoStore { return &repoStore{q: q} }

func (s *repoStore) CreateRepo(ctx context.Context, repo *core.Repo) error {
	return s.q.CreateRepo(ctx, pgdb.CreateRepoParams{
		ID:           repo.ID,
		ProjectID:    repo.ProjectID,
		Name:         repo.Name,
		RelativePath: repo.RelativePath,
		RemoteUrl:    ptrToNullString(repo.RemoteURL),
		CreatedAt:    repo.CreatedAt,
		UpdatedAt:    repo.UpdatedAt,
	})
}

func (s *repoStore) GetRepo(ctx context.Context, id string) (*core.Repo, error) {
	row, err := s.q.GetRepo(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("repo not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get repo: %w", err)
	}
	return repoFromPG(row), nil
}

func (s *repoStore) GetRepoByProjectAndPath(ctx context.Context, projectID, relativePath string) (*core.Repo, error) {
	row, err := s.q.GetRepoByProjectAndPath(ctx, pgdb.GetRepoByProjectAndPathParams{
		ProjectID:    projectID,
		RelativePath: relativePath,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("repo not found: project=%s path=%s", projectID, relativePath)
		}
		return nil, fmt.Errorf("failed to get repo by project+path: %w", err)
	}
	return repoFromPG(row), nil
}

func (s *repoStore) ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error) {
	rows, err := s.q.ListReposByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list repos: %w", err)
	}
	out := make([]*core.Repo, len(rows))
	for i, row := range rows {
		out[i] = repoFromPG(row)
	}
	return out, nil
}

func (s *repoStore) UpdateRepo(ctx context.Context, repo *core.Repo) error {
	return s.q.UpdateRepo(ctx, pgdb.UpdateRepoParams{
		ID:        repo.ID,
		Name:      repo.Name,
		RemoteUrl: ptrToNullString(repo.RemoteURL),
	})
}

func (s *repoStore) DeleteRepo(ctx context.Context, id string) error {
	return s.q.DeleteRepo(ctx, id)
}

func repoFromPG(r pgdb.Repo) *core.Repo {
	return &core.Repo{
		ID:           r.ID,
		ProjectID:    r.ProjectID,
		Name:         r.Name,
		RelativePath: r.RelativePath,
		RemoteURL:    nullStringToPtr(r.RemoteUrl),
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
}
