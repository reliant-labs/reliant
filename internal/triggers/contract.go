// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/store"
)

// The interfaces below are declared HERE, at the consumer, and deliberately
// narrow: *db.Repo, *launch.Launcher and the Temporal client satisfy them
// structurally without knowing this package exists. That is also what lets the
// fire path be tested with in-memory fakes rather than a database.
//
// They are the collaborators the package's components (Syncer, Firer,
// EventFirer, Intake, TriggerPoller, Redriver, Backend) share, so this file is
// the package's forge contract: forge generate writes a mock of each into
// mock_gen.go. There is no single Service to bind: each component has its own
// constructor. Interfaces that belong to one concern stay beside it: the poll
// strategy's Poller, Pollers and PollCredentials, the Reconciler's
// ProjectionStore and ProjectionSyncer, and WorkflowResolver beside the two
// resolvers that implement it.

// OwnerSource is what resolving an activation's owner reads: the owner's
// connection and the GitHub identity the access refresher recorded. It is a
// required part of Repo and EventRepo, so a store that cannot resolve owners
// does not compile (it used to be asserted at run time, and a store without it
// silently gave every owner no GitHub id, which an `owner.github_id` filter
// reads as nobody).
type OwnerSource interface {
	// GetConnection returns the user's connection; a foreign id is an error.
	GetConnection(ctx context.Context, userID, id string) (*core.Connection, error)
	// GetIntegrationAccessSubject is the user's id at the provider as the
	// access refresher last recorded it, or "" when none is fresh.
	GetIntegrationAccessSubject(ctx context.Context, userID, integrationID string, freshAfter time.Time) (string, error)
}

// ReceiptStore records an authenticated delivery's event together with its
// receipt link. Intake requires it: a receipt-bearing Accept used to fail at
// run time with "receipt-aware event store is required".
type ReceiptStore interface {
	TriggerDeliveryEvent(ctx context.Context, id, triggerID, userID string) (*core.TriggerEvent, error)
	RecordDeliveryTriggerEvent(ctx context.Context, id, token string, event *core.TriggerEvent) (stored *core.TriggerEvent, created bool, err error)
}

// IntakeRepo is what Intake needs: the inbound event repository and the
// delivery receipts, both required.
type IntakeRepo interface {
	EventRepo
	ReceiptStore
}

// Repo is the slice of the repository this package reads and writes. The
// signatures match *db.Repo exactly.
type Repo interface {
	OwnerSource
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	ListAllTriggers(ctx context.Context) ([]*core.Trigger, error)
	// LockTrigger row-locks the trigger until the surrounding transaction ends.
	LockTrigger(ctx context.Context, id string) error

	CreateTriggerEvent(ctx context.Context, ev *core.TriggerEvent) (created bool, err error)
	GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error)
	GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error)
	// SettlePendingTriggerEvent settles a still-pending queued event.
	SettlePendingTriggerEvent(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string, def *core.TriggerEventDefinition) (settled bool, err error)

	// GetDaemon reports a missing row as svcerr.NotFound.
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)

	RootWorkflowStatusForChats(ctx context.Context, chatIDs []string) (map[string]core.WorkflowStatus, error)
	// ListRootWorkflows is a chat's root workflows, newest first.
	ListRootWorkflows(ctx context.Context, chatID string) ([]*core.Workflow, error)
	// AbandonFailedWorkflowStart stops a PENDING root whose start is
	// recorded as failed; false means it is not stranded (any more).
	AbandonFailedWorkflowStart(ctx context.Context, id string) (bool, error)
}

// EventRepo is what the inbound path — intake, event fire and redriver —
// reads and writes. *db.Repo satisfies it.
type EventRepo interface {
	OwnerSource
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	CreateTriggerEvent(ctx context.Context, ev *core.TriggerEvent) (created bool, err error)
	GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error)
	// GetLatestTriggerEvent is the trigger's newest event with outcome (any
	// outcome when nil), nil when it has none.
	GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error)
	// SettlePendingTriggerEvent records a verdict, and the definition the
	// fire resolved (nil keeps the recorded one), on a still-pending event.
	SettlePendingTriggerEvent(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string, def *core.TriggerEventDefinition) (settled bool, err error)
	ListStalePendingTriggerEvents(ctx context.Context, olderThan time.Time, limit int) ([]*core.TriggerEvent, error)
}

// TriggerPauser is how the system pauses a trigger that can no longer launch
// (AutoPause). store.TriggerWritesExtStore satisfies it.
type TriggerPauser interface {
	// SystemPauseTrigger disables the trigger with reason (core.TriggerPaused*)
	// and records the pause in its change history; paused is false when it
	// was already paused, and nothing is written.
	SystemPauseTrigger(ctx context.Context, id, reason string) (paused bool, err error)
}

// Launcher is the one door that turns an event plus a spec into a running
// session. Implemented by *launch.Launcher.
type Launcher interface {
	Launch(ctx context.Context, ev launch.Event, spec launch.Spec) (*launch.Result, error)
	// ResolveDefaultWorkflow names the workflow a launch for userID runs when
	// it names none (reqWorkflow empty): the owner's default setting, else the
	// system default. A trigger with no workflow is fired as that workflow, so
	// the firer and the launch agree on what runs.
	ResolveDefaultWorkflow(ctx context.Context, userID, reqWorkflow string) string
}

// ScheduleClient is the Temporal schedule surface the syncer uses. Satisfied
// by client.Client.ScheduleClient().
type ScheduleClient interface {
	Create(ctx context.Context, options client.ScheduleOptions) (client.ScheduleHandle, error)
	List(ctx context.Context, options client.ScheduleListOptions) (client.ScheduleListIterator, error)
	GetHandle(ctx context.Context, scheduleID string) client.ScheduleHandle
}

// WorkflowStarter starts the fire workflow for a manual "run now". Satisfied
// by client.Client.
type WorkflowStarter interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error)
}

// PollRepo is what polling reads and writes. *db.Repo satisfies it.
type PollRepo interface {
	EventRepo
	GetTriggerRegistration(ctx context.Context, triggerID string) (*core.TriggerRegistration, error)
	UpsertTriggerRegistration(ctx context.Context, reg *core.TriggerRegistration) error
}

// FireRepo is what the scheduled firer needs: the trigger repository and the
// capacity store. Both are required, so a firer cannot be built over a store
// that lacks capacity.
type FireRepo interface {
	Repo
	CapacityStore
}

// EventFireRepo is what the inbound firer and the redriver need: the event
// repository and the capacity store, both required.
type EventFireRepo interface {
	EventRepo
	CapacityStore
}

// CapacityStore is every read and write workflow capacity makes: admission,
// slot binding and release, start-fence recovery, expiry and queue settlement.
// It is part of FireRepo and EventFireRepo, which Firer, EventFirer and
// Redriver require: a store missing any of it does not compile, instead of a
// capped trigger quietly skipping admission. Stores satisfies it.
type CapacityStore interface {
	// EnqueueTriggerCapacitySuperseding enrolls ev and returns the waiting
	// events it superseded (`waiting: latest`), settled skipped.
	EnqueueTriggerCapacitySuperseding(ctx context.Context, ev *core.TriggerEvent, scope store.TriggerCapacityScope) ([]*core.TriggerEvent, error)
	CanLaunchTriggerCapacity(ctx context.Context, eventID string, scope store.TriggerCapacityScope, limits store.TriggerCapacityLimits) (bool, error)
	NextAdmissibleTriggerCapacityEvent(ctx context.Context, scope store.TriggerCapacityScope, limits store.TriggerCapacityLimits) (*core.TriggerEvent, error)
	BindTriggerCapacityExecution(ctx context.Context, eventID, workflowID, firstRunID string) error
	PrepareTriggerCapacityExecution(ctx context.Context, eventID, workflowID string) error
	ListTriggerCapacityExecutions(ctx context.Context, scope store.TriggerCapacityScope) ([]store.TriggerCapacityExecution, error)
	ListTriggerCapacityExecutionBatch(ctx context.Context, limit int) ([]store.TriggerCapacityExecution, error)
	ReleaseTriggerCapacityExecution(ctx context.Context, e store.TriggerCapacityExecution) error
	ReleaseClosedTriggerCapacityExecution(ctx context.Context, e store.TriggerCapacityExecution) error
	FenceUnboundTriggerCapacityStart(ctx context.Context, e store.TriggerCapacityExecution) (bool, error)
	ReleaseUnboundTriggerCapacity(ctx context.Context, e store.TriggerCapacityExecution, reason string) (bool, error)
	TriggerCapacityStartFenced(ctx context.Context, e store.TriggerCapacityExecution) (bool, error)
	// ExpirePendingTriggerCapacityEvents settles as failed the waiting events
	// recorded before recordedBefore, independently of the queue heads, and
	// returns them as settled.
	ExpirePendingTriggerCapacityEvents(ctx context.Context, recordedBefore time.Time, detail string, limit int) ([]*core.TriggerEvent, error)
	// The three below are concurrency.cancel_running's: the running runs of a
	// key a newer firing supersedes, the record of which firing did, and the
	// cancelled status of one whose run wrote none.
	ListTriggerCapacityKeyRuns(ctx context.Context, scope store.TriggerCapacityScope, key string, order int64) ([]store.TriggerCapacityKeyRun, error)
	MarkTriggerCapacityRunSuperseded(ctx context.Context, eventID, byEventID string) (bool, error)
	SettleSupersededTriggerCapacityRun(ctx context.Context, eventID string) error
}

// RunCanceller stops one exact execution of a chat's run through the run
// lifecycle's own cancel, the one a user's stop takes
// (runs.Service.TerminateExecution, Terminate addressed to one execution): it
// cancels the run cooperatively, so the run's finally: runs and it records its
// own cancelled status, and hard-terminates only a run that does not
// acknowledge in time. concurrency.cancel_running stops a superseded run with
// it, so there is one way a run is stopped, not a second one for triggers.
//
// It stops runID and nothing else: when the chat's current execution is no
// longer runID (it closed, or a person's later turn replaced it), it refuses
// with an error and stops nothing.
type RunCanceller interface {
	TerminateExecution(ctx context.Context, chatID, workflowID, runID string) error
}

// CapacityDescriber is the Temporal read that reconciles occupied slots: the
// description of a workflow's execution. client.Client satisfies it.
type CapacityDescriber interface {
	DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
}

// RedriveStarter is what the redriver starts fires with and reconciles
// capacity through. client.Client satisfies both.
type RedriveStarter interface {
	WorkflowStarter
	CapacityDescriber
}
