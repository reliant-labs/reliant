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
		MaxHeartbeatThrottleInterval:     500 * time.Millisecond,
		BuildID:                          v2workflow.WorkerBuildID,
		DeadlockDetectionTimeout:         30 * time.Second,
		Interceptors:                     []interceptor.WorkerInterceptor{observability.NewOTelWorkerInterceptor()},
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
