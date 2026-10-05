// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/instanceid"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/workflowevent"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	v2activities "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// maxHeartbeatThrottleInterval is worker.Options.MaxHeartbeatThrottleInterval.
// Pulled out as a named constant, rather than inlined in workerOpts below, so
// TestMaxHeartbeatThrottleInterval can pin it: this one value sets BOTH the
// heartbeat RPC's own deadline and how fast a pending Temporal cancellation
// reaches a running activity, and those two pull in opposite directions. See
// the comment on its use below before changing it.
const maxHeartbeatThrottleInterval = 2 * time.Second

// Config holds the dependencies needed to create and start a Temporal worker.
type Config struct {
	// Required dependencies
	TemporalClient client.Client
	Database       db.Repository
	StreamingHub   streaming.StreamingHub
	ToolsFactory   *tools.ToolsFactory
	ToolExecutor   toolexec.ToolExecutor
	DaemonRouter   toolexec.DaemonRouter // Routes commands to user's daemon (nil = worktree ops unavailable)
	MCPBinder      toolexec.MCPContextBinder
	ConfigProvider config.ConfigProvider

	// TriggerLauncher is the one door a scheduled fire launches through,
	// normally *launch.Launcher.
	//
	// INJECTED rather than constructed here on purpose. launch.NewLauncher
	// needs a run recorder and a daemon prober that this package does not
	// have and should not acquire — the api-server and the worker build them
	// differently, and a test wants neither. nil registers the schedule
	// workflow but not its activity, so a fire that arrives at a worker with
	// no launcher fails loudly and is retried on one that has it, instead of
	// being silently dropped.
	TriggerLauncher triggers.Launcher

	// TriggerPollers looks up a polled integration's poll function. nil
	// registers the poll workflow without its activity: a poll that reaches
	// this worker retries until one that has it picks it up.
	TriggerPollers triggers.Pollers

	// TriggerCredentials resolves a polled trigger's credential from the
	// trigger row (owner and connection), for the poll activity. Normally the
	// same connauth.Source as IntegrationCredentials. nil skips every poll
	// rather than letting a poller run unauthenticated.
	TriggerCredentials triggers.PollCredentials

	// IntegrationCredentials authenticates the action node's integration
	// calls (research/CONNECTIONS_VAULT.md §3.1): saved connections, plus
	// control-plane-delegated GitHub tokens when hosted. It exists on the
	// worker only: the worker is the one reader of connection secrets. It is
	// the SAME source the http__request tool uses, so the two doors resolve
	// a credential identically. nil refuses every authenticated call.
	IntegrationCredentials httpaction.CredentialSource

	// Optional overrides (for testing)
	RunExecutorOverride handlers.RunExecutor
	DriverResolver      drivers.DriverResolver // Custom LLM driver resolver (nil = production default)

	// Task queue configuration
	TaskQueueSuffix string // Optional suffix for test isolation
}

// Handle holds a running worker and its lifecycle.
type Handle struct {
	Worker worker.Worker
	Done   chan struct{} // Closed when worker exits
	Err    error         // Set to the error from worker.Run, if any
}

// taskQueueName returns the full task queue name with optional suffix.
func (c *Config) taskQueueName() string {
	base := v2workflow.SharedTaskQueue
	if c.TaskQueueSuffix == "" {
		return base
	}
	return base + "-" + c.TaskQueueSuffix
}

// StartWorker creates, registers, and starts a Temporal worker.
// The returned Handle can be used to wait for shutdown via handle.Done.
// Call handle.Worker.Stop() to initiate graceful shutdown.
func StartWorker(cfg *Config) (*Handle, *v2.ActivityRegistry, error) {
	// Create activity registry
	registry := v2.NewActivityRegistry(cfg.Database)

	// Create threads service
	threadsService := threads.NewService(cfg.Database)

	// Create activity dependencies
	activityDeps := v2activities.NewActivities(
		cfg.Database,
		cfg.StreamingHub,
		threadsService,
		cfg.ToolsFactory,
		cfg.ToolExecutor,
		cfg.DaemonRouter,
		cfg.MCPBinder,
		cfg.TemporalClient,
		cfg.ConfigProvider,
	)

	// Apply overrides for testing
	if cfg.RunExecutorOverride != nil {
		activityDeps.RunExecutor = cfg.RunExecutorOverride
	}
	if cfg.DriverResolver != nil {
		activityDeps.DriverResolver = cfg.DriverResolver
	}

	activityDeps.Connections = cfg.IntegrationCredentials

	// Register all activities
	v2activities.RegisterAll(registry, activityDeps)

	// Create shared worker with standard options
	workerOpts := worker.Options{
		StickyScheduleToStartTimeout:     5 * time.Second,
		MaxConcurrentWorkflowTaskPollers: 5,
		MaxConcurrentActivityTaskPollers: 10,
		WorkerStopTimeout:                5 * time.Second,
		// This is the SDK's heartbeat batching window. Ticks from
		// ActivityWrapper's background heartbeater (activityHeartbeatInterval,
		// 500ms, workflow/runtime/registry.go) that land inside an open window
		// are swallowed locally (temporalInvoker.Heartbeat) — no RPC, no
		// cancellation check — until the window closes and the last details are
		// sent. Temporal delivers a pending cancellation ONLY in a heartbeat
		// RPC's response, so cancel latency for anything on this path is
		// bounded by this value, not by how often RecordHeartbeat is called.
		// Verified against SDK v1.37.0 and v1.47.0 source; see
		// specs/fast-cancel-briefing.md for the full trace.
		//
		// This value is ALSO the heartbeat RPC's own deadline, and that is the
		// budget that actually breaks under load (internal_task_handlers.go
		// internalHeartBeat):
		//
		//	recordTimeout := i.heartbeatThrottleInterval
		//	if recordTimeout < minRPCTimeout { recordTimeout = minRPCTimeout }
		//	ctx, cancel := context.WithTimeout(ctx, recordTimeout)
		//
		// So the per-heartbeat budget is max(thisValue, minRPCTimeout=1s). An
		// earlier revision set this to 500ms and justified it with the claim
		// that the 1s floor made the budget insensitive to this value — that
		// the previous 3s setting "never widened that 1s budget at all". That
		// reading is WRONG: the floor only applies BELOW 1s. At 3s the budget
		// is 3s. The revert therefore narrowed a 3s budget to 1s while
		// believing it changed nothing, and it undid a fix that had been
		// correctly targeted at this exact failure.
		//
		// The consequence, measured: 872 "RecordActivityHeartbeat with error /
		// context deadline exceeded" in a single day's worker log, arriving in
		// bursts of 50-60 per minute whenever the shared Postgres behind
		// Temporal got busy. Each one cancels a healthy activity mid-stream.
		// Chat ee527bdd lost all five of a step's retry attempts inside one
		// burst and auto-paused.
		//
		// Raised to 2s, which doubles the RPC budget that was being blown. The
		// cost is real and is the reason this is not higher: this value is the
		// sole determinant of how fast a cancel reaches a running activity, so
		// pause and interrupt now take up to 2s instead of 500ms.
		//
		// Do NOT try to fix heartbeat RPC failures by LOWERING a timeout. The
		// budget is a max() with a 1s floor, so it only moves upward. Lowering
		// HeartbeatTimeout (registry.go) to squeeze the 0.8x derivation is
		// worse than useless: it cannot widen this budget, and it shortens the
		// window before Temporal declares a live worker dead and re-dispatches
		// its activities — which during one of these bursts means ExecuteTools
		// running the same shell commands and file edits twice.
		//
		// spuriousHeartbeatCancel (workflow/runtime/registry.go) remains the
		// backstop that converts whatever still slips through into a retry
		// rather than a user-visible cancellation.
		MaxHeartbeatThrottleInterval: maxHeartbeatThrottleInterval,
		BuildID:                      v2workflow.WorkerBuildID,
		// Set explicitly rather than inherited from the client, so the identity
		// is stable no matter how the client was constructed (tests and the
		// replay harness build their own). This is the value that surfaces as
		// Temporal's LastWorkerIdentity, which is what "did the worker restart?"
		// is actually read from — see internal/instanceid for why the SDK's
		// hostname-based default cannot answer that question.
		Identity:                 instanceid.WorkerIdentity(),
		DeadlockDetectionTimeout: 30 * time.Second,
		Interceptors:             []interceptor.WorkerInterceptor{observability.NewOTelWorkerInterceptor()},
	}

	w := worker.New(cfg.TemporalClient, cfg.taskQueueName(), workerOpts)

	// Register activities with worker
	registry.RegisterWithWorker(w)

	// Register workflows
	w.RegisterWorkflowWithOptions(v2.DynamicWorkflow, workflow.RegisterOptions{
		Name: v2workflow.WorkflowDynamic,
	})
	w.RegisterWorkflowWithOptions(GenerateTitleWorkflow, workflow.RegisterOptions{
		Name: "GenerateTitleWorkflow",
	})

	// Schedule triggers. The workflow is always registered — it is pure
	// orchestration — but the activity needs a launcher. Registering the
	// workflow without the activity means a fire on a launcher-less worker
	// retries rather than failing to start at all, which is what makes a
	// partially-rolled-out deploy safe.
	w.RegisterWorkflowWithOptions(triggers.TriggerFireWorkflow, workflow.RegisterOptions{
		Name: triggers.FireWorkflowName,
	})
	// Inbound triggers (webhook, integration, workflow event) fire through
	// their own workflow: the api-server's receiver records the event and
	// starts it, and its activity launches the recorded event.
	w.RegisterWorkflowWithOptions(triggers.TriggerEventFireWorkflow, workflow.RegisterOptions{
		Name: triggers.EventFireWorkflowName,
	})
	// Polled integration triggers: the schedule runs the poll workflow; the
	// activity asks the integration's poller what is new and records it.
	w.RegisterWorkflowWithOptions(triggers.TriggerPollWorkflow, workflow.RegisterOptions{
		Name: triggers.PollWorkflowName,
	})
	// An activation of a workflow-declared trigger fires from the
	// declaration as it is now; every fire path reads it through the same
	// resolution run start uses.
	declarations := triggers.LaunchWorkflows{Repo: cfg.Database}
	if cfg.TriggerPollers != nil {
		if repo, ok := cfg.Database.(*db.Repo); ok {
			poller := triggers.NewTriggerPoller(
				triggerPollRepo{Repo: repo, connections: repo.Connections()},
				cfg.TriggerPollers,
				triggers.NewIntake(repo, cfg.TemporalClient, cfg.taskQueueName()).WithWorkflows(declarations),
				cfg.TriggerCredentials,
			)
			w.RegisterActivityWithOptions(poller.Poll, activity.RegisterOptions{Name: triggers.PollActivityName})
		}
	}
	if cfg.TriggerLauncher != nil {
		w.RegisterActivityWithOptions(
			triggers.NewFirer(cfg.Database, cfg.TriggerLauncher).WithWorkflows(declarations).Fire,
			activity.RegisterOptions{Name: triggers.FireActivityName},
		)
		w.RegisterActivityWithOptions(
			triggers.NewEventFirer(cfg.Database, cfg.TriggerLauncher).WithWorkflows(declarations).Fire,
			activity.RegisterOptions{Name: triggers.EventFireActivityName},
		)
	} else {
		logging.Warn("no trigger launcher configured; this worker will not execute scheduled fires")
	}

	// Workflow-event triggers: same split as schedules. The dispatch workflow
	// is always registered; its activity needs the launcher.
	w.RegisterWorkflowWithOptions(workflowevent.RunEventDispatchWorkflow, workflow.RegisterOptions{
		Name: workflowevent.DispatchWorkflowName,
	})
	if cfg.TriggerLauncher != nil {
		dispatcher := workflowevent.NewDispatcher(cfg.Database, cfg.TriggerLauncher, nil).WithWorkflows(declarations)
		w.RegisterActivityWithOptions(
			workflowevent.NewActivity(cfg.Database, dispatcher).Dispatch,
			activity.RegisterOptions{Name: workflowevent.DispatchActivityName},
		)
	}

	// Start worker with lifecycle management
	handle := &Handle{
		Worker: w,
		Done:   make(chan struct{}),
	}

	go func() {
		defer close(handle.Done)
		handle.Err = w.Run(worker.InterruptCh())
	}()

	return handle, registry, nil
}

// TaskQueueName returns the task queue name that would be used by a worker with the given config.
func TaskQueueName(suffix string) string {
	cfg := &Config{TaskQueueSuffix: suffix}
	return cfg.taskQueueName()
}

// triggerPollRepo gives the poll activity the owner-scoped connection read it
// checks a polled trigger's connection with, which lives on the connection
// store rather than the main repository.
type triggerPollRepo struct {
	*db.Repo
	connections core.ConnectionStore
}

func (r triggerPollRepo) GetConnection(ctx context.Context, userID, id string) (*core.Connection, error) {
	return r.connections.GetConnection(ctx, userID, id)
}
