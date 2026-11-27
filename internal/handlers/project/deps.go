// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package project

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// OrgResolver answers the org questions of project rpcs. *orgs.Resolver is
// one.
type OrgResolver interface {
	ActingOrg(ctx context.Context, userID, requested string) (string, error)
	ListScope(ctx context.Context, userID, requested string, stamp orgs.Backfill) (*core.OrgScope, error)
	// DefaultOrg is the org the caller acts in when a request names none:
	// "" when they have none or the deployment has no orgs.
	DefaultOrg(ctx context.Context, userID string) (string, error)
}

// Detachments forgets a project's checkout on a machine and reads back what
// was forgotten. store.ProjectDaemonDetachmentExtStore satisfies it.
type Detachments interface {
	// DetachProjectFromDaemon forgets a checkout without touching its files
	// and records the detachment, which ListProjectDaemonDetachmentsForDaemon
	// reads back so the connect heal leaves the pair alone.
	DetachProjectFromDaemon(ctx context.Context, projectID, daemonID, path string, guard func(ctx context.Context) error) error
	ListProjectDaemonDetachmentsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemonDetachment, error)
}

// ProjectLookups finds the user's project in one org (nil scope: any org).
// Projects are unique per (org, user, path) and (org, user, remote_url).
// store.ProjectExtStore satisfies it.
type ProjectLookups interface {
	GetProjectByPathAndUserInScope(ctx context.Context, path, userID string, scope *core.OrgScope) (*core.Project, error)
	GetProjectByRemoteURLAndUserInScope(ctx context.Context, remoteURL, userID string, scope *core.OrgScope) (*core.Project, error)
	// GetProjectByRemoteKeyAndUserInScope matches the repository a remote
	// names, in any form of it (projects.remote_key; not unique).
	GetProjectByRemoteKeyAndUserInScope(ctx context.Context, remoteURL, userID string, scope *core.OrgScope) (*core.Project, error)
	GetProjectBySameRemoteAndUserInScope(ctx context.Context, remoteURL, userID string, scope *core.OrgScope) (*core.Project, error)
}

// DaemonInstalls lists the checkouts a machine holds, and the nested
// repositories being set up with them. store.ProjectDaemonExtStore satisfies
// it.
type DaemonInstalls interface {
	ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemon, error)
	ListProjectDaemonReposForProjects(ctx context.Context, projectIDs []string) ([]*core.ProjectDaemonRepo, error)
}

// ProjectReader reads a project by id. It checks no owner: every caller
// decides the caller's access on the row it returns (authz).
// store.ProjectExtStore satisfies it.
type ProjectReader interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
}

// DaemonReader reads a machine by id. It checks no owner: the caller decides
// whose machine it is (authz.CanWriteMachine). store.DaemonExtStore
// satisfies it.
type DaemonReader interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// RepoLister lists the repos of a project. store.RepoExtStore satisfies it.
type RepoLister interface {
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
}

// Repository is the slice of *db.Repo the project handlers use.
type Repository interface {
	// AssignUnassignedOrg stamps the caller's projects and chats that have
	// no org with their default org (the lazy backfill, orgs.ListScope).
	AssignUnassignedOrg(ctx context.Context, userID, orgID string) error
	CreateProject(ctx context.Context, project *db.Project) error
	GetProjectByPathAndUser(ctx context.Context, path, userID string) (*db.Project, error)
	GetProjectByRemoteURLAndUser(ctx context.Context, remoteURL, userID string) (*db.Project, error)
	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)
	UpdateProject(ctx context.Context, project *db.Project, userID string) error
	SetProjectForgeName(ctx context.Context, id, userID, forgeProjectName string) (bool, error)
	TouchProject(ctx context.Context, id string, userID string) error
	DeleteProject(ctx context.Context, id string, userID string) error
	UpsertProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string) error
	// RecordObservedProjectDaemonInScope records a checkout the platform
	// made or found, only while the project is in the machine's scope
	// (re-read under its row lock) and not detached from it.
	RecordObservedProjectDaemonInScope(ctx context.Context, projectID, daemonID, path string, defaultBranch *string, scope *core.OrgScope) (bool, error)
	UpsertQueuedProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string, requestID string, nestedRepoIDs []string) error
	// IsDaemonIDAttached reports whether the machine is connected now (a
	// fresh attachment lease): PreviewProjectMachineMove asks only a
	// connected machine, and never wakes one.
	IsDaemonIDAttached(ctx context.Context, daemonID string, staleThreshold time.Duration) (bool, error)
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error)
	// GetDaemon completes projectcheckout.Reader: whose machine an install
	// is, and what to call it in a sentence.
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	DeleteProjectDaemon(ctx context.Context, projectID, daemonID string) error
	// BeginCheckoutTeardown and SettleCheckoutTeardown record a remove or
	// reclone of a checkout by the op id its machine acknowledges.
	BeginCheckoutTeardown(ctx context.Context, projectID, daemonID, kind, path string, guard func(ctx context.Context) error) (*db.CheckoutTeardown, error)
	SettleCheckoutTeardown(ctx context.Context, id, state, detail string) (*db.CheckoutTeardown, error)

	CreateRepo(ctx context.Context, repo *core.Repo) error
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	// ListReposByProjects, ListRepoForgeAppsByProjects and
	// ListProjectDaemonsForProjects read a whole project list's repos, forge
	// apps and checkouts in one round trip each (ListProjectsWithApps).
	ListReposByProjects(ctx context.Context, projectIDs []string) ([]*core.Repo, error)
	ListRepoForgeAppsByProjects(ctx context.Context, projectIDs []string) ([]core.RepoForgeApp, error)
	ListProjectDaemonsForProjects(ctx context.Context, projectIDs []string) ([]*core.ProjectDaemon, error)
	// SetRepoForgeApps records the forge apps a daemon found in a repo
	// (projectfacts.RecordForgeApps).
	SetRepoForgeApps(ctx context.Context, repoID string, apps []core.RepoForgeApp, complete bool) error

	CreateWorktree(ctx context.Context, worktree *db.Worktree) error
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	UpdateWorktree(ctx context.Context, worktree *db.Worktree) error
	// ListFailedWorktreesForDaemon and MergeWorktreeCleanupMetadata complete
	// workspacecreate.FailedCreateStore: a connecting machine is asked to
	// remove what its failed creates left behind.
	ListFailedWorktreesForDaemon(ctx context.Context, daemonID string, since time.Time, limit int) ([]*db.Worktree, error)
	MergeWorktreeCleanupMetadata(ctx context.Context, id string, fn func(m *db.CleanupMetadata, archived bool) bool) error

	CreateSetting(ctx context.Context, setting *db.Setting) error
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*db.Setting, error)
	UpdateSetting(ctx context.Context, setting *db.Setting) error
}

// ProjectConfigs seeds a new project's config record. store.ProjectConfigExtStore
// satisfies it.
type ProjectConfigs interface {
	UpsertProjectConfigRecord(ctx context.Context, rec *core.ProjectConfigRecord) error
}

// ProjectPurges is a project's deletion with a restore window
// (project_delete.go): schedule it (hiding the project and pausing its
// triggers), cancel it, purge at once, and list what the caller may restore.
// Each takes the project as the caller's authority was decided on, and
// refuses (store.ErrOrgChanged) a project that moved org since. An immediate
// purge is claimed in the request and its batches run after it returns
// (rpc_purge_project_now.go). projectpurge.Service satisfies it.
type ProjectPurges interface {
	ScheduleProjectDeletion(ctx context.Context, id, by string, deleteAfter time.Time, decided core.ProjectDecision) (core.ProjectDeletion, error)
	RestoreProject(ctx context.Context, id string, decided core.ProjectDecision) ([]string, error)
	ClaimProjectPurgeNow(ctx context.Context, id string, decided core.ProjectDecision) error
	PurgeClaimedProject(ctx context.Context, id, owner string) ([]string, error)
	ListDeletedProjects(ctx context.Context, userID string, org *core.OrgScope, includePurging bool) ([]*core.Project, error)
}

// MoveLedger is a project move's ledger and preview reads. A move is recorded
// (idempotently by the actor's request key) before its saga starts, and read
// back as it runs; the preview is the running work that refuses a move now and
// what it would change. None takes an owner: the handler decides who may read
// or start a move (authz) around each call. store.ProjectOrgMoveExtStore
// satisfies it.
type MoveLedger interface {
	ProjectOrgMoveBlockers(ctx context.Context, projectID string) (*core.ProjectOrgMoveBlockers, error)
	ProjectOrgMoveEffects(ctx context.Context, projectID, toOrgID string) (*core.ProjectOrgMoveEffects, error)
	InsertProjectOrgMove(ctx context.Context, m *core.ProjectOrgMove) (*core.ProjectOrgMove, bool, error)
	FindProjectOrgMoveByRequest(ctx context.Context, actorUserID, requestKey string) (*core.ProjectOrgMove, error)
	GetProjectOrgMove(ctx context.Context, id string) (*core.ProjectOrgMove, error)
	ListProjectOrgMoves(ctx context.Context, f core.ProjectOrgMoveFilters) ([]*core.ProjectOrgMove, error)
	// ProjectPipelineEnrolled is whether a repository of the project is
	// enrolled in the pipeline with a mode other than off (G1).
	ProjectPipelineEnrolled(ctx context.Context, projectID string) (bool, error)
}

// SharedProjects is the projects others shared with an org the caller is in
// (the sharing service's list; ListProjectsWithApps). The caller passes them
// through authz. store.ShareEventExtStore satisfies it.
type SharedProjects interface {
	ListSharedProjects(ctx context.Context, f core.SharedProjectFilters) ([]*core.Project, error)
}

// Router sends commands to a user's daemon. toolexec.DaemonRouter
// implements it.
type Router interface {
	// SendDaemonCommand runs one request/response command on the daemon the
	// user's commands route to.
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// SendDaemonCommandToDaemon runs one on a specific daemon (reading a
	// fresh clone's forge name).
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// PlacementDaemonID ranks the machines holding a project's checkout the
	// way the project's other work is placed (projectcheckout.Resolve).
	PlacementDaemonID(ctx context.Context, userID, daemonID string) (string, error)
	// ResolveDaemonID names the daemon SendDaemonCommand would route to.
	ResolveDaemonID(ctx context.Context, userID string) (string, error)
	// EnqueueDaemonCommand queues a command for the user's daemons'
	// next connect.
	EnqueueDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) (int, error)
	SendLoadProjectConfigs(ctx context.Context, userID, projectPath, requestID string) error
	SendWatchProjectConfigs(ctx context.Context, userID, projectPath string, includeInitial bool) error
}

// MachineConfigSync is a Router that can also ask ONE named machine to load
// and watch a project's config: a checkout recorded on that machine is synced
// from there. toolexec.NATSDaemonRouter is one. A router that is not has the
// machine sync on its next connect, whose registration names every checkout
// it holds.
type MachineConfigSync interface {
	SendLoadProjectConfigsToDaemon(ctx context.Context, userID, daemonID, projectPath, requestID string) error
	SendWatchProjectConfigsToDaemon(ctx context.Context, userID, daemonID, projectPath string, includeInitial bool) error
}

// TriggerSchedules converges a trigger's Temporal Schedule onto its row (a
// missing row removes it). *triggers.Syncer is one.
type TriggerSchedules interface {
	Sync(ctx context.Context, triggerID string) error
}

// CloneDispatcher asks the control plane to clone a repository onto one of
// the caller's daemons. controlplane.Service implements it.
type CloneDispatcher interface {
	CloneRepoOntoDaemon(ctx context.Context, jwt string, req controlplane.CloneRepoRequest) (controlplane.CloneRepoResult, error)
}

// HostedApps reads an org's hosted apps from the control plane as the caller,
// with their own bearer (ListProjectsWithApps). controlplane.Service is one.
type HostedApps interface {
	ListHostedApps(ctx context.Context, jwt, orgID string) ([]controlplane.HostedApp, error)
}

// MoveStarter starts a project move's saga, keyed by the move id: starting a
// move that already started is not an error.
type MoveStarter interface {
	StartProjectOrgMove(ctx context.Context, moveID string) error
}

// MoveFacts reads control-plane's half of a move preview (secret access,
// hosted projects, the target's funding, the machines' orgs).
// controlplane.Service is one.
type MoveFacts interface {
	PreviewProjectOrgMove(ctx context.Context, q controlplane.ProjectOrgMovePreviewQuery) (*controlplane.ProjectOrgMovePreview, error)
}
