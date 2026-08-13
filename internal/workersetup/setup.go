// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	v2activities "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

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

	// Register all activities
	v2activities.RegisterAll(registry, activityDeps)

	// Create shared worker with standard options
	workerOpts := worker.Options{
		StickyScheduleToStartTimeout:     5 * time.Second,
		MaxConcurrentWorkflowTaskPollers: 5,
		MaxConcurrentActivityTaskPollers: 10,
		WorkerStopTimeout:                5 * time.Second,
		// This is NOT just a throttle — it is also the heartbeat RPC's own
		// deadline. The SDK sets the RPC timeout to this value, floored at
		// minRPCTimeout=1s (internal_task_handlers.go internalHeartBeat):
		//
		//	recordTimeout := i.heartbeatThrottleInterval
		//	if recordTimeout < minRPCTimeout { recordTimeout = minRPCTimeout }
		//	ctx, cancel := context.WithTimeout(ctx, recordTimeout)
		//
		// At 500ms the floor applied, so every heartbeat had exactly ONE second
		// to complete a round trip to the Temporal server. A single slow trip
		// failed the RPC, DeadlineExceeded mapped to a retryable gRPC code, and
		// the SDK cancelled the whole activity context — killing a healthy
		// activity seconds into a 30-day StartToCloseTimeout.
		//
		// The user-visible damage was not a timeout message. Whatever operation
		// happened to be in flight died with the context and reported ITS error
		// instead: "streaming cancelled by user" from CallLLM (which no user
		// cancelled), and "failed to connect to database ... operation was
		// canceled" from SaveMessage (with Postgres up, healthy, and reachable
		// the whole time). Observed 63+ times across two log windows, every one
		// with cause="context deadline exceeded".
		//
		// 3s gives a round trip three times the budget. The cost is that a
		// server-side cancellation now reaches a running activity in up to ~3s
		// rather than ~500ms; per-tool and per-spawn cancellation do not depend
		// on this path (they run over the daemon router and a workflow signal
		// respectively), so that latency is not user-visible.
		//
		// spuriousHeartbeatCancel in workflow/runtime/registry.go still converts
		// any that slip through into a retry rather than a chat-parking pause.
		// This reduces how often that safety net is needed; it does not replace
		// it.
		MaxHeartbeatThrottleInterval: 3 * time.Second,
		BuildID:                      v2workflow.WorkerBuildID,
		DeadlockDetectionTimeout:     30 * time.Second,
		Interceptors:                 []interceptor.WorkerInterceptor{observability.NewOTelWorkerInterceptor()},
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
