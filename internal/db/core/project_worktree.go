package core

import (
	"context"
	"time"
)

// Project represents a code repository.
//
// RemoteURL is the canonical git remote URL (e.g.
// "https://github.com/foo/bar.git") used to identify the project across
// daemons. Two clones of the same remote on different daemons collapse into
// one Project row; per-daemon checkout paths live in project_daemons. Nil for
// non-git projects, or projects whose remote has not yet been resolved.
type Project struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Path          string  `json:"path"`
	UserID        string  `json:"user_id"`
	Description   *string `json:"description,omitempty"`
	IsGitRepo     bool    `json:"is_git_repo"`
	DefaultBranch *string `json:"default_branch,omitempty"`
	RemoteURL     *string `json:"remote_url,omitempty"`
	// IsForge is true when the project's repo root contains a forge.yaml.
	// Set by the project lifecycle when a clone / create happens; not lazily
	// recomputed on read. See [internal/skills/catalog/forge.go] for the
	// canonical detection check.
	IsForge    bool      `json:"is_forge"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	LastActive time.Time `json:"last_active"`
}

// ProjectDaemon records that a daemon has a local clone of a project. A
// single Project may have rows for multiple daemons (desktop + cloud), each
// with its own checkout path. Backs the project/daemon picker.
type ProjectDaemon struct {
	ProjectID     string    `json:"project_id"`
	DaemonID      string    `json:"daemon_id"`
	Path          string    `json:"path"`
	DefaultBranch *string   `json:"default_branch,omitempty"`
	ClonedAt      time.Time `json:"cloned_at"`
}

// CleanupMetadata tracks what was cleaned up when archiving a worktree.
type CleanupMetadata struct {
	DirectoryDeleted bool `json:"directory_deleted"`
	BranchDeleted    bool `json:"branch_deleted"`
}

// Worktree represents a workspace-level git worktree.
//
// In the multi-repo model, a Worktree spans the entire project workspace, not
// a single nested repo. Path points at a workspace directory (e.g.
// ~/.reliant/worktrees/<id>/) that contains N nested git-worktree checkouts —
// one per Repo, at <Path>/<repo.relative_path>/. There is intentionally no
// RepoID column: a chat operates at workspace root and uses the per-tool
// `repo` param to scope tool calls to a specific nested repo. The `repos`
// table tracks which repos belong to a project; the worktree row is the
// per-feature workspace identity.
//
// Branch is the creation-time branch — useful as a label for display and for
// archive cleanup, but NOT a source of truth for write operations. The user
// may check out a different branch in any nested repo via plain git, and a
// single Worktree row cannot represent N divergent branches anyway. All
// write paths (push, pull, create-PR) resolve the branch from HEAD on the
// resolved checkout dir at op time.
//
// BaseBranch is the legacy single base branch (single-repo projects use it as
// canonical). BaseBranches overrides on a per-repo basis for multi-repo
// workspaces, where repo A may default to `main` and repo B to
// `master`/`develop`. Lookup order at PR creation time:
//
//	worktree.BaseBranches[repo_id] -> worktree.BaseBranch -> daemon
//	auto-detect (gh -> git remote show -> main/master probe).
type Worktree struct {
	ID           string
	Name         string
	Path         string
	Branch       string
	BaseBranch   string
	BaseBranches map[string]string
	ProjectID    string
	ChatID       *string
	// DaemonID is the daemon that physically created and owns this worktree's
	// on-disk git checkouts (~/.reliant/worktrees/<id>/). Tool execution for a
	// worktree-bound chat must route to this daemon; the path exists nowhere
	// else. Nil for pre-existing rows and single-daemon setups, in which case
	// callers fall back to default daemon resolution.
	DaemonID        *string
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
	GetProjectByPathAndUser(ctx context.Context, path, userID string) (*Project, error)
	GetProjectByRemoteURLAndUser(ctx context.Context, remoteURL, userID string) (*Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*Project, error)
	ListProjects(ctx context.Context, filters ProjectFilters) ([]*Project, error)
	UpdateProject(ctx context.Context, project *Project, userID string) error
	TouchProject(ctx context.Context, id string, userID string) error
	DeleteProject(ctx context.Context, id string, userID string) error

	// Project ↔ Daemon installations. A row exists for each daemon that has
	// a local clone of the project.
	UpsertProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string) error
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*ProjectDaemon, error)
	ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*ProjectDaemon, error)
	DeleteProjectDaemon(ctx context.Context, projectID, daemonID string) error
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
