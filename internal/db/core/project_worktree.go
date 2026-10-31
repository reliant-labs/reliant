package core

import (
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// ErrProjectNotFound is returned when no project matches the id and owner.
// Absent and not-owned are deliberately the same error. A store failure is
// never this error.
// ErrWorktreeNotFound is returned when no worktree matches the id. A store
// failure is never this error.
var ErrWorktreeNotFound = svcerr.NotFound("worktree")

// ErrWorktreeNameTaken is returned when a create or unarchive would give two
// live (unarchived) worktrees of one project the same name. Archived rows do
// not hold a name.
var ErrWorktreeNameTaken = svcerr.AlreadyExists("a live worktree already has this name")

// ErrProjectNotFound covers a missing project and one the caller may not see
// alike, so ids are not an oracle. A svcerr NotFound with its own sentence.
var ErrProjectNotFound = svcerr.WithMessage(svcerr.ErrNotFound, "project not found or access denied")

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
	// Transient is the last outcome that was neither a removal nor a hold
	// (recently_active, stale): not shown to the user, but it makes the sweep
	// wait before asking again, and CheckedAt orders the queue so a row that
	// was just asked about goes behind the ones that were not.
	Transient string `json:"transient,omitempty"`
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
	// StrippedPaths and StrippedBytes record the regenerable files
	// (node_modules, build output, tool caches) the machine deleted from a
	// held directory while keeping its work; restoring it needs the install
	// or generate step run again. StrippedBytes is a lower bound, summed over
	// every pass.
	StrippedPaths []string `json:"stripped_paths,omitempty"`
	StrippedBytes int64    `json:"stripped_bytes,omitempty"`
	// LandedAt is when the daemon first reported a live worktree's work
	// landed (merged into its base branch, nothing else in it); cleared when
	// a later check finds new work. ArchivedBecause says why the sweep
	// archived a row on the user's behalf ("landed"); empty for an archive
	// the user made.
	LandedAt        *time.Time `json:"landed_at,omitempty"`
	ArchivedBecause string     `json:"archived_because,omitempty"`
}

// ArchivedBecauseLanded marks a worktree the sweep archived because its work
// had landed and its chats had gone quiet. Its chats stay live; the first
// tool batch one of them runs brings the worktree back.
const ArchivedBecauseLanded = "landed"

// WorktreeRunActivity is what the chats working in a worktree say about it:
// whether any run is going (pending, active, or paused awaiting a resume) and
// when anything last happened in it. A chat works in a worktree when it is
// bound to it, or one of its threads is.
type WorktreeRunActivity struct {
	Running    bool
	LastActive time.Time
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

// WorktreePath is the id and directory of one worktree row, and the project
// whose repos say where its checkouts sit under that directory.
type WorktreePath struct {
	ID        string
	Path      string
	ProjectID string
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
	// FailureReason says, in words the user can act on, why creation failed
	// — set only with a FAILED status, and only for a reason that is the
	// user's to fix (internal/workspacecreate). Empty otherwise.
	FailureReason string
	// ActivityGeneration changes on every admitted server activity. Finalizing
	// rejects new admissions until the exact journal is reconciled/cancelled.
	ActivityGeneration   int64
	RecoveryFinalizingID *string
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
	ProjectID *string
	ChatID    *string
	Status    *int32
	// IncludeArchived adds archived rows after the live ones.
	IncludeArchived bool
	// ArchivedOnly lists only archived rows, most recently archived first.
	// It takes precedence over IncludeArchived.
	ArchivedOnly bool
	// MainOnly narrows to the project's main workspace.
	MainOnly bool
	Limit    int
	Offset   int
}
