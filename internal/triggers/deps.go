// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"

	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
)

// The interfaces below are declared HERE, at the consumer, and deliberately
// narrow: *db.Repo, *launch.Launcher and the Temporal client satisfy them
// structurally without knowing this package exists. That is also what lets the
// fire path be tested with in-memory fakes rather than a database.

// Repo is the slice of the repository this package reads and writes. The
// signatures match db.Repository exactly.
type Repo interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	ListAllTriggers(ctx context.Context) ([]*core.Trigger, error)
	// LockTrigger row-locks the trigger until the surrounding transaction ends.
	LockTrigger(ctx context.Context, id string) error

	CreateTriggerEvent(ctx context.Context, ev *core.TriggerEvent) (created bool, err error)
	GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error)
	GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error)

	// GetDaemon reports a missing row as sql.ErrNoRows.
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)

	GetRootWorkflowStatusForChats(ctx context.Context, chatIDs []string) (map[string]core.WorkflowStatus, error)
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
