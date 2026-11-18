// Copyright (c) 2025 Reliant Labs

// Package workflowcheck owns the GitHub check run a workflow's `check:` block
// declares (reliantv1.WorkflowCheck): one check per firing, created queued
// when the firing resolves the workflow, in_progress when the run's first
// node starts, and concluded on every ending.
//
// The lifecycle is level-triggered, not a chain of events. Three writers
// record facts in one row per check (workflow_check_runs):
//
//   - the trigger's fire activity opens it (Open), before the run launches or
//     waits for capacity, so a queued firing already shows on GitHub;
//   - the run stamps it when its first node starts (Started) and when it
//     ends, with its verdict and check output (Ended);
//   - nothing else. The firing's outcome and the run's workflow row are read,
//     never written, by this package.
//
// The syncer (SyncOnce, run by Syncer) then DERIVES what the check should say from the
// firing and its run (derive) and writes the difference to GitHub as the
// trigger's GitHub identity, through the catalog's check_run actions. A
// write that fails is retried with backoff; a run that died without saying
// so is still concluded from its workflow row, and a check nothing concluded
// times out. So every ending concludes the check: success, failure,
// cancelled (skipped, superseded), timed_out, an engine fault (failure,
// titled "Reliant could not run this check"), never neutral or skipped.
//
// Built by hand, by internal/app's NewWorkflowChecks provider
// (providers_worker.go): never a node compose.go constructs from Deps.
//
//forge:provided
package workflowcheck

import (
	"context"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// Service is the engine side of `check:`.
type Service interface {
	// Open records the check of one firing, when its workflow declares one.
	// It is idempotent per firing: a retried or resumed fire finds the row
	// it wrote. A firing whose event names no repository or commit gets no
	// check (logged); only a store failure is an error.
	Open(ctx context.Context, f Firing) error
	// Started records that the launch run of chatID started its first node.
	Started(ctx context.Context, chatID string) error
	// Ended records how the launch run of chatID saw its own end. The first
	// report stands: a retried report changes nothing.
	Ended(ctx context.Context, chatID string, ending core.WorkflowCheckEnding) error
	// SyncOnce writes every due check to GitHub and returns how many it
	// synced. Syncer runs it on an interval.
	SyncOnce(ctx context.Context) (int, error)
}

// Firing is one firing of a trigger whose workflow may declare a check.
type Firing struct {
	Workflow *reliantv1.Workflow
	Trigger  *core.Trigger
	Event    *core.TriggerEvent
	// Scope is the `trigger` root the run will see, for check.repository
	// and check.head_sha.
	Scope triggerspec.Scope
}

// Store is what the service reads and writes. *db.Repo satisfies it.
type Store interface {
	// CreateWorkflowCheckRun inserts row; false when the firing already has
	// a check of that name.
	CreateWorkflowCheckRun(ctx context.Context, row *core.WorkflowCheckRun) (bool, error)
	// MarkWorkflowCheckRunsStarted stamps the checks of chatID's launch
	// firing as started (the first stamp stands) and makes them due.
	MarkWorkflowCheckRunsStarted(ctx context.Context, chatID string, at time.Time) error
	// RecordWorkflowCheckRunsEnded records how chatID's launch run ended on
	// its firing's checks (the first record stands) and makes them due.
	RecordWorkflowCheckRunsEnded(ctx context.Context, chatID string, ending core.WorkflowCheckEnding, at time.Time) error
	// ClaimWorkflowCheckRuns leases up to max due, unfinished checks.
	ClaimWorkflowCheckRuns(ctx context.Context, now, leaseUntil time.Time, max int) ([]*core.WorkflowCheckRun, error)
	// ObserveWorkflowCheckRun reads a check's firing and its run;
	// core.ErrWorkflowCheckRunNotFound when the firing is gone.
	ObserveWorkflowCheckRun(ctx context.Context, triggerEventID string) (*core.WorkflowCheckObservation, error)
	// SaveWorkflowCheckRunSync writes a sync's result (the GitHub fields, the
	// queue fields, paused_since and finished_at) and releases the lease.
	SaveWorkflowCheckRunSync(ctx context.Context, row *core.WorkflowCheckRun) error
}

// Identity is who a check is written as: the trigger's owner, org, acting
// identity and name.
type Identity struct {
	UserID         string
	OrgID          string
	ActingIdentity string
	Automation     string
}

// GitHub opens one repository's check runs for writing as an identity.
type GitHub interface {
	CheckRuns(ctx context.Context, id Identity, owner, repo string) (CheckRuns, error)
}

// CheckRuns writes one repository's check runs.
type CheckRuns interface {
	// Find returns the id of the check run named name on headSHA whose
	// external_id is externalID, 0 when there is none: a create whose answer
	// was lost.
	Find(ctx context.Context, headSHA, name, externalID string) (int64, error)
	Create(ctx context.Context, w Write) (int64, error)
	Update(ctx context.Context, id int64, w Write) error
}

// Write is one check run write. Name and HeadSHA are for a create only.
type Write struct {
	Name, HeadSHA string
	Status        string
	Conclusion    string
	ExternalID    string
	DetailsURL    string
	StartedAt     *time.Time
	CompletedAt   *time.Time
	Output        *Output
}

// Deps are the service's collaborators.
type Deps struct {
	Store Store
	// GitHub writes check runs; nil makes SyncOnce refuse (a process that
	// only records, such as a test of the fire path).
	GitHub GitHub
	// RunURL is a run's page, the check's details_url; nil (or "") links
	// nothing.
	RunURL func(chatID string) string
}

// HostedRunURL is a run's page on the hosted web app, the details_url of the
// checks a hosted worker writes.
func HostedRunURL(chatID string) string {
	return "https://app.reliantlabs.io/workflows/runs/" + chatID
}

// New builds the service.
func New(deps Deps) Service {
	return newService(deps)
}
