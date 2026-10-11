package core

import (
	"context"
	"errors"
	"time"
)

// ErrProjectNotFound is returned when no project matches the id and owner.
// Absent and not-owned are deliberately the same error. A store failure is
// never this error.
// ErrWorktreeNotFound is returned when no worktree matches the id. A store
// failure is never this error.
var ErrWorktreeNotFound = errors.New("worktree not found")

// ErrWorktreeNameTaken is returned when a create or unarchive would give two
// live (unarchived) worktrees of one project the same name. Archived rows do
// not hold a name.
var ErrWorktreeNameTaken = errors.New("a live worktree already has this name")

var ErrProjectNotFound = errors.New("project not found or access denied")

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
	IsForge bool `json:"is_forge"`
	// ForgeProjectName is forge's name for the project — the `name` key in
	// its forge.yaml — and the key the control plane files the project's
	// deploy environments under. Nil when never read (not a forge project, a
	// forge.yaml without a name, or no daemon has reported it yet). Written at
	// create from repo.discover and refreshed by ForgeService.GetTopology.
	ForgeProjectName *string   `json:"forge_project_name,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	LastActive       time.Time `json:"last_active"`
}

// ProjectDaemon records that a daemon has a local clone of a project. A
// single Project may have rows for multiple daemons (desktop + cloud), each
// with its own checkout path. Backs the project/daemon picker.
type ProjectDaemon struct {
	ProjectID string `json:"project_id"`
	DaemonID  string `json:"daemon_id"`
	// Where the checkout lives — or WILL live, while InstallState is
	// ProjectInstallInstalling. A path here is not proof of a checkout.
	Path          string    `json:"path"`
	DefaultBranch *string   `json:"default_branch,omitempty"`
	ClonedAt      time.Time `json:"cloned_at"`
	// InstallState is how far the clone has got. Rows written before this
	// existed read as installed, which is accurate: the old flow only wrote
	// a row once a clone had completed.
	InstallState ProjectInstallState `json:"install_state"`
	// InstallError is why the clone failed; empty unless it did.
	InstallError string `json:"install_error,omitempty"`
	// InstallRequestID ties the row to a queued daemon command, so the
	// outcome notification — which carries no project id — can find it.
	InstallRequestID string `json:"install_request_id,omitempty"`
}

// ProjectInstallState is how far a project's checkout has got on one daemon.
type ProjectInstallState string

const (
	// ProjectInstallInstalling means the clone is queued or running. A
	// daemon that is asleep or booting can hold one of these for a while.
	ProjectInstallInstalling ProjectInstallState = "installing"
	// ProjectInstallInstalled means the checkout exists.
	ProjectInstallInstalled ProjectInstallState = "installed"
	// ProjectInstallFailed means the clone ran and failed.
	ProjectInstallFailed ProjectInstallState = "failed"
)

// CleanupMetadata tracks what became of an archived worktree's directory.
//
// DirectoryDeleted is the settled state: the daemon removed the directory (or
// found it already gone). Until then HeldReason, when set, says why the daemon
// declined to remove it on its own ("dirty", "unpushed", ...), so the storage
// inbox item can list it.
type CleanupMetadata struct {
	DirectoryDeleted bool `json:"directory_deleted"`
	BranchDeleted    bool `json:"branch_deleted"`
	// DeleteBranch asks for the branch to be deleted once the directory is
	// gone: git refuses to delete a branch that is checked out.
	DeleteBranch bool `json:"delete_branch,omitempty"`

	HeldReason string `json:"held_reason,omitempty"`
	HeldDetail string `json:"held_detail,omitempty"`
	// Cleaning is true while a user-confirmed clean-up is running for this
	// worktree. It only counts while CleaningUntil is in the future: the process
	// running it renews the deadline every minute, so one that died (a restart
	// mid-clean-up) stops counting within minutes instead of disabling Clean up
	// until the next sweep. CleaningBy names that process for diagnostics.
	Cleaning      bool       `json:"cleaning,omitempty"`
	CleaningBy    string     `json:"cleaning_by,omitempty"`
	CleaningUntil *time.Time `json:"cleaning_until,omitempty"`
	// RetireFence is set when an unarchive could not reach the machine to retire
	// that archive's fence. The sweep delivers it, and until it does the row is
	// treated as live and nothing is removed.
	RetireFence string `json:"retire_fence,omitempty"`
	// Rechecks counts consecutive checks that found the same hold; the sweep
	// backs off exponentially on it.
	Rechecks int `json:"rechecks,omitempty"`
	// LockedAt is when the daemon last confirmed it holds the reliant lock on a
	// live worktree, so the sweep need not ask again for a day.
	LockedAt *time.Time `json:"locked_at,omitempty"`
	// SizeBytes is the held directory's size as last measured (a lower bound
	// for very large trees).
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// SnapshotRefs are the local refs the user's work was saved to before the
	// directory was removed by a confirmed clean-up.
	SnapshotRefs []string   `json:"snapshot_refs,omitempty"`
	CheckedAt    *time.Time `json:"checked_at,omitempty"`
}

// CleaningNow reports whether a clean-up is genuinely running: flagged, and its
// deadline not yet passed.
func (m *CleanupMetadata) CleaningNow(now time.Time) bool {
	return m != nil && m.Cleaning && m.CleaningUntil != nil && now.Before(*m.CleaningUntil)
}

// ReclaimCandidate is a worktree row a daemon may still hold a directory for,
// with what the server needs to describe it to that daemon.
type ReclaimCandidate struct {
	Worktree    *Worktree
	OwnerUserID string
	ProjectPath string
}

// WorktreePath is the id and directory of one worktree row.
type WorktreePath struct {
	ID   string
	Path string
}

// HeldWorktree is an archived worktree the daemon declined to remove.
type HeldWorktree struct {
	Worktree    *Worktree
	ProjectName string
	ProjectPath string
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
	DaemonID *string
	Status   int32
	// IdempotencyKey de-duplicates retried creates. Creation is asynchronous,
	// so a client that loses the response cannot tell failure from a dropped
	// reply; without this the natural retry produces a second workspace.
	IdempotencyKey  *string
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
	// SetProjectForgeName records forge's name for a project and marks it a
	// forge project. Reports whether the row changed; a repeat with the same
	// name is a no-op, so it is safe to call on every forge read.
	SetProjectForgeName(ctx context.Context, id, userID, forgeProjectName string) (bool, error)
	TouchProject(ctx context.Context, id string, userID string) error
	DeleteProject(ctx context.Context, id string, userID string) error

	// Project ↔ Daemon installations. A row exists for each daemon that has
	// a local clone of the project.
	UpsertProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string) error
	// UpsertQueuedProjectDaemon records a clone that is queued but has not
	// run; path is where the checkout WILL be.
	UpsertQueuedProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string, requestID string) error
	// ResolveQueuedProjectDaemon applies the outcome the daemon reported,
	// keyed by request id. A non-empty installErr marks the row failed.
	ResolveQueuedProjectDaemon(ctx context.Context, requestID, installErr string) error
	// MarkProjectDaemonInstalled settles a queued clone from a filesystem
	// announcement, which names the path but carries no request id.
	MarkProjectDaemonInstalled(ctx context.Context, projectID, daemonID string) error
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*ProjectDaemon, error)
	ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*ProjectDaemon, error)
	DeleteProjectDaemon(ctx context.Context, projectID, daemonID string) error
}

// WorktreeStore is the shared contract for worktree persistence across drivers.
type WorktreeStore interface {
	CreateWorktree(ctx context.Context, worktree *Worktree) error
	GetWorktree(ctx context.Context, id string) (*Worktree, error)
	GetWorktreeByPath(ctx context.Context, path string) (*Worktree, error)
	// GetLiveWorktreeByName returns the project's unarchived worktree called
	// name, or (nil, nil). At most one exists.
	GetLiveWorktreeByName(ctx context.Context, projectID, name string) (*Worktree, error)
	// ListWorktreesByBranch returns every row of the project recorded against
	// the branch, archived ones included.
	ListWorktreesByBranch(ctx context.Context, projectID, branch string) ([]*Worktree, error)
	// GetWorktreeByIdempotencyKey returns a prior create's worktree for this
	// key, or (nil, nil) when there is none.
	GetWorktreeByIdempotencyKey(ctx context.Context, projectID, key string) (*Worktree, error)
	ListWorktrees(ctx context.Context, filters WorktreeFilters) ([]*Worktree, error)
	UpdateWorktree(ctx context.Context, worktree *Worktree) error
	UpdateWorktreeCleanupMetadata(ctx context.Context, id string, metadata *CleanupMetadata) error
	// ListWorktreesForReclaim returns every non-main workspace that may still
	// have a directory on its daemon: archived ones to remove, active ones to
	// lock. Rows whose directory is already settled are excluded.
	ListWorktreesForReclaim(ctx context.Context) ([]*ReclaimCandidate, error)
	// AdoptWorktreeDaemon records daemonID on a row that has none. A no-op when
	// the row already names a daemon.
	AdoptWorktreeDaemon(ctx context.Context, id, daemonID string) error
	// ListLiveWorktreePathsForUser returns the path of every unarchived row the
	// user owns.
	ListLiveWorktreePathsForUser(ctx context.Context, userID string) ([]WorktreePath, error)
	// ListHeldWorktreesForUser returns the user's archived workspaces a daemon
	// has recorded as held (not auto-removed) and not yet deleted.
	ListHeldWorktreesForUser(ctx context.Context, userID string) ([]*HeldWorktree, error)
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
