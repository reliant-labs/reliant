// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Repo. The generated repo_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: repoRepo (a *crud.Repo[Repo]) and the
//     package-level GetRepoByID / ListRepo / CreateRepo / UpdateRepo / DeleteRepo
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Repo struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindRepoBy(ctx context.Context, db orm.Context, /* args */) ([]*Repo, error) {
//		var rows []*Repo
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("project_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveRepos(ctx context.Context, db orm.Context) ([]*Repo, error) {
//		return ListRepo(ctx, db, orm.WhereEq("project_id", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitremote"
)

// RepoExtra carries fields on Repo that are NOT columns — computed
// values, transient state, anything derived rather than stored. Repo is
// embedded on RepoExtra (see repo_orm.go), so a field added here is a field on
// every Repo value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type RepoExtra struct {
}

// InsertRepo stores a nested repo with the created_at and updated_at the
// caller set (zero is now). A remote URL is stored without credentials.
func InsertRepo(ctx context.Context, db orm.Context, r *core.Repo) error {
	row := &Repo{
		ID:           r.ID,
		ProjectID:    r.ProjectID,
		Name:         r.Name,
		RelativePath: r.RelativePath,
		RemoteURL:    gitremote.StripCredentialsPtr(r.RemoteURL),
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
	return CreateRepo(ctx, db, row)
}

// GetRepo returns the repo, or an error naming it when there is none.
func GetRepo(ctx context.Context, db orm.Context, id string) (*core.Repo, error) {
	row, err := GetRepoByID(ctx, db, id)
	if errors.Is(err, orm.ErrNoRows) {
		return nil, fmt.Errorf("repo not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get repo: %w", err)
	}
	return repoToCore(row), nil
}

// GetRepoByProjectAndPath returns the project's repo at relativePath, or an
// error naming both when there is none.
func GetRepoByProjectAndPath(ctx context.Context, db orm.Context, projectID, relativePath string) (*core.Repo, error) {
	rows, err := ListRepo(ctx, db,
		orm.WhereEq(RepoFieldProjectID, projectID),
		orm.WhereEq(RepoFieldRelativePath, relativePath),
		orm.WithLimit(1))
	if err != nil {
		return nil, fmt.Errorf("failed to get repo by project+path: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("repo not found: project=%s path=%s", projectID, relativePath)
	}
	return repoToCore(rows[0]), nil
}

// ListReposByProject returns the project's repos ordered by relative path.
func ListReposByProject(ctx context.Context, db orm.Context, projectID string) ([]*core.Repo, error) {
	rows, err := ListRepo(ctx, db,
		orm.WhereEq(RepoFieldProjectID, projectID),
		orm.WithOrderBy(RepoFieldRelativePath, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list repos: %w", err)
	}
	out := make([]*core.Repo, len(rows))
	for i, row := range rows {
		out[i] = repoToCore(row)
	}
	return out, nil
}

// ReplaceRepo rewrites the repo's name and remote URL (without credentials)
// and stamps updated_at. A missing repo is not an error.
func ReplaceRepo(ctx context.Context, db orm.Context, r *core.Repo) error {
	row := &Repo{ID: r.ID, Name: r.Name, RemoteURL: gitremote.StripCredentialsPtr(r.RemoteURL)}
	err := UpdateRepoMasked(ctx, db, row, []string{RepoFieldName, RepoFieldRemoteURL})
	if err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

// RemoveRepo deletes the repo; a missing one is not an error.
func RemoveRepo(ctx context.Context, db orm.Context, id string) error {
	if err := DeleteRepo(ctx, db, id); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

func repoToCore(row *Repo) *core.Repo {
	return &core.Repo{
		ID:           row.ID,
		ProjectID:    row.ProjectID,
		Name:         row.Name,
		RelativePath: row.RelativePath,
		RemoteURL:    gitremote.StripCredentialsPtr(row.RemoteURL),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}
