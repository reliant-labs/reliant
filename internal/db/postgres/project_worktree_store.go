package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type projectStore struct{ q pgdb.Querier }

// NewProjectStore creates the Postgres project store implementation.
func NewProjectStore(q pgdb.Querier) core.ProjectStore { return &projectStore{q: q} }

func (s *projectStore) CreateProject(ctx context.Context, project *core.Project) error {
	return s.q.CreateProject(ctx, pgdb.CreateProjectParams{
		ID:            project.ID,
		UserID:        project.UserID,
		Name:          project.Name,
		Path:          project.Path,
		Description:   ptrToNullString(project.Description),
		IsGitRepo:     project.IsGitRepo,
		DefaultBranch: ptrToNullString(project.DefaultBranch),
		RemoteUrl:     ptrToNullString(project.RemoteURL),
		IsForge:       project.IsForge,
		CreatedAt:     project.CreatedAt,
		UpdatedAt:     project.UpdatedAt,
		LastActive:    project.LastActive,
	})
}

func (s *projectStore) GetProject(ctx context.Context, id string) (*core.Project, error) {
	row, err := s.q.GetProject(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	return projectFromPG(row), nil
}

func (s *projectStore) GetProjectByPath(ctx context.Context, path string) (*core.Project, error) {
	row, err := s.q.GetProjectByPath(ctx, path)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found: %s", path)
		}
		return nil, fmt.Errorf("failed to get project by path: %w", err)
	}
	return projectFromPG(row), nil
}

func (s *projectStore) GetProjectByPathAndUser(ctx context.Context, path, userID string) (*core.Project, error) {
	row, err := s.q.GetProjectByPathAndUser(ctx, pgdb.GetProjectByPathAndUserParams{Path: path, UserID: userID})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found: %s", path)
		}
		return nil, fmt.Errorf("failed to get project by path and user: %w", err)
	}
	return projectFromPG(row), nil
}

func (s *projectStore) GetProjectByRemoteURLAndUser(ctx context.Context, remoteURL, userID string) (*core.Project, error) {
	row, err := s.q.GetProjectByRemoteURLAndUser(ctx, pgdb.GetProjectByRemoteURLAndUserParams{
		RemoteUrl: sql.NullString{String: remoteURL, Valid: remoteURL != ""},
		UserID:    userID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found for remote_url: %s", remoteURL)
		}
		return nil, fmt.Errorf("failed to get project by remote_url and user: %w", err)
	}
	return projectFromPG(row), nil
}

func (s *projectStore) GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*core.Project, error) {
	row, err := s.q.GetProjectWithUserCheck(ctx, pgdb.GetProjectWithUserCheckParams{ID: id, UserID: userID})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found or access denied")
		}
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	return projectFromPG(row), nil
}

func (s *projectStore) ListProjects(ctx context.Context, filters core.ProjectFilters) ([]*core.Project, error) {
	limit := int64(filters.Limit)
	offset := int64(filters.Offset)
	if limit == 0 {
		limit = 100
	}
	rows, err := s.q.ListProjects(ctx, pgdb.ListProjectsParams{UserID: filters.UserID, Offset: int32(offset), Limit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	return projectsFromPG(rows), nil
}

func (s *projectStore) UpdateProject(ctx context.Context, project *core.Project, userID string) error {
	return s.q.UpdateProject(ctx, pgdb.UpdateProjectParams{
		ID:            project.ID,
		Name:          project.Name,
		Description:   ptrToNullString(project.Description),
		IsGitRepo:     project.IsGitRepo,
		DefaultBranch: ptrToNullString(project.DefaultBranch),
		RemoteUrl:     ptrToNullString(project.RemoteURL),
		IsForge:       project.IsForge,
		LastActive:    project.LastActive,
		UserID:        userID,
	})
}

func (s *projectStore) TouchProject(ctx context.Context, id string, userID string) error {
	return s.q.TouchProject(ctx, pgdb.TouchProjectParams{
		ID:     id,
		UserID: userID,
	})
}

func (s *projectStore) DeleteProject(ctx context.Context, id string, userID string) error {
	return s.q.DeleteProject(ctx, pgdb.DeleteProjectParams{
		ID:     id,
		UserID: userID,
	})
}

func (s *projectStore) UpsertProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string) error {
	return s.q.UpsertProjectDaemon(ctx, pgdb.UpsertProjectDaemonParams{
		ProjectID:     projectID,
		DaemonID:      daemonID,
		Path:          path,
		DefaultBranch: ptrToNullString(defaultBranch),
	})
}

func (s *projectStore) ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error) {
	rows, err := s.q.ListProjectDaemonsForProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project_daemons for project: %w", err)
	}
	out := make([]*core.ProjectDaemon, len(rows))
	for i, row := range rows {
		out[i] = projectDaemonFromPG(row)
	}
	return out, nil
}

func (s *projectStore) ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemon, error) {
	rows, err := s.q.ListProjectDaemonsForDaemon(ctx, daemonID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project_daemons for daemon: %w", err)
	}
	out := make([]*core.ProjectDaemon, len(rows))
	for i, row := range rows {
		out[i] = projectDaemonFromPG(row)
	}
	return out, nil
}

func (s *projectStore) DeleteProjectDaemon(ctx context.Context, projectID, daemonID string) error {
	return s.q.DeleteProjectDaemon(ctx, pgdb.DeleteProjectDaemonParams{
		ProjectID: projectID,
		DaemonID:  daemonID,
	})
}

func projectDaemonFromPG(row pgdb.ProjectDaemon) *core.ProjectDaemon {
	return &core.ProjectDaemon{
		ProjectID:     row.ProjectID,
		DaemonID:      row.DaemonID,
		Path:          row.Path,
		DefaultBranch: nullStringToPtr(row.DefaultBranch),
		ClonedAt:      row.ClonedAt,
	}
}

type worktreeStore struct{ q pgdb.Querier }

// NewWorktreeStore creates the Postgres worktree store implementation.
func NewWorktreeStore(q pgdb.Querier) core.WorktreeStore { return &worktreeStore{q: q} }

func (s *worktreeStore) CreateWorktree(ctx context.Context, worktree *core.Worktree) error {
	baseBranchesJSON, _ := encodeBaseBranches(worktree.BaseBranches)
	return s.q.CreateWorktree(ctx, pgdb.CreateWorktreeParams{
		ID:           worktree.ID,
		Name:         worktree.Name,
		Path:         worktree.Path,
		Branch:       worktree.Branch,
		BaseBranch:   worktree.BaseBranch,
		BaseBranches: baseBranchesJSON,
		ProjectID:    worktree.ProjectID,
		ChatID:       ptrToNullString(worktree.ChatID),
		Status:       worktree.Status,
		IsMain:       worktree.IsMain,
		CreatedAt:    worktree.CreatedAt,
		UpdatedAt:    worktree.UpdatedAt,
		LastActive:   worktree.LastActive,
		DeletedAt:    projectPtrToNullTime(worktree.DeletedAt),
	})
}

// encodeBaseBranches marshals a per-repo base-branch map for storage.
// nil/empty map -> NULL column.
func encodeBaseBranches(m map[string]string) (sql.NullString, error) {
	if len(m) == 0 {
		return sql.NullString{}, nil
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("failed to marshal base_branches: %w", err)
	}
	return sql.NullString{String: string(encoded), Valid: true}, nil
}

func decodeBaseBranches(ns sql.NullString) map[string]string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(ns.String), &parsed); err != nil {
		return nil
	}
	return parsed
}

func (s *worktreeStore) GetWorktree(ctx context.Context, id string) (*core.Worktree, error) {
	row, err := s.q.GetWorktree(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("worktree not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get worktree: %w", err)
	}
	return worktreeFromPG(row), nil
}

func (s *worktreeStore) GetWorktreeByPath(ctx context.Context, path string) (*core.Worktree, error) {
	row, err := s.q.GetWorktreeByPath(ctx, path)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("worktree not found: %s", path)
		}
		return nil, fmt.Errorf("failed to get worktree by path: %w", err)
	}
	return worktreeFromPG(row), nil
}

func (s *worktreeStore) ListWorktrees(ctx context.Context, filters core.WorktreeFilters) ([]*core.Worktree, error) {
	limit := int64(filters.Limit)
	if limit == 0 {
		limit = 100
	}
	offset := int64(filters.Offset)

	projectIDNull := sql.NullString{}
	if filters.ProjectID != nil {
		projectIDNull = sql.NullString{String: *filters.ProjectID, Valid: true}
	}
	chatIDNull := sql.NullString{}
	if filters.ChatID != nil {
		chatIDNull = sql.NullString{String: *filters.ChatID, Valid: true}
	}
	statusNull := sql.NullInt32{}
	if filters.Status != nil {
		statusNull = sql.NullInt32{Int32: int32(*filters.Status), Valid: true}
	}

	rows, err := s.q.ListWorktrees(ctx, pgdb.ListWorktreesParams{
		ProjectID:       projectIDNull,
		ChatID:          chatIDNull,
		Status:          statusNull,
		IncludeArchived: filters.IncludeArchived,
		Limit:           int32(limit),
		Offset:          int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}
	return worktreesFromPG(rows), nil
}

func (s *worktreeStore) UpdateWorktree(ctx context.Context, worktree *core.Worktree) error {
	baseBranchesJSON, err := encodeBaseBranches(worktree.BaseBranches)
	if err != nil {
		return err
	}
	return s.q.UpdateWorktree(ctx, pgdb.UpdateWorktreeParams{
		ID:           worktree.ID,
		Name:         worktree.Name,
		Branch:       worktree.Branch,
		Status:       worktree.Status,
		BaseBranch:   worktree.BaseBranch,
		BaseBranches: baseBranchesJSON,
		LastActive:   worktree.LastActive,
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
	return s.q.UpdateWorktreeCleanupMetadata(ctx, pgdb.UpdateWorktreeCleanupMetadataParams{CleanupMetadata: metadataJSON, ID: id})
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

func projectFromPG(row pgdb.Project) *core.Project {
	return &core.Project{
		ID:            row.ID,
		Name:          row.Name,
		Path:          row.Path,
		UserID:        row.UserID,
		Description:   nullStringToPtr(row.Description),
		IsGitRepo:     row.IsGitRepo,
		DefaultBranch: nullStringToPtr(row.DefaultBranch),
		RemoteURL:     nullStringToPtr(row.RemoteUrl),
		IsForge:       row.IsForge,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
		LastActive:    row.LastActive,
	}
}

func projectsFromPG(rows []pgdb.Project) []*core.Project {
	projects := make([]*core.Project, len(rows))
	for i, row := range rows {
		projects[i] = projectFromPG(row)
	}
	return projects
}

func worktreeFromPG(row pgdb.Worktree) *core.Worktree {
	var cleanupMetadata *core.CleanupMetadata
	if row.CleanupMetadata.Valid {
		var parsed core.CleanupMetadata
		if err := json.Unmarshal([]byte(row.CleanupMetadata.String), &parsed); err == nil {
			cleanupMetadata = &parsed
		}
	}
	return &core.Worktree{
		ID:              row.ID,
		Name:            row.Name,
		Path:            row.Path,
		Branch:          row.Branch,
		BaseBranch:      row.BaseBranch,
		BaseBranches:    decodeBaseBranches(row.BaseBranches),
		ProjectID:       row.ProjectID,
		ChatID:          nullStringToPtr(row.ChatID),
		Status:          row.Status,
		IsMain:          row.IsMain,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		LastActive:      row.LastActive,
		DeletedAt:       projectNullTimeToPtr(row.DeletedAt),
		CleanupMetadata: cleanupMetadata,
	}
}

func worktreesFromPG(rows []pgdb.Worktree) []*core.Worktree {
	worktrees := make([]*core.Worktree, len(rows))
	for i, row := range rows {
		worktrees[i] = worktreeFromPG(row)
	}
	return worktrees
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
