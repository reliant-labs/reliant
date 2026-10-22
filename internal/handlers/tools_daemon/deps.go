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
	"github.com/reliant-labs/reliant/internal/projectfacts"
)

// Repository is everything the daemon gateway reads and writes.
type Repository interface {
	// Daemon rows and what a heartbeat reports about the machine.
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	UpsertDaemon(ctx context.Context, daemon *db.Daemon) error
	GetDaemonStorageState(ctx context.Context, daemonID string) (string, error)
	SetDaemonStorageState(ctx context.Context, daemonID string, stateJSON string) error
	SetDaemonLocalModels(ctx context.Context, daemonID string, inventoryJSON string) error

	// The attachment lease a connected daemon holds on this gateway.
	UpsertDaemonAttachment(ctx context.Context, att *db.DaemonAttachment) error
	RenewDaemonAttachment(ctx context.Context, att *db.DaemonAttachment) (created bool, err error)
	ReleaseDaemonAttachment(ctx context.Context, daemonID string, attachedAt time.Time) error
	UpdateDaemonAttachmentPorts(ctx context.Context, daemonID string, ports []uint32) error
	UpdateDaemonAttachmentMemory(ctx context.Context, daemonID string, usedBytes, limitBytes int64, pressure bool) error

	// Projects a daemon reports, the installs it finishes, and the project
	// config it pushes. GetProjectConfigPushedAt reads the one column the
	// staleness check needs, never the whole (large) record.
	GetProjectByPathAndUser(ctx context.Context, path, userID string) (*db.Project, error)
	CreateProject(ctx context.Context, project *db.Project) error
	MarkProjectDaemonInstalled(ctx context.Context, projectID, daemonID string) error
	ResolveQueuedProjectDaemon(ctx context.Context, requestID, installErr string) error
	UpsertProjectConfigRecord(ctx context.Context, record *db.ProjectConfigRecord) error
	GetProjectConfigPushedAt(ctx context.Context, projectID string) (time.Time, error)

	// What the owner's other clients are told.
	CreateUserUpdate(ctx context.Context, update *db.UserUpdate) error
	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error

	// ListProjects is called directly too; ProjectReconcileStore declares it.
	ProjectReconcileStore
	daemonHostnameLookup
	projectfacts.ForgeNameWriter
}

// ProjectReconcileStore is what a reconcile pass (reconcileProjectDaemons)
// reads and writes: the user's projects, the project_daemons row of each one
// found on the daemon, and the remote_url backfill that respects the
// (user_id, remote_url) unique index.
type ProjectReconcileStore interface {
	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)
	UpsertProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string) error
	GetProjectByRemoteURLAndUser(ctx context.Context, remoteURL, userID string) (*db.Project, error)
	// UpdateProject also serves projectfacts.ReconcileGitRepo.
	projectfacts.ProjectUpdater
}
