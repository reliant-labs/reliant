package core

import (
	"context"
	"time"
)

// Project represents a code repository.
type Project struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Path          string    `json:"path"`
	UserID        string    `json:"user_id"`
	Description   *string   `json:"description,omitempty"`
	IsGitRepo     bool      `json:"is_git_repo"`
	DefaultBranch *string   `json:"default_branch,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastActive    time.Time `json:"last_active"`
}

// CleanupMetadata tracks what was cleaned up when archiving a worktree.
type CleanupMetadata struct {
	DirectoryDeleted bool `json:"directory_deleted"`
	BranchDeleted    bool `json:"branch_deleted"`
}

// Worktree represents a git worktree.
type Worktree struct {
	ID         string
	Name       string
	Path       string
	Branch     string
	BaseBranch string
	ProjectID  string
	// RepoID identifies which nested repo this worktree belongs to. Optional
	// during the multi-repo migration; required once backfill completes.
	RepoID          *string
	ChatID          *string
	Status          int32
	IsMain          bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastActive      time.Time
	DeletedAt       *time.Time
	CleanupMetadata *CleanupMetadata `json:"cleanup_metadata,omitempty"`
}

// Repo is a git repository nested inside a project.
//
// A project that is itself a git repo has one Repo with RelativePath == "".
// A project that contains N sibling repos has one Repo per sibling with
// RelativePath set to the sibling's path relative to the project root.
type Repo struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Name         string    `json:"name"`
	RelativePath string    `json:"relative_path"`
	RemoteURL    *string   `json:"remote_url,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ProjectFilters contains options for filtering projects.
type ProjectFilters struct {
	UserID string
	Limit  int
	Offset int
}

// WorktreeFilters contains options for filtering worktrees.
type WorktreeFilters struct {
	ProjectID       *string
	ChatID          *string
	Status          *int32
	IncludeArchived bool
	Limit           int
	Offset          int
}

// ProjectStore is the shared contract for project persistence across drivers.
type ProjectStore interface {
	CreateProject(ctx context.Context, project *Project) error
	GetProject(ctx context.Context, id string) (*Project, error)
	GetProjectByPath(ctx context.Context, path string) (*Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*Project, error)
	ListProjects(ctx context.Context, filters ProjectFilters) ([]*Project, error)
	UpdateProject(ctx context.Context, project *Project, userID string) error
	TouchProject(ctx context.Context, id string, userID string) error
	DeleteProject(ctx context.Context, id string, userID string) error
}

// WorktreeStore is the shared contract for worktree persistence across drivers.
type WorktreeStore interface {
	CreateWorktree(ctx context.Context, worktree *Worktree) error
	GetWorktree(ctx context.Context, id string) (*Worktree, error)
	GetWorktreeByPath(ctx context.Context, path string) (*Worktree, error)
	ListWorktrees(ctx context.Context, filters WorktreeFilters) ([]*Worktree, error)
	UpdateWorktree(ctx context.Context, worktree *Worktree) error
	UpdateWorktreeCleanupMetadata(ctx context.Context, id string, metadata *CleanupMetadata) error
	DeleteWorktree(ctx context.Context, id string) error
	ArchiveWorktree(ctx context.Context, id string) error
	UnarchiveWorktree(ctx context.Context, id string) error
}

// RepoStore is the shared contract for nested-repo persistence across drivers.
type RepoStore interface {
	CreateRepo(ctx context.Context, repo *Repo) error
	GetRepo(ctx context.Context, id string) (*Repo, error)
	GetRepoByProjectAndPath(ctx context.Context, projectID, relativePath string) (*Repo, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*Repo, error)
	UpdateRepo(ctx context.Context, repo *Repo) error
	DeleteRepo(ctx context.Context, id string) error
}
