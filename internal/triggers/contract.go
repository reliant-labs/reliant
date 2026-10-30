// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
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

// Repo is the slice of the repository this package reads and writes. The
// signatures match *db.Repo exactly.
type Repo interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	ListAllTriggers(ctx context.Context) ([]*core.Trigger, error)
	// LockTrigger row-locks the trigger until the surrounding transaction ends.
	LockTrigger(ctx context.Context, id string) error

	CreateTriggerEvent(ctx context.Context, ev *core.TriggerEvent) (created bool, err error)
	GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error)
	GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error)
	// SetTriggerEventDefinition records which workflow definition a firing
	// resolved, on a row written before the fire decided.
	SetTriggerEventDefinition(ctx context.Context, id string, def *core.TriggerEventDefinition) error

	// GetDaemon reports a missing row as sql.ErrNoRows.
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)

	GetRootWorkflowStatusForChats(ctx context.Context, chatIDs []string) (map[string]core.WorkflowStatus, error)
	// ListRootWorkflows is a chat's root workflows, newest first.
	ListRootWorkflows(ctx context.Context, chatID string) ([]*core.Workflow, error)
	// AbandonFailedWorkflowStart stops a PENDING root whose start is
	// recorded as failed; false means it is not stranded (any more).
	AbandonFailedWorkflowStart(ctx context.Context, id string) (bool, error)
}

// EventRepo is what the inbound path — intake, event fire and redriver —
// reads and writes. *db.Repo satisfies it.
type EventRepo interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	CreateTriggerEvent(ctx context.Context, ev *core.TriggerEvent) (created bool, err error)
	GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error)
	SettlePendingTriggerEvent(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string) (settled bool, err error)
	// SetTriggerEventDefinition records which workflow definition an event's
	// fire resolved.
	SetTriggerEventDefinition(ctx context.Context, id string, def *core.TriggerEventDefinition) error
	ListStalePendingTriggerEvents(ctx context.Context, olderThan time.Time, limit int) ([]*core.TriggerEvent, error)
}

// Launcher is the one door that turns an event plus a spec into a running
// session. Implemented by *launch.Launcher.
type Launcher interface {
	Launch(ctx context.Context, ev launch.Event, spec launch.Spec) (*launch.Result, error)
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
	// GetConnection returns the user's connection; a foreign id is an error.
	GetConnection(ctx context.Context, userID, id string) (*core.Connection, error)
}
