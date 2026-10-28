// Copyright (c) 2025 Reliant Labs

// Package workersetup registers reliant's Temporal workflows and activities
// on a worker (NewWorker) and runs it as a supervised component (Worker), for
// the temporal-worker and the test harnesses alike.
//
//forge:exclude-contract: Temporal worker composition (workflow/activity registration over injected collaborators), built as the worker root's TemporalWorker node (internal/app) and by the test harnesses; it wires services rather than being one
package workersetup

import (
	"context"
	"runtime"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/instanceid"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/workflowevent"
	"github.com/reliant-labs/reliant/internal/version"
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
	Database       *db.Repo
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
	// same connauth.Service as IntegrationCredentials. nil skips every poll
	// rather than letting a poller run unauthenticated.
	TriggerCredentials triggers.PollCredentials

	// IntegrationCredentials authenticates the action node's integration
	// calls (research/CONNECTIONS_VAULT.md §3.1): saved connections, plus
	// control-plane-delegated GitHub tokens when hosted. It exists on the
	// worker only: the worker is the one reader of connection secrets. It is
	// the SAME source the http__request tool uses, so the two doors resolve
	// a credential identically. nil refuses every authenticated call.
	IntegrationCredentials httpaction.CredentialSource

	// HostedActions is fixed-action-only hosted HTTP execution, never a tool
	// credential source. nil refuses nodes that request hosted_use.
	HostedActions handlers.HostedActionExecutor

	// AllowPrivateEgress is ServerProcessConfig.network.allow_private_egress:
	// whether a user-supplied model endpoint may resolve to a private,
	// loopback or link-local address. False is the default.
	AllowPrivateEgress bool
	// WriteLedger records the action node's mutating integration calls, so an
	// event they cause can be attributed. nil records nothing.
	WriteLedger writeledger.Service

	// AccountLimits records the rate-limit state of the accounts that serve
	// requests and benches one the provider refused. Optional: nil records
	// nothing and a refused account is waited out, not switched away from.
	AccountLimits handlers.AccountLimitRecorder

	// Optional overrides (for testing)
	RunExecutorOverride handlers.RunExecutor
	DriverResolver      drivers.DriverResolver // Custom LLM driver resolver (nil = production default)
	// WorkflowInterceptors run after the production interceptors. The replay
	// fixture generator injects a workflow panic through one; production
	// passes none.
	WorkflowInterceptors []interceptor.WorkerInterceptor

	// WorkflowTaskSlots is worker.Options.MaxConcurrentWorkflowTaskExecutionSize
	// (WorkerConfig's RELIANT_WORKER_MAX_CONCURRENT_WORKFLOW_TASKS). 0 derives
	// it from the CPU budget; see WorkflowTaskSlots.
	WorkflowTaskSlots int
	// WorkflowTaskPollers is worker.Options.MaxConcurrentWorkflowTaskPollers.
	// 0 keeps defaultWorkflowTaskPollers.
	WorkflowTaskPollers int

	// Task queue configuration
	TaskQueueSuffix string // Optional suffix for test isolation
}

// defaultWorkflowTaskPollers is the workflow-task poller count when the
// config names none.
const defaultWorkflowTaskPollers = 5

// WorkflowTaskSlots is how many workflow tasks a worker runs at once:
// configured when it is set, else max(2, gomaxprocs-1).
//
// The cap exists because a workflow task can be a full history replay, which
// is CPU-bound for as long as the history takes to re-execute (a 37k-event
// run took ~15 minutes of two cores in prod, 2026-10-23). The SDK's default is
// 1000 slots, so a burst of replays took every core, and the activities in the
// same process missed their DB deadlines. Activities draw from their own
// slots, so capping workflow tasks below the core count leaves them a core.
//
// gomaxprocs-1, not gomaxprocs: a replay runs on one goroutine, so N slots
// can occupy N cores, and a cap equal to the core count protects nothing. The
// floor of 2 is the SDK's: it refuses 1, which would leave its sticky-queue
// poller the only one. The cost of a low cap is head-of-line blocking: while
// every slot is held by a long replay, this worker starts no other workflow
// task until one finishes (queries waiting for a slot time out instead of
// replaying, which is the other half of the point). Raise the setting when
// that latency matters more than isolation.
//
// gomaxprocs is the runtime's, which since Go 1.25 already honours the
// container's CPU limit (ceil(limit), at least 2), so a 3-core pod gets 2.
func WorkflowTaskSlots(configured, gomaxprocs int) int {
	if configured > 0 {
		return configured
	}
	return max(2, gomaxprocs-1)
}

// workflowTaskPollers is the configured poller count, or the default.
func workflowTaskPollers(configured int) int {
	if configured > 0 {
		return configured
	}
	return defaultWorkflowTaskPollers
}

// taskQueueName returns the full task queue name with optional suffix.
func (c *Config) taskQueueName() string {
	base := v2workflow.SharedTaskQueue
	if c.TaskQueueSuffix == "" {
		return base
	}
	return base + "-" + c.TaskQueueSuffix
}

// NewWorker creates a Temporal worker and registers every workflow and
// activity on it. It does not start it: the returned Worker is a component,
// polling from Run until Drain stops it (see Worker).
func NewWorker(cfg *Config) (*Worker, *v2.ActivityRegistry, error) {
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
	activityDeps.HostedActions = cfg.HostedActions
	activityDeps.AllowPrivateEgress = cfg.AllowPrivateEgress
	activityDeps.WriteLedger = cfg.WriteLedger
	activityDeps.AccountRouting = accountRouting(cfg.Database, cfg.AccountLimits)

	// Register all activities
	v2activities.RegisterAll(registry, activityDeps)

	onFatalError, fatal := fatalErrorSink()
	w := worker.New(cfg.TemporalClient, cfg.taskQueueName(), workerOptions(cfg, onFatalError, runtime.GOMAXPROCS(0)))

	// Register activities with worker
	registry.RegisterWithWorker(w)

	// Register workflows
	w.RegisterWorkflowWithOptions(v2.DynamicWorkflow, workflow.RegisterOptions{
		Name: v2workflow.WorkflowDynamic,
	})
	w.RegisterWorkflowWithOptions(GenerateTitleWorkflow, workflow.RegisterOptions{
		Name: "GenerateTitleWorkflow",
	})
	w.RegisterWorkflowWithOptions(DraftAutomationWorkflow, workflow.RegisterOptions{
		Name: DraftAutomationWorkflowName,
	})

	// Schedule triggers. The workflow is always registered — it is pure
	// orchestration — but the activity needs a launcher. Registering the
	// workflow without the activity means a fire on a launcher-less worker
	// retries rather than failing to start at all, which is what makes a
	// partially-rolled-out deploy safe.
	w.RegisterWorkflowWithOptions(triggers.TriggerFireWorkflow, workflow.RegisterOptions{
		Name: triggers.FireWorkflowName,
	})
	w.RegisterWorkflow(triggers.TriggerCapacityRedriveWorkflow)
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
		if repo := cfg.Database; repo != nil {
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
			triggers.NewFirer(cfg.Database, cfg.TriggerLauncher).WithWorkflows(declarations).WithCapacityDescriptions(cfg.TemporalClient).Fire,
			activity.RegisterOptions{Name: triggers.FireActivityName},
		)
		w.RegisterActivityWithOptions(
			triggers.NewEventFirer(cfg.Database, cfg.TriggerLauncher).WithWorkflows(declarations).WithCapacityDescriptions(cfg.TemporalClient).Fire,
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

	return newWorker(w, fatal), registry, nil
}

// workerOptions is the shared worker's worker.Options, gomaxprocs being the
// runtime's (a parameter so the derivation is testable).
func workerOptions(cfg *Config, onFatalError func(error), gomaxprocs int) worker.Options {
	return worker.Options{
		// How Worker.Run learns the worker died; see temporalWorker.
		OnFatalError:                 onFatalError,
		StickyScheduleToStartTimeout: 5 * time.Second,
		// Workflow tasks are capped (WorkflowTaskSlots has why); activities
		// are not, and keep the SDK's own 1000-slot budget: they are mostly
		// waiting on an LLM stream, a daemon or the DB, not burning CPU.
		MaxConcurrentWorkflowTaskExecutionSize: WorkflowTaskSlots(cfg.WorkflowTaskSlots, gomaxprocs),
		MaxConcurrentWorkflowTaskPollers:       workflowTaskPollers(cfg.WorkflowTaskPollers),
		MaxConcurrentActivityTaskPollers:       10,
		// On Stop, in-flight tasks get activityReleaseGrace to finish; then
		// activityRelease cancels the activities still running, and the rest
		// is time for them to report, so Temporal re-dispatches them at once.
		// Must fit the shutdown budget (SHUTDOWN_TIMEOUT) the Drain runs in.
		WorkerStopTimeout: activityReleaseGrace + activityReportReserve,
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
		// The binary's release version, so a workflow task's history event and
		// the SDK's "Workflow panic" log name the build that ran it. A constant
		// here ("reliant-worker") made every build look identical: on
		// 2026-10-23 a mid-rollout TMPRL1100 non-determinism panic — an old
		// replica replaying history the new one had written — could not be
		// told apart from a corrupt history. Versioning-based dispatch stays
		// off (UseBuildIDForVersioning unset): this only labels.
		BuildID: version.TemporalBuildID(),
		// Set explicitly rather than inherited from the client, so the identity
		// is stable no matter how the client was constructed (tests and the
		// replay harness build their own). This is the value that surfaces as
		// Temporal's LastWorkerIdentity, which is what "did the worker restart?"
		// is actually read from — see internal/instanceid for why the SDK's
		// hostname-based default cannot answer that question.
		Identity:                 instanceid.WorkerIdentity(),
		DeadlockDetectionTimeout: 30 * time.Second,
		Interceptors: append([]interceptor.WorkerInterceptor{
			observability.NewMetricsWorkerInterceptor(),
			observability.NewRunScopeWorkerInterceptor(chatOwners(cfg)),
			newActivityRelease(),
		}, cfg.WorkflowInterceptors...),
	}
}

// chatOwners answers whose chat an activity is working on, for the run scope
// the worker stamps on every log line (observability.NewRunScopeWorkerInterceptor).
// A chat's owner never changes; it is read once per chat per hour.
func chatOwners(cfg *Config) *telemetry.ChatOwnerCache {
	if cfg.Database == nil {
		return nil
	}
	repo := cfg.Database
	return telemetry.NewChatOwnerCache(func(ctx context.Context, chatID string) (string, error) {
		chat, err := repo.GetChat(ctx, chatID)
		if err != nil || chat == nil {
			return "", err
		}
		return chat.UserID, nil
	}, time.Hour, 10000)
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
	connections db.ConnectionStore
}

func (r triggerPollRepo) GetConnection(ctx context.Context, userID, id string) (*core.Connection, error) {
	return r.connections.GetConnection(ctx, userID, id)
}
