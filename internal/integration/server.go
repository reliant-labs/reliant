// Copyright (c) 2025 Reliant Labs
package integration

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/mcpconfig"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/configadapter"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/envutil"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workersetup"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/reconciliation"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/worktree"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// workerHandle holds a worker and its lifecycle management
type workerHandle struct {
	worker worker.Worker
	done   chan struct{} // Closed when worker exits
}

// Server is the integrated V2 server
type Server struct {
	// Components
	temporalServer     *temporal.EmbeddedServer
	temporalClient     client.Client
	database           db.Repository
	mcpManager         *mcp.Manager
	toolsFactory       *tools.ToolsFactory
	toolExecutor       toolexec.ToolExecutor
	remoteToolExecutor *toolexec.RemoteExecutor
	toolsDaemonService *services.ToolsDaemonService
	daemonRouter       toolexec.DaemonRouter
	worktreeService    worktree.Service

	// Streaming hub
	streamingHub streaming.StreamingHub

	// Workers with lifecycle management
	workers []*workerHandle

	// Activity registry (shared across workers)
	activityRegistry *v2.ActivityRegistry

	// Pause service for unified pause/resume operations
	pauseService *v2workflow.PauseService

	// Reconciler for workflow state reconciliation
	reconciler *reconciliation.Reconciler

	// Config
	config *Config

	// Whether we're using an external Temporal server (don't stop it on shutdown)
	usingExternalTemporal bool
}

type Config struct {
	// Database
	DatabasePath string

	// Temporal
	TemporalConfig *temporal.ServerConfig

	// ExternalTemporalServer - if provided, use this instead of creating a new one
	// This allows tests to share a single Temporal server across multiple test harnesses
	ExternalTemporalServer *temporal.EmbeddedServer

	// TaskQueueSuffix - if provided, appends to task queue names for isolation
	// This allows multiple test harnesses to use the same Temporal server without interference
	TaskQueueSuffix string

	// LLM
	AnthropicAPIKey string

	// Worktree
	WorktreeBaseDir string // Base directory for worktrees (defaults to ~/.reliant/worktrees)

	// Testing overrides - when set, these will be used instead of creating real executors
	// This allows e2e tests to inject mocks without modifying the server code
	ToolExecutorOverride toolexec.ToolExecutor  // Mock tool executor for testing
	RunExecutorOverride  handlers.RunExecutor   // Mock run executor for testing
	DriverResolver       drivers.DriverResolver // Custom LLM driver resolver (nil = production default)

	// SilentLogging suppresses all logging output (for tests)
	SilentLogging bool
}

// NewServer creates a new integrated server
func NewServer(cfg *Config) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}

	// Create database and optionally Temporal server in parallel
	var wg sync.WaitGroup
	var repo db.Repository
	var temporalServer *temporal.EmbeddedServer
	var dbErr, temporalErr error

	// If external Temporal server is provided, use it
	useExternalTemporal := cfg.ExternalTemporalServer != nil

	if useExternalTemporal {
		wg.Add(1)
		temporalServer = cfg.ExternalTemporalServer
	} else {
		wg.Add(2)
	}

	// Create database
	go func() {
		defer wg.Done()
		r, err := db.NewRepoFromDir(cfg.DatabasePath)
		if err != nil {
			dbErr = fmt.Errorf("failed to create database: %w", err)
			return
		}
		repo = r
	}()

	// Create embedded Temporal server (only if not using external)
	if !useExternalTemporal {
		go func() {
			defer wg.Done()
			ts, err := temporal.NewEmbeddedServer(cfg.TemporalConfig)
			if err != nil {
				temporalErr = fmt.Errorf("failed to create temporal server: %w", err)
				return
			}
			temporalServer = ts
		}()
	}

	wg.Wait()

	// Initialize API key provider with database (after repo is created)
	drivers.InitializeAPIKeyProvider(
		repo,
		drivers.WithControlPlaneClient(controlplane.NewClientFromEnv()),
		drivers.WithReliantRuntimeBaseURL(envutil.GetEnv("RELIANT_RUNTIME_BASE_URL", "")),
	)

	// Check for errors
	if dbErr != nil {
		return nil, dbErr
	}
	if temporalErr != nil {
		return nil, temporalErr
	}

	return &Server{
		temporalServer:        temporalServer,
		database:              repo,
		config:                cfg,
		usingExternalTemporal: useExternalTemporal,
	}, nil
}

// GenerateTitleWorkflow is a thin inline wrapper around V2_GenerateTitle activity
// This provides async execution, retries, and observability

// Start starts the server
func (s *Server) Start(ctx context.Context) error {
	startTime := time.Now()

	// Start Temporal server (only if we own it - external servers are already running)
	if !s.usingExternalTemporal {
		go func() {
			if err := s.temporalServer.Start(); err != nil {
				panic(fmt.Errorf("temporal server failed: %w", err))
			}
		}()
	}

	// Wait for Temporal to be ready with retry logic
	if err := s.waitForTemporalReady(ctx); err != nil {
		return fmt.Errorf("temporal server did not become ready: %w", err)
	}

	// Create Temporal client
	client, err := s.temporalServer.NewClient(ctx, "reliant")
	if err != nil {
		return fmt.Errorf("failed to create temporal client: %w", err)
	}
	s.temporalClient = client

	// Note: MCPManager is now initialized lazily on first use, not on startup
	// This allows the server to work with multiple projects without needing
	// to know all project paths upfront. The MCP handler will create a manager
	// and load configs from each project's directory when needed.

	// Create a lazy-initialized MCP manager
	s.mcpManager = mcp.NewManager()

	// Create tools factory with minimal options for V2
	// For now, most services are nil - tools will have limited functionality
	// Pass the db.Repo for v2-specific tools like the agent tool
	s.toolsFactory = tools.NewToolsFactory(&tools.ToolsOptions{
		Repo: s.database, // Pass the v2 repository for agent tool
	})

	// Create tool executor. In production we always route through daemon/gRPC semantics.
	// Use override only for tests.
	if s.config.ToolExecutorOverride != nil {
		s.toolExecutor = s.config.ToolExecutorOverride
	} else {
		s.remoteToolExecutor = toolexec.NewRemoteExecutor(nil)
		s.toolExecutor = s.remoteToolExecutor

		// Wire server-side tool execution.
		serverExec := toolexec.NewLocalToolExecutor(s.toolsFactory)
		s.remoteToolExecutor.SetServerExecutor(serverExec)

		// Create ToolsDaemonService + LocalDaemonRouter so the worker has
		// a fully wired DaemonRouter from the start.  This MUST happen
		// before workersetup.StartWorker() to avoid nil-router errors.
		s.toolsDaemonService = services.NewToolsDaemonService(s.database)
		s.daemonRouter = toolexec.NewLocalDaemonRouter(s.toolsDaemonService)
		s.remoteToolExecutor.SetDaemonRouter(s.daemonRouter)

		// Server-side tools that need daemon FS/exec access create a per-request
		// RemoteClient that routes through the DaemonRouter — same path as cloud mode.
		s.remoteToolExecutor.SetDaemonClientFactory(func(userID string) daemon.Client {
			return daemon.NewRemoteClient(s.daemonRouter, userID)
		})
	}

	storedConfigProvider := config.NewStoredConfigProvider(configadapter.NewRepoConfigStore(s.database))

	if s.mcpManager != nil {
		s.mcpManager.SetProjectConfigResolver(func(ctx context.Context, projectPath string) (*config.Config, error) {
			project, err := mcpconfig.ResolveProjectForMCPPath(ctx, s.database, projectPath)
			if err != nil {
				return nil, err
			}
			return storedConfigProvider.GetProjectConfig(ctx, config.ProjectRef{ProjectID: project.ID})
		})
	}

	// Create PauseService for pause/resume operations (signal-based pause via Temporal)
	s.pauseService = v2workflow.NewPauseService(s.temporalClient, s.database)

	// Create Reconciler for workflow state reconciliation
	// This detects and fixes mismatches between Temporal and DB state
	s.reconciler = reconciliation.NewReconciler(s.database, s.temporalClient, nil)

	// Create in-memory streaming hub for the integration server
	s.streamingHub = streaming.NewMemoryHub()

	// Use shared worker setup package
	handle, registry, err := workersetup.StartWorker(&workersetup.Config{
		TemporalClient:      s.temporalClient,
		Database:            s.database,
		StreamingHub:        s.streamingHub,
		ToolsFactory:        s.toolsFactory,
		ToolExecutor:        s.toolExecutor,
		DaemonRouter:        s.daemonRouter, // may be nil in test overrides, but wired for production
		MCPBinder:           toolexec.NewLocalMCPContextBinder(s.mcpManager),
		ConfigProvider:      storedConfigProvider,
		RunExecutorOverride: s.config.RunExecutorOverride,
		DriverResolver:      s.config.DriverResolver,
		TaskQueueSuffix:     s.config.TaskQueueSuffix,
	})
	if err != nil {
		return fmt.Errorf("failed to start worker: %w", err)
	}
	s.activityRegistry = registry
	s.workers = append(s.workers, &workerHandle{
		worker: handle.Worker,
		done:   handle.Done,
	})

	// Start background reconciliation to detect and fix stale workflow states
	// This runs periodically to reconcile DB state with Temporal
	s.reconciler.StartBackgroundReconciliation(ctx)

	// Only log startup time if not in quiet mode (check if output is discarded)
	if logging.DefaultOutput != io.Discard {
		fmt.Printf("[Startup] ✓ Server started successfully in %v\n", time.Since(startTime))
	}
	return nil
}

// Stop stops the server gracefully
func (s *Server) Stop() error {
	const (
		totalShutdownTimeout = 15 * time.Second
	)

	logging.Info("Integration server shutdown started", "timeout", totalShutdownTimeout)

	// Create shutdown context with timeout for the entire operation
	ctx, cancel := context.WithTimeout(context.Background(), totalShutdownTimeout)
	defer cancel()

	// Phase 1: Signal all components to stop (non-blocking)
	logging.Info("Stopping main workers", "count", len(s.workers))
	for i, handle := range s.workers {
		logging.Debug("Stopping worker", "index", i)
		handle.worker.Stop()
	}

	// Stop background reconciliation
	if s.reconciler != nil {
		logging.Info("Stopping reconciler")
		s.reconciler.Stop()
	}

	// Close Temporal client (no new workflows can be started)
	if s.temporalClient != nil {
		logging.Info("Closing Temporal client")
		s.temporalClient.Close()
	}

	// Phase 2: Wait for everything to shut down concurrently

	// Use errgroup or simple goroutines with channels
	type shutdownResult struct {
		component string
		err       error
	}
	results := make(chan shutdownResult, 4) // workers, temporal, database, mcp

	// Wait for workers
	go func() {
		for i, handle := range s.workers {
			select {
			case <-handle.done:
				// Worker exited normally
				logging.Debug("Worker exited", "index", i)
			case <-ctx.Done():
				logging.Warn("Workers did not stop in time", "index", i, "timeout", totalShutdownTimeout)
				results <- shutdownResult{"workers", fmt.Errorf("workers did not stop in time")}
				return
			}
		}
		logging.Info("All workers stopped successfully")
		results <- shutdownResult{"workers", nil}
	}()

	// Stop Temporal server (only if we own it)
	go func() {
		if s.usingExternalTemporal {
			// Don't stop external Temporal server - it's shared
			logging.Info("Skipping Temporal server shutdown (using external server)")
			results <- shutdownResult{"temporal", nil}
		} else {
			logging.Info("Stopping embedded Temporal server")
			err := s.temporalServer.Stop()
			if err != nil {
				logging.Error("Temporal server shutdown error", "error", err)
			} else {
				logging.Info("Temporal server stopped successfully")
			}
			results <- shutdownResult{"temporal", err}
		}
	}()

	// Close database
	go func() {
		results <- shutdownResult{"database", s.database.Close()}
	}()

	// Close MCP Manager
	go func() {
		var err error
		if s.mcpManager != nil {
			err = s.mcpManager.Close()
		}
		results <- shutdownResult{"mcp", err}
	}()

	// Close ToolsDaemonService (stops stale-daemon monitor)
	if s.toolsDaemonService != nil {
		s.toolsDaemonService.Close()
	}

	// Collect results
	var errors []error
	componentsCompleted := 0
	totalComponents := 4

	for componentsCompleted < totalComponents {
		select {
		case result := <-results:
			componentsCompleted++
			if result.err != nil {
				errors = append(errors, fmt.Errorf("%s: %w", result.component, result.err))
			}
		case <-ctx.Done():
			remaining := totalComponents - componentsCompleted
			return fmt.Errorf("shutdown timeout: %d components did not complete in time: %w", remaining, ctx.Err())
		}
	}

	// Check if any errors occurred
	if len(errors) > 0 {
		errMsgs := make([]string, len(errors))
		for i, e := range errors {
			errMsgs[i] = e.Error()
		}
		logging.Error("Integration server shutdown completed with errors", "errors", errMsgs, "count", len(errors))
		return errors[0]
	}

	logging.Info("Integration server shutdown completed successfully")
	return nil
}

// Client returns the Temporal client
func (s *Server) Client() client.Client {
	return s.temporalClient
}

// SetTemporalClient sets the Temporal client (for API server connecting to external Temporal)
func (s *Server) SetTemporalClient(c client.Client) {
	s.temporalClient = c
}

// Database returns the database repository
func (s *Server) Database() db.Repository {
	return s.database
}

// ToolsFactory returns the tools factory
func (s *Server) ToolsFactory() *tools.ToolsFactory {
	return s.toolsFactory
}

// ToolExecutor returns the tool executor
func (s *Server) ToolExecutor() toolexec.ToolExecutor {
	return s.toolExecutor
}

// WorktreeService returns the worktree service
func (s *Server) WorktreeService() worktree.Service {
	return s.worktreeService
}

// ToolsDaemonService returns the shared ToolsDaemonService created during Start().
// Returns nil if the server was started with test overrides (ToolExecutorOverride).
func (s *Server) ToolsDaemonService() *services.ToolsDaemonService {
	return s.toolsDaemonService
}

// DaemonRouter returns the DaemonRouter created during Start().
// Returns nil if the server was started with test overrides (ToolExecutorOverride).
func (s *Server) DaemonRouter() toolexec.DaemonRouter {
	return s.daemonRouter
}

// StreamingHub returns the streaming hub used by this server
func (s *Server) StreamingHub() streaming.StreamingHub {
	return s.streamingHub
}

// PauseService returns the unified pause/resume service
func (s *Server) PauseService() *v2workflow.PauseService {
	return s.pauseService
}

// Reconciler returns the workflow state reconciler
func (s *Server) Reconciler() *reconciliation.Reconciler {
	return s.reconciler
}

// SharedTaskQueueName returns the shared task queue name (with optional test suffix)
func (s *Server) SharedTaskQueueName() string {
	return workersetup.TaskQueueName(s.config.TaskQueueSuffix)
}

// DataDir returns the data directory path where databases and state files are stored
func (s *Server) DataDir() string {
	return s.config.DatabasePath
}

// TemporalFrontendHostPort returns the Temporal frontend host:port
func (s *Server) TemporalFrontendHostPort() string {
	return s.temporalServer.FrontendHostPort()
}

// waitForTemporalReady waits for Temporal server to be ready by attempting client connections
func (s *Server) waitForTemporalReady(ctx context.Context) error {
	const (
		maxRetries    = 40                     // Increased retries to compensate for faster polling
		retryInterval = 100 * time.Millisecond // Faster polling - 100ms instead of 500ms
	)

	for i := 0; i < maxRetries; i++ {
		// Try to create a client (this will fail if Temporal isn't ready)
		testClient, err := s.temporalServer.NewClient(ctx, "reliant")
		if err == nil {
			// Client created successfully - close it (we'll create the real one later)
			testClient.Close()
			return nil
		}

		// Not ready yet, wait and retry
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for temporal: %w", ctx.Err())
		case <-time.After(retryInterval):
			// Continue retry loop
		}
	}

	return fmt.Errorf("temporal server did not become ready after %d attempts", maxRetries)
}