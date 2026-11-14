// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Project. The generated project_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: projectRepo (a *crud.Repo[Project]) and the
//     package-level GetProjectByID / ListProject / CreateProject / UpdateProject / DeleteProject
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Project struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindProjectBy(ctx context.Context, db orm.Context, /* args */) ([]*Project, error) {
//		var rows []*Project
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("name = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveProjects(ctx context.Context, db orm.Context) ([]*Project, error) {
//		return ListProject(ctx, db, orm.WhereEq("name", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitremote"
)

// ProjectExtra carries fields on Project that are NOT columns — computed
// values, transient state, anything derived rather than stored. Project is
// embedded on ProjectExtra (see project_orm.go), so a field added here is a field on
// every Project value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ProjectExtra struct {
}

// InsertProject stores a new project, with the created_at, updated_at and
// last_active the caller set (the generated Create keeps a caller-set
// timestamp). A remote URL is stored without credentials.
func InsertProject(ctx context.Context, db orm.Context, p *core.Project) error {
	if err := CreateProject(ctx, db, projectFromCore(p)); err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}
	return nil
}

// GetProject returns the project, or an error naming it when there is none.
func GetProject(ctx context.Context, db orm.Context, id string) (*core.Project, error) {
	row, err := GetProjectByID(ctx, db, id)
	if errors.Is(err, orm.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", svcerr.NotFound("project"), id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	return projectToCore(row), nil
}

// GetProjectByPath returns a project registered at path, or an error naming
// the path when there is none.
func GetProjectByPath(ctx context.Context, db orm.Context, path string) (*core.Project, error) {
	p, err := firstProject(ctx, db, orm.WhereEq(ProjectFieldPath, path))
	if err != nil {
		return nil, fmt.Errorf("failed to get project by path: %w", err)
	}
	if p == nil {
		return nil, fmt.Errorf("%w: %s", svcerr.NotFound("project"), path)
	}
	return p, nil
}

// GetProjectByPathAndUser returns the user's project at path, or an error
// naming the path when there is none.
func GetProjectByPathAndUser(ctx context.Context, db orm.Context, path, userID string) (*core.Project, error) {
	p, err := firstProject(ctx, db, orm.WhereEq(ProjectFieldPath, path), orm.WhereEq(ProjectFieldUserID, userID))
	if err != nil {
		return nil, fmt.Errorf("failed to get project by path and user: %w", err)
	}
	if p == nil {
		return nil, fmt.Errorf("%w: %s", svcerr.NotFound("project"), path)
	}
	return p, nil
}

// GetProjectByRemoteURLAndUser returns the user's project cloned from
// remoteURL (compared without credentials), or an error naming the URL when
// there is none.
func GetProjectByRemoteURLAndUser(ctx context.Context, db orm.Context, remoteURL, userID string) (*core.Project, error) {
	p, err := firstProject(ctx, db,
		orm.WhereEq(ProjectFieldRemoteURL, gitremote.StripCredentials(remoteURL)),
		orm.WhereEq(ProjectFieldUserID, userID))
	if err != nil {
		return nil, fmt.Errorf("failed to get project by remote_url and user: %w", err)
	}
	if p == nil {
		return nil, fmt.Errorf("%w for remote_url: %s", svcerr.NotFound("project"), remoteURL)
	}
	return p, nil
}

// GetProjectWithUserCheck returns the project when it is the user's, else
// core.ErrProjectNotFound.
func GetProjectWithUserCheck(ctx context.Context, db orm.Context, id, userID string) (*core.Project, error) {
	p, err := firstProject(ctx, db, orm.WhereEq(ProjectFieldID, id), orm.WhereEq(ProjectFieldUserID, userID))
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	if p == nil {
		return nil, core.ErrProjectNotFound
	}
	return p, nil
}

// ListProjects returns the user's projects, most recently active first. A
// zero limit is 100.
func ListProjects(ctx context.Context, db orm.Context, filters core.ProjectFilters) ([]*core.Project, error) {
	limit := filters.Limit
	if limit == 0 {
		limit = 100
	}
	rows, err := ListProject(ctx, db,
		orm.WhereEq(ProjectFieldUserID, filters.UserID),
		orm.WithOrderBy(ProjectFieldLastActive, orm.Desc),
		orm.WithLimit(limit),
		orm.WithOffset(filters.Offset))
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	out := make([]*core.Project, len(rows))
	for i, row := range rows {
		out[i] = projectToCore(row)
	}
	return out, nil
}

// ReplaceProject rewrites the user's project's mutable columns from p and
// stamps updated_at. A project that is missing or another user's is left
// alone, without an error.
func ReplaceProject(ctx context.Context, db orm.Context, p *core.Project, userID string) error {
	_, err := db.Bun().NewUpdate().Model(projectFromCore(p)).
		Column(ProjectFieldName, ProjectFieldDescription, ProjectFieldIsGitRepo, ProjectFieldDefaultBranch,
			ProjectFieldRemoteURL, ProjectFieldIsForge, ProjectFieldForgeProjectName, ProjectFieldLastActive).
		Set("updated_at = NOW()").
		Where("id = ?", p.ID).
		Where("user_id = ?", userID).
		Exec(ctx)
	return err
}

// SetProjectForgeName records forge's name for a project (forge.yaml `name`)
// and marks it a forge project. It reports whether it wrote: when both
// already hold it is a no-op, with no updated_at churn, so callers can run it
// on every successful forge read.
func SetProjectForgeName(ctx context.Context, db orm.Context, id, userID, forgeProjectName string) (bool, error) {
	res, err := db.Bun().NewUpdate().Model((*Project)(nil)).
		Set("is_forge = TRUE").
		Set("forge_project_name = ?", forgeProjectName).
		Set("updated_at = NOW()").
		Where("id = ?", id).
		Where("user_id = ?", userID).
		Where("(NOT is_forge OR forge_project_name IS DISTINCT FROM ?)", forgeProjectName).
		Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// TouchProject stamps the user's project as active now.
func TouchProject(ctx context.Context, db orm.Context, id, userID string) error {
	_, err := db.Bun().NewUpdate().Model((*Project)(nil)).
		Set("last_active = NOW()").
		Set("updated_at = NOW()").
		Where("id = ?", id).
		Where("user_id = ?", userID).
		Exec(ctx)
	return err
}

// DeleteUserProject deletes the user's project. A project that is missing or
// another user's is left alone, without an error.
func DeleteUserProject(ctx context.Context, db orm.Context, id, userID string) error {
	_, err := db.Bun().NewDelete().Model((*Project)(nil)).
		Where("id = ?", id).
		Where("user_id = ?", userID).
		Exec(ctx)
	return err
}

// firstProject returns the first project matching opts, or nil when none
// does.
func firstProject(ctx context.Context, db orm.Context, opts ...orm.QueryOption) (*core.Project, error) {
	rows, err := ListProject(ctx, db, append(opts, orm.WithLimit(1))...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return projectToCore(rows[0]), nil
}

func projectFromCore(p *core.Project) *Project {
	isGitRepo := p.IsGitRepo
	return &Project{
		ID:               p.ID,
		Name:             p.Name,
		Path:             p.Path,
		UserID:           p.UserID,
		Description:      p.Description,
		IsGitRepo:        &isGitRepo,
		DefaultBranch:    p.DefaultBranch,
		RemoteURL:        gitremote.StripCredentialsPtr(p.RemoteURL),
		IsForge:          p.IsForge,
		ForgeProjectName: p.ForgeProjectName,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        p.UpdatedAt,
		LastActive:       p.LastActive,
	}
}

func projectToCore(row *Project) *core.Project {
	return &core.Project{
		ID:               row.ID,
		Name:             row.Name,
		Path:             row.Path,
		UserID:           row.UserID,
		Description:      row.Description,
		IsGitRepo:        row.IsGitRepo != nil && *row.IsGitRepo,
		DefaultBranch:    row.DefaultBranch,
		RemoteURL:        gitremote.StripCredentialsPtr(row.RemoteURL),
		IsForge:          row.IsForge,
		ForgeProjectName: row.ForgeProjectName,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
		LastActive:       row.LastActive,
	}
}
