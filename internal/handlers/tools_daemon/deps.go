// forge:deps — consumer-declared interfaces for this package's Deps.
// Repository is this package's own declaration of what the daemon gateway
// reads and writes; mock_gen.go mocks it. *db.Repo satisfies it, and the
// composition root hands it to New.
//
// It lists the methods the handlers call directly, then embeds the role of
// every helper and callee they hand the repository to (the project reconcile
// pass, the hostname lookup, projectfacts), so a callee that needs more points
// the compiler here.
package tools_daemon

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectfacts"
)

// Repository is everything the daemon gateway reads and writes.
type Repository interface {
	// Daemon rows and what a heartbeat reports about the machine.
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	UpsertDaemon(ctx context.Context, daemon *db.Daemon) error
	// MergeDeclaredDaemonLabels merges the durable labels a daemon declared
	// at registration into its row's labels; false when it wrote nothing.
	MergeDeclaredDaemonLabels(ctx context.Context, userID, daemonID string, boundCredential bool, labels map[string]string, maxLabels int) (bool, error)
	GetDaemonStorageState(ctx context.Context, daemonID string) (string, error)
	SetDaemonStorageState(ctx context.Context, daemonID string, stateJSON string) error

	// The attachment lease a connected daemon holds on this gateway is the
	// Attachments role; UpsertDaemonAttachment stays here for now.
	UpsertDaemonAttachment(ctx context.Context, att *db.DaemonAttachment) error

	// Projects a daemon reports, the installs it finishes, and the project
	// config it pushes. GetProjectMachineCatalogPushedAt reads the one column
	// the staleness check needs, never the whole (large) record.
	// GetProjectByPathAndUserInScope resolves a reported path in the
	// machine's org (nil scope: any org): the same path may be a project in
	// each of the user's orgs.
	GetProjectByPathAndUserInScope(ctx context.Context, path, userID string, scope *core.OrgScope) (*db.Project, error)
	// GetProjectWithUserCheck resolves the project of the machine's own
	// checkout at a path (ensureOwnedProjectForPath).
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	CreateProject(ctx context.Context, project *db.Project) error
	// WithMachineCheckoutsLocked runs fn under the lock a detach from the
	// machine takes, so a reported path is resolved against the detach
	// whole or not at all (ensureOwnedProjectForPath).
	WithMachineCheckoutsLocked(ctx context.Context, daemonID string, fn func(ctx context.Context) error) error
	ResolveQueuedProjectDaemon(ctx context.Context, requestID, installErr string) error
	SyncProjectConfigSnapshot(ctx context.Context, record *db.ProjectConfigRecord, force bool, scope *core.OrgScope) (bool, error)
	GetProjectMachineCatalogPushedAt(ctx context.Context, projectID, daemonID string) (time.Time, error)

	// What the owner's other clients are told.
	CreateUserUpdate(ctx context.Context, update *db.UserUpdate) error
	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error

	// ListProjects is called directly too; ProjectReconcileStore declares it.
	ProjectReconcileStore
	CheckoutRestoreStore
	daemonHostnameLookup
	projectfacts.ForgeNameWriter
}

// ProjectReconcileStore is what a reconcile pass (reconcileProjectDaemons)
// reads and writes: the user's projects, the project_daemons row of each one
// found on the daemon, and the remote_url backfill that respects the
// (user_id, remote_url) unique index.
type ProjectReconcileStore interface {
	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)
	// ListProjectDaemonsForDaemon is what the machine already has on record,
	// which a creation-path probe never overrules.
	ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemon, error)
	// StartQueuedProjectDaemonClones starts the stuck-clone clock of the
	// clones queued while the machine was away: they run from this connect.
	StartQueuedProjectDaemonClones(ctx context.Context, daemonID string) error
	// RecordObservedProjectDaemonInScope records a checkout found on the
	// machine, never over a detachment of the pair, and only while the
	// project's org, re-read under its row lock, is in the machine's scope
	// (a move out of it during the probe is seen); false otherwise.
	RecordObservedProjectDaemonInScope(ctx context.Context, projectID, daemonID, path string, defaultBranch *string, scope *core.OrgScope) (bool, error)
	// MarkProjectDaemonInstalledInScope and FailProjectDaemonInstallInScope
	// settle a clone whose outcome was lost, from the row's own path; the
	// first also settles one from the machine's filesystem announcement.
	// Like RecordObservedProjectDaemonInScope, each re-reads the project's
	// org under its row lock and settles nothing on a project no longer in
	// the machine's scope; true only when it settled (the first: the
	// checkout is installed now).
	MarkProjectDaemonInstalledInScope(ctx context.Context, projectID, daemonID string, scope *core.OrgScope) (bool, error)
	FailProjectDaemonInstallInScope(ctx context.Context, projectID, daemonID, installErr string, scope *core.OrgScope) (bool, error)
	// GetProjectByRemoteURLAndUserInScope finds the user's project of a
	// remote in one org (nil scope: any): the remote backfill's uniqueness
	// check, per (org, user, remote_url).
	GetProjectByRemoteURLAndUserInScope(ctx context.Context, remoteURL, userID string, scope *core.OrgScope) (*db.Project, error)
	// DetachProjectDaemonOfOtherOrg removes a checkout record from the
	// machine when its project is, under its row lock, still in another org,
	// recording it whole so it can be restored: the reconcile's machine-org
	// cleanup.
	DetachProjectDaemonOfOtherOrg(ctx context.Context, projectID, daemonID, userID, machineOrg string) (bool, error)
	// ListProjectDaemonDetachmentsForDaemon is what the user detached from
	// the machine: a folder there is left unrecorded (DetachProjectFromDaemon).
	ListProjectDaemonDetachmentsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemonDetachment, error)
	// UpdateProject also serves projectfacts.ReconcileGitRepo.
	projectfacts.ProjectUpdater
	// ForgeAppsStore records the forge apps the reconcile probe reports for
	// each repo of a checkout it found (projectfacts.RecordForgeApps).
	projectfacts.ForgeAppsStore
}

// Attachments is the lease a connected daemon holds on this gateway and what
// its heartbeats record on it. store.DaemonAttachmentExtStore satisfies it.
type Attachments interface {
	// RenewDaemonAttachment renews the lease of a stream the caller is
	// holding right now, re-creating the row if it is gone, and reports
	// whether it had to.
	RenewDaemonAttachment(ctx context.Context, att *core.DaemonAttachment) (created bool, err error)
	// ReleaseDaemonAttachment deletes the lease a session created, never a
	// newer session's.
	ReleaseDaemonAttachment(ctx context.Context, daemonID string, attachedAt time.Time) error
	SetDaemonAttachmentPorts(ctx context.Context, daemonID string, ports []uint32) error
	SetDaemonAttachmentMemory(ctx context.Context, daemonID string, usedBytes, limitBytes int64, pressure bool) error
	SetDaemonAttachmentHealth(ctx context.Context, daemonID string, health core.MachineHealth) error
}

// LocalModels stores the model inventory a daemon publishes.
// store.DaemonExtStore satisfies it.
type LocalModels interface {
	SetDaemonLocalModels(ctx context.Context, daemonID, inventoryJSON string) error
}
