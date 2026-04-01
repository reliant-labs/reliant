package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	sqlitedb "github.com/reliant-labs/reliant/internal/db/sqlite/generated"
)

type projectStore struct{ q sqlitedb.Querier }

// NewProjectStore creates the SQLite project store implementation.
func NewProjectStore(q sqlitedb.Querier) core.ProjectStore { return &projectStore{q: q} }

func (s *projectStore) CreateProject(ctx context.Context, project *core.Project) error {
	return s.q.CreateProject(ctx, projectToCreateParams(project))
}

func (s *projectStore) GetProject(ctx context.Context, id string) (*core.Project, error) {
	sqlcProject, err := s.q.GetProject(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	return projectFromSQLc(sqlcProject), nil
}

func (s *projectStore) GetProjectByPath(ctx context.Context, path string) (*core.Project, error) {
	sqlcProject, err := s.q.GetProjectByPath(ctx, path)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found: %s", path)
		}
		return nil, fmt.Errorf("failed to get project by path: %w", err)
	}
	return projectFromSQLc(sqlcProject), nil
}

func (s *projectStore) GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*core.Project, error) {
	sqlcProject, err := s.q.GetProjectWithUserCheck(ctx, sqlitedb.GetProjectWithUserCheckParams{ID: id, UserID: userID})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found or access denied")
		}
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	return projectFromSQLc(sqlcProject), nil
}

func (s *projectStore) ListProjects(ctx context.Context, filters core.ProjectFilters) ([]*core.Project, error) {
	limit := int64(filters.Limit)
	offset := int64(filters.Offset)
	if limit == 0 {
		limit = 100
	}
	sqlcProjects, err := s.q.ListProjects(ctx, sqlitedb.ListProjectsParams{
		UserID: filters.UserID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	return projectsFromSQLc(sqlcProjects), nil
}

func (s *projectStore) UpdateProject(ctx context.Context, project *core.Project, userID string) error {
	return s.q.UpdateProject(ctx, sqlitedb.UpdateProjectParams{
		ID:            project.ID,
		Name:          project.Name,
		Description:   ptrToNullString(project.Description),
		IsGitRepo:     project.IsGitRepo,
		DefaultBranch: ptrToNullString(project.DefaultBranch),
		LastActive:    project.LastActive,
		UserID:        userID,
	})
}

func (s *projectStore) TouchProject(ctx context.Context, id string, userID string) error {
	return s.q.TouchProject(ctx, sqlitedb.TouchProjectParams{
		ID:     id,
		UserID: userID,
	})
}

func (s *projectStore) DeleteProject(ctx context.Context, id string, userID string) error {
	return s.q.DeleteProject(ctx, sqlitedb.DeleteProjectParams{
		ID:     id,
		UserID: userID,
	})
}

type worktreeStore struct{ q sqlitedb.Querier }

// NewWorktreeStore creates the SQLite worktree store implementation.
func NewWorktreeStore(q sqlitedb.Querier) core.WorktreeStore { return &worktreeStore{q: q} }

func (s *worktreeStore) CreateWorktree(ctx context.Context, worktree *core.Worktree) error {
	return s.q.CreateWorktree(ctx, worktreeToCreateParams(worktree))
}

func (s *worktreeStore) GetWorktree(ctx context.Context, id string) (*core.Worktree, error) {
	sqlcWorktree, err := s.q.GetWorktree(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("worktree not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get worktree: %w", err)
	}
	return worktreeFromSQLc(sqlcWorktree), nil
}

func (s *worktreeStore) GetWorktreeByPath(ctx context.Context, path string) (*core.Worktree, error) {
	sqlcWorktree, err := s.q.GetWorktreeByPath(ctx, path)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("worktree not found: %s", path)
		}
		return nil, fmt.Errorf("failed to get worktree by path: %w", err)
	}
	return worktreeFromSQLc(sqlcWorktree), nil
}

func (s *worktreeStore) ListWorktrees(ctx context.Context, filters core.WorktreeFilters) ([]*core.Worktree, error) {
	limit := int64(filters.Limit)
	if limit == 0 {
		limit = 100
	}
	offset := int64(filters.Offset)

	var projectIDFilter interface{}
	if filters.ProjectID != nil {
		projectIDFilter = *filters.ProjectID
	}
	var chatIDFilter interface{}
	if filters.ChatID != nil {
		chatIDFilter = *filters.ChatID
	}
	var statusFilter interface{}
	if filters.Status != nil {
		statusFilter = *filters.Status
	}

	sqlcWorktrees, err := s.q.ListWorktrees(ctx, sqlitedb.ListWorktreesParams{
		ProjectID: projectIDFilter,
		ChatID:    chatIDFilter,
		Status:    statusFilter,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	return worktreesFromSQLc(sqlcWorktrees), nil
}

func (s *worktreeStore) UpdateWorktree(ctx context.Context, worktree *core.Worktree) error {
	return s.q.UpdateWorktree(ctx, sqlitedb.UpdateWorktreeParams{
		ID:         worktree.ID,
		Name:       worktree.Name,
		Branch:     worktree.Branch,
		Status:     int64(worktree.Status),
		BaseBranch: worktree.BaseBranch,
		LastActive: worktree.LastActive,
	})
}

func (s *worktreeStore) UpdateWorktreeCleanupMetadata(ctx context.Context, id string, metadata *core.CleanupMetadata) error {
	metadataJSON := sql.NullString{}
	if metadata != nil {
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal cleanup metadata: %w", err)
		}
		metadataJSON = sql.NullString{String: string(encoded), Valid: true}
	}
	return s.q.UpdateWorktreeCleanupMetadata(ctx, sqlitedb.UpdateWorktreeCleanupMetadataParams{CleanupMetadata: metadataJSON, ID: id})
}

func (s *worktreeStore) DeleteWorktree(ctx context.Context, id string) error {
	return s.q.DeleteWorktree(ctx, id)
}

func (s *worktreeStore) ArchiveWorktree(ctx context.Context, id string) error {
	return s.q.ArchiveWorktree(ctx, id)
}

func (s *worktreeStore) UnarchiveWorktree(ctx context.Context, id string) error {
	return s.q.UnarchiveWorktree(ctx, id)
}

func projectFromSQLc(sp sqlitedb.Project) *core.Project {
	return &core.Project{
		ID:            sp.ID,
		Name:          sp.Name,
		Path:          sp.Path,
		UserID:        sp.UserID,
		Description:   nullStringToPtr(sp.Description),
		IsGitRepo:     sp.IsGitRepo,
		DefaultBranch: nullStringToPtr(sp.DefaultBranch),
		CreatedAt:     sp.CreatedAt,
		UpdatedAt:     sp.UpdatedAt,
		LastActive:    sp.LastActive,
	}
}

func projectsFromSQLc(rows []sqlitedb.Project) []*core.Project {
	projects := make([]*core.Project, len(rows))
	for i, row := range rows {
		projects[i] = projectFromSQLc(row)
	}
	return projects
}

func projectToCreateParams(project *core.Project) sqlitedb.CreateProjectParams {
	return sqlitedb.CreateProjectParams{
		ID:            project.ID,
		UserID:        project.UserID,
		Name:          project.Name,
		Path:          project.Path,
		Description:   ptrToNullString(project.Description),
		IsGitRepo:     project.IsGitRepo,
		DefaultBranch: ptrToNullString(project.DefaultBranch),
		CreatedAt:     project.CreatedAt,
		UpdatedAt:     project.UpdatedAt,
		LastActive:    project.LastActive,
	}
}

func worktreeFromSQLc(sw sqlitedb.Worktree) *core.Worktree {
	var cleanupMetadata *core.CleanupMetadata
	if sw.CleanupMetadata.Valid {
		var parsed core.CleanupMetadata
		if err := json.Unmarshal([]byte(sw.CleanupMetadata.String), &parsed); err == nil {
			cleanupMetadata = &parsed
		}
	}

	return &core.Worktree{
		ID:              sw.ID,
		Name:            sw.Name,
		Path:            sw.Path,
		Branch:          sw.Branch,
		BaseBranch:      sw.BaseBranch,
		ProjectID:       sw.ProjectID,
		ChatID:          nullStringToPtr(sw.ChatID),
		Status:          int32(sw.Status),
		IsMain:          sw.IsMain,
		CreatedAt:       sw.CreatedAt,
		UpdatedAt:       sw.UpdatedAt,
		LastActive:      sw.LastActive,
		DeletedAt:       projectNullTimeToPtr(sw.DeletedAt),
		CleanupMetadata: cleanupMetadata,
	}
}

func worktreesFromSQLc(rows []sqlitedb.Worktree) []*core.Worktree {
	worktrees := make([]*core.Worktree, len(rows))
	for i, row := range rows {
		worktrees[i] = worktreeFromSQLc(row)
	}
	return worktrees
}

func worktreeToCreateParams(worktree *core.Worktree) sqlitedb.CreateWorktreeParams {
	return sqlitedb.CreateWorktreeParams{
		ID:         worktree.ID,
		Name:       worktree.Name,
		Path:       worktree.Path,
		Branch:     worktree.Branch,
		BaseBranch: worktree.BaseBranch,
		ProjectID:  worktree.ProjectID,
		ChatID:     ptrToNullString(worktree.ChatID),
		Status:     int64(worktree.Status),
		IsMain:     worktree.IsMain,
		CreatedAt:  worktree.CreatedAt,
		UpdatedAt:  worktree.UpdatedAt,
		LastActive: worktree.LastActive,
		DeletedAt:  projectPtrToNullTime(worktree.DeletedAt),
	}
}

func projectPtrToNullTime(t *time.Time) sql.NullTime {
	if t != nil {
		return sql.NullTime{Time: *t, Valid: true}
	}
	return sql.NullTime{}
}

func projectNullTimeToPtr(nt sql.NullTime) *time.Time {
	if nt.Valid {
		return &nt.Time
	}
	return nil
}
