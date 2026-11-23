// Copyright (c) 2025 Reliant Labs

// Package projectpurge deletes a project with a restore window (control-plane
// docs/design/unified-shell-and-projects.md §11.3, D18): scheduling hides the
// project and pauses its triggers, a restore undoes that until the purge
// begins, and a purge claims the project and runs its batches. The caller
// decides who may (authz.ProjectDelete); this records and executes it, over
// the store's project_schedule queries (internal/store/project_schedule.go).
// Handlers that held *db.Repo got these from its project_schedule methods.
//
// forge:provided because Deps.DB is an orm.Context, which no graph node
// produces; internal/app's NewProjectPurges provider (providers_worker.go)
// builds it over the repo's ORM, and that one node serves the API's project
// handler and the worker's purge loop.
//
//forge:provided
package projectpurge

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Lease is how long a purge's claim holds a project: the purge worker takes a
// project again only once its claim is older than this, so a purge that
// stopped part way (a replica that crashed, an immediate purge that failed) is
// resumed after it. A purge of the largest project measured (35k chats) ran 17
// minutes in one statement; in batches each is short, so an hour is generous.
const Lease = time.Hour

// Result is what one pass of PurgeDueProjects did.
type Result struct {
	// Purged is how many projects it deleted; Failed how many it could not
	// (each is taken again once its claim's lease runs out).
	Purged, Failed int
	// OrphansSwept is how many rows naming a gone project it removed.
	OrphansSwept int64
}

// Service schedules, restores and purges projects.
type Service interface {
	// ScheduleProjectDeletion hides the project until deleteAfter, when the
	// purge worker deletes it, and pauses its enabled triggers, in one
	// transaction. A project already scheduled keeps its first date. The
	// returned trigger ids are the ones paused, whose schedules the caller
	// converges. decided is the project the caller's authority was decided
	// on; a project that moved org (or changed owner) since is
	// store.ErrOrgChanged, and nothing is written.
	ScheduleProjectDeletion(ctx context.Context, id, by string, deleteAfter time.Time, decided core.ProjectDecision) (core.ProjectDeletion, error)
	// RestoreProject cancels the project's scheduled deletion and turns back
	// on the triggers it paused, returning their ids. A project not scheduled
	// is a no-op; one whose purge began is core.ErrProjectPurgeStarted; one
	// that moved org since decided is store.ErrOrgChanged.
	RestoreProject(ctx context.Context, id string, decided core.ProjectDecision) ([]string, error)
	// ClaimProjectPurgeNow claims decided's owner's project for an immediate
	// purge, scheduled or not: under the owner's org lock and only while the
	// project is still decided's owner's and in decided's org (else
	// store.ErrOrgChanged). From the claim on the project is hidden, can no
	// longer be restored, and is due: PurgeClaimedProject runs its batches,
	// and if they stop part way the purge worker finishes them once the claim
	// is older than Lease. A project that is missing or another user's is
	// core.ErrProjectNotFound.
	ClaimProjectPurgeNow(ctx context.Context, id string, decided core.ProjectDecision) error
	// PurgeClaimedProject runs the batches of a purge ClaimProjectPurgeNow
	// claimed, owner being the owner the claim was decided on. It returns the
	// ids of the triggers it deleted, whose schedules the caller removes. A
	// failure leaves the project hidden and claimed, for the purge worker to
	// resume.
	PurgeClaimedProject(ctx context.Context, id, owner string) ([]string, error)
	// ListDeletedProjects lists the scheduled, restorable projects userID
	// owns or deleted, in org (nil: every org); includePurging adds userID's
	// own projects whose purge has begun.
	ListDeletedProjects(ctx context.Context, userID string, org *core.OrgScope, includePurging bool) ([]*core.Project, error)
	// PurgeDueProjects is one pass of the purge worker at now: it purges up
	// to limit projects whose scheduled deletion is due, each claimed for
	// lease first, then sweeps up to limit rows per table that name a project
	// that is gone. A project that fails is logged and left for a later pass;
	// the error is only a pass that could not list what is due.
	PurgeDueProjects(ctx context.Context, now time.Time, lease time.Duration, limit int) (Result, error)
}

// Deps are the service's collaborators.
type Deps struct {
	DB orm.Context
}

// New builds the service over deps.
func New(deps Deps) Service {
	return &service{db: deps.DB}
}
