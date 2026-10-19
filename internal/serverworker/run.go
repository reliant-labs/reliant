// Copyright (c) 2025 Reliant Labs
package serverworker

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/threads"
	scenariorunner "github.com/reliant-labs/reliant/internal/workflow/scenario/runner"

	"github.com/reliant-labs/forge/pkg/serverkit"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/agentruns"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/configadapter"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/debugserver"
	"github.com/reliant-labs/reliant/internal/handlers/chat"
	"github.com/reliant-labs/reliant/internal/handlers/run"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/workflowevent"
	"github.com/reliant-labs/reliant/internal/triggertools"
	"github.com/reliant-labs/reliant/internal/videojobs"
	"github.com/reliant-labs/reliant/internal/workersetup"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	typedconfig "github.com/reliant-labs/reliant/pkg/config"
)

// Options holds all configurable values for the Temporal worker.
type Options struct {
	// Database
	DatabaseDriver string
	DatabaseURL    string
	// DatabaseDirectURL is the session-capable URL for session-scoped work
	// (db.DatabaseConfig.DirectURL); empty when DatabaseURL already is one.
	DatabaseDirectURL string
	DataDir           string

	// Temporal
	TemporalHost      string
	TemporalPort      int
	TemporalNamespace string

	// NATS / streaming
	NATSURL         string
	StreamingDriver string

	// Health check
	HealthPort int

	// PreStopDelay and ShutdownTimeout are the rollout drain budget
	// (PRE_STOP_DELAY / SHUTDOWN_TIMEOUT) as serverkit.Drainer takes it: zero
	// is serverkit's default (5s / 30s), a negative PreStopDelay no pause.
	PreStopDelay    time.Duration
	ShutdownTimeout time.Duration

	// PprofPort is the localhost-only diagnostics port (see debugserver).
	PprofPort int

	// ControlPlane is the control plane hosting this deployment; the zero
	// value is self-hosted (the role config's ControlPlaneConfig).
	ControlPlane controlplane.Endpoint

	// Log is the configured log level and format (the root config's
	// LogConfig).
	Log logging.Settings
}

// Run boots the Temporal worker with the given options. It blocks until a
// SIGINT/SIGTERM is received or the worker exits, then performs graceful
// shutdown.
func Run(ctx context.Context, opts Options) error {
	// -----------------------------------------------------------------
	// 1. Validate required config
	// -----------------------------------------------------------------
	if opts.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if opts.NATSURL == "" {
		return fmt.Errorf("NATS_URL is required (tool routing and streaming go through NATS)")
	}

	// The readiness gate and rollout drain budget. The worker serves no
	// inbound traffic, so its readiness flip matters less than the
	// api-server's — but the same budget is what bounds Worker.Stop, and that
	// is the part that has to fit inside the pod's grace period.
	drainer := serverkit.NewDrainer(serverkit.Config{PreStopDelay: opts.PreStopDelay, ShutdownTimeout: opts.ShutdownTimeout})

	// -----------------------------------------------------------------
	// 2. Logging
	// -----------------------------------------------------------------
	logLevel := logging.GetLogLevel(opts.Log)
	logging.SetupWithRotation(opts.Log, logLevel, false, &logging.RotationConfig{
		Filename:   filepath.Join(opts.DataDir, "logs", "temporal-worker.log"),
		MaxSizeMB:  50,
		MaxBackups: 3,
		MaxAgeDays: 30,
		Compress:   true,
	})
	defer logging.Close() //nolint:errcheck

	// Forge owns the OTel runtime; Sentry remains independently initialized below.
	obsProvider, err := observability.Init(observability.ConfigFromEnv("reliant-temporal-worker"))
	if err != nil {
		return fmt.Errorf("initialize observability: %w", err)
	}
	defer func() {
		if err := obsProvider.Shutdown(); err != nil {
			logging.Warn("Failed to shutdown observability", "error", err)
		}
	}()

	logging.Info("Starting temporal-worker",
		"temporal_host", opts.TemporalHost,
		"temporal_port", opts.TemporalPort,
		"db_driver", opts.DatabaseDriver,
		"data_dir", opts.DataDir,
	)

	// Ensure RELIANT_DATA_DIR is set for activities that need to locate files
	if err := os.Setenv("RELIANT_DATA_DIR", opts.DataDir); err != nil {
		logging.Warn("Failed to set RELIANT_DATA_DIR env var", "error", err)
	}

	// -----------------------------------------------------------------
	// 3. Global model registry
	// -----------------------------------------------------------------
	if err := models.InitGlobalRegistryWithUserConfig(nil); err != nil {
		return fmt.Errorf("failed to initialize model registry: %w", err)
	}

	// -----------------------------------------------------------------
	// 4. Database
	// -----------------------------------------------------------------
	dbDriver, err := db.ParseDatabaseDriver(opts.DatabaseDriver)
	if err != nil {
		return fmt.Errorf("invalid DATABASE_DRIVER %q: %w", opts.DatabaseDriver, err)
	}

	// api-server owns the schema; block here until it has applied migrations
	// rather than racing it (see db.MigrationPolicy).
	repo, err := db.NewRepoFromConfig(db.DatabaseConfig{
		Driver:    dbDriver,
		DataDir:   opts.DataDir,
		URL:       opts.DatabaseURL,
		DirectURL: opts.DatabaseDirectURL,
		Migrate:   db.MigrateWait,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer func() { _ = repo.Close() }()

	// Credential vault: required when hosted, generated when self-hosted.
	// Fails startup on a missing or malformed key.
	vaultKeys, err := db.BootVault(ctx, repo, opts.ControlPlane.Hosted(), opts.DataDir)
	if err != nil {
		return err
	}
	connResolver, err := newConnectionResolver(repo, vaultKeys)
	if err != nil {
		return err
	}
	if err := checkExecutors(); err != nil {
		return err
	}
	integrationCredentials, err := newIntegrationCredentials(connResolver, opts.ControlPlane)
	if err != nil {
		return err
	}
	catalogSearch, err := newCatalogSearch(repo.Connections(), integrationCredentials)
	if err != nil {
		return err
	}
	// Every mutating integration call, from the action node or an agent tool,
	// is recorded before it is made, so the event it causes can be attributed.
	writeLedger := writeledger.New(writeledger.Deps{Store: repo.IntegrationWrites(), Runs: repo})
	go writeLedger.PruneLoop(ctx, repo.IntegrationWrites())

	// API key provider (allows LLM drivers to resolve per-user keys from DB)
	drivers.InitializeAPIKeyProvider(repo)
	drivers.InstallReliantKeyHealer(ctx, repo, opts.ControlPlane.URL, opts.ControlPlane.InternalServiceSecret, 0)

	// -----------------------------------------------------------------
	// 5. Temporal client
	// -----------------------------------------------------------------
	// PayloadStore: large payloads are claim-checked into the shared
	// temporal_payload_blobs table (GC runs in the api-server). Must match
	// the api-server's wiring or histories become unreadable across them.
	temporalClient, err := temporal.NewExternalClient(ctx, temporal.ExternalClientConfig{
		Host:         opts.TemporalHost,
		Port:         opts.TemporalPort,
		Namespace:    opts.TemporalNamespace,
		PayloadStore: claimcheck.NewPostgresStore(repo.DB.SQLDB()),
	})
	if err != nil {
		return fmt.Errorf("failed to connect to Temporal: %w", err)
	}
	defer temporalClient.Close()

	logging.Info("Connected to external Temporal server",
		"host", opts.TemporalHost, "port", opts.TemporalPort)

	// -----------------------------------------------------------------
	// 6. Tools factory + remote executor
	// -----------------------------------------------------------------
	remoteExecutor := toolexec.NewRemoteExecutor(nil)

	// Stored config provider. Cached: CallLLM resolves the project config on
	// every turn, and the record can be tens of MB of skills. Each call still
	// checks the record's version, so a daemon push is seen immediately on
	// every worker replica.
	storedConfigProvider := config.NewCachedStoredConfigProvider(configadapter.NewRepoConfigStore(repo), repo, config.DefaultParsedConfigCacheBytes)

	// -----------------------------------------------------------------
	// 7. Streaming hub
	// -----------------------------------------------------------------
	streamingDriver, err := streaming.ParseStreamingDriver(opts.StreamingDriver)
	if err != nil {
		return fmt.Errorf("invalid STREAMING_DRIVER %q: %w", opts.StreamingDriver, err)
	}

	streamingHub, err := streaming.NewStreamingHub(streaming.StreamingConfig{
		Driver:  streamingDriver,
		NATSUrl: opts.NATSURL,
	})
	if err != nil {
		return fmt.Errorf("failed to create streaming hub: %w", err)
	}
	defer func() { _ = streamingHub.Close() }()

	logging.Info("Streaming hub initialized", "driver", opts.StreamingDriver)

	// -----------------------------------------------------------------
	// 8. NATS connection + update hubs
	// -----------------------------------------------------------------
	nc, err := natsutil.Connect(opts.NATSURL)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}
	defer nc.Close()

	userUpdateHub := streaming.NewNATSUpdateHub[db.UserUpdate](nc, "user.updates", "UserUpdate")
	chatUpdateHub := streaming.NewNATSUpdateHub[db.ChatUpdate](nc, "chat.updates", "ChatUpdate")
	logging.Info("Update hubs initialized (NATS)")
	defer func() { _ = userUpdateHub.Close() }()
	defer func() { _ = chatUpdateHub.Close() }()

	// Wire repo update notifiers to push events to update hubs
	repo.SetUpdateNotifiers(
		func(update *db.UserUpdate) {
			userUpdateHub.Publish(context.Background(), streaming.UpdateEvent[db.UserUpdate]{
				Key: update.UserID, SequenceNumber: update.SequenceNumber, Payload: *update,
			})
		},
		func(chatID string, seqNum int64, update db.ChatUpdate) {
			chatUpdateHub.Publish(context.Background(), streaming.UpdateEvent[db.ChatUpdate]{
				Key: chatID, SequenceNumber: seqNum, Payload: update,
			})
		},
	)

	// -----------------------------------------------------------------
	// 9. Analytics + telemetry
	// -----------------------------------------------------------------
	logging.Info("[Analytics] Initializing analytics")
	analyticsClient := analytics.NewClientFromSettings(ctx, "", true)
	analytics.SetClient(analyticsClient)
	analytics.SetPrivacyChecker(repo)

	// Telemetry — Sentry in prod (when SENTRY_DSN is set), noop in dev/test.
	telemetry.SetReporter(telemetry.NewReporterFromEnv())

	// -----------------------------------------------------------------
	// 10. Tool execution routing via NATS
	// -----------------------------------------------------------------
	daemonRouterOpts := []toolexec.NATSRouterOption{toolexec.WithDatabase(repo)}
	if cpURL := opts.ControlPlane.URL; cpURL != "" {
		// Daemon records come from repo; the control plane is wired only to
		// resume a suspended managed machine (see serverapi's
		// daemonRouterOptions).
		daemonRouterOpts = append(daemonRouterOpts,
			toolexec.WithDaemonResumer(controlplane.NewDaemonClient(cpURL)),
			// The worker alone may fall back to a trigger's delegated
			// token: an unattended fire has no user JWT.
			toolexec.WithControlPlaneCredentials(automationcred.New(automationcred.Deps{Keys: repo})))
	}
	router := toolexec.NewNATSDaemonRouter(nc, daemonRouterOpts...)
	remoteExecutor.SetDaemonRouter(router)
	natsChecker := nc.IsConnected
	logging.Info("Tool execution routing via NATS")

	// The run-management tools (start_run, control_run, send_to_run) act as the
	// calling chat's owner through the same launcher and run service the
	// api-server uses. They are built here, after the daemon router they
	// depend on, rather than with the other tool options.
	pauseService := v2workflow.NewPauseService(temporalClient, repo)
	runLifecycle := runs.NewService(repo, temporalClient, pauseService)
	runLauncher := launch.NewLauncher(repo, threads.NewService(repo), temporalClient, runLifecycle, v2workflow.SharedTaskQueue)
	chatService, err := chat.New(chat.Deps{
		Logger:    slog.Default(),
		Config:    &typedconfig.Config{},
		Repo:      repo,
		Temporal:  temporalClient,
		Pause:     pauseService,
		TaskQueue: v2workflow.SharedTaskQueue,
		Router:    router,
	})
	if err != nil {
		return fmt.Errorf("chat service: %w", err)
	}
	runService, err := run.New(run.Deps{
		Logger: slog.Default(),
		Config: &typedconfig.Config{},
		Repo:   repo,
		Chats:  chatService,
	})
	if err != nil {
		return fmt.Errorf("run service: %w", err)
	}
	agentRuns := agentruns.New(runLauncher, runService)

	// The same registry the api-server builds, so both agree on which
	// integrations are polled and which deliver events at all.
	triggerPollers, err := webhook.RegistryFromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("integration pollers: %w", err)
	}

	// list_triggers reads as the calling chat's owner through the same
	// TriggerService the api-server serves. Its inbound half needs the
	// provider registry (which integrations deliver events here) and the
	// vault (signed webhooks). PUBLIC_URL is read when the worker has it;
	// without it a webhook's URL is reported as a bare path.
	triggerSyncer, triggerFires := trigger.TemporalBackend(temporalClient, repo, v2workflow.SharedTaskQueue, triggerPollers.IsPolled)
	triggerService, err := trigger.New(trigger.Deps{
		Logger:    slog.Default(),
		Config:    &typedconfig.Config{},
		Repo:      repo,
		Syncer:    triggerSyncer,
		Fires:     triggerFires,
		PublicURL: strings.TrimSpace(os.Getenv("PUBLIC_URL")),
		Sealer:    vaultKeys,
		Catalog:   triggerPollers,
		Intake: triggers.NewIntake(repo, temporalClient, v2workflow.SharedTaskQueue).
			WithWorkflows(triggers.LaunchWorkflows{Repo: repo}).
			WithEnrichers(triggers.OriginEnricherFromEnv(repo.IntegrationWrites(), repo, os.Getenv)),
	})
	if err != nil {
		return fmt.Errorf("trigger service: %w", err)
	}

	runMachine := toolexec.NewRunMachine(router)
	toolsFactory := tools.NewToolsFactory(&tools.ToolsOptions{
		Repo:            repo,
		ControlPlaneURL: opts.ControlPlane.URL,
		// The worker is where spawn_send actually executes (inside the
		// ExecuteTools activity), so this is the wiring that matters most for
		// agent-to-agent delivery.
		AgentMessageNotifier: temporal.NewAgentMessageNotifier(temporalClient, workersetup.ChatWorkflowLookup(repo)),
		// spawn_stop executes here too, and the stop is NOT best-effort: with
		// no stopper the tool reports that it cannot stop anything rather than
		// claiming a cancellation that never left the process.
		SpawnStopper: temporal.NewSpawnStopper(temporalClient, workersetup.ChatWorkflowLookup(repo), repo),
		// generate_image executes here, inside the ExecuteTools activity, so
		// this is the wiring that actually decides whether the tool works.
		ImageGeneratorResolver: resolveImageGenerator,
		// generate_video executes in the worker; the api-server only reads its
		// catalog metadata. The job store is what makes a render resumable.
		VideoGeneratorResolver: resolveVideoGenerator,
		VideoJobs:              videojobs.NewSQLStore(repo.DB.SQLDB()),
		// run_scenario / write_scenario execute on the real runtime via the
		// scenario runner; injected because the runner imports this package's
		// dependents.
		ScenarioRunner: scenariorunner.RunScenario,
		// start_run / control_run / send_to_run execute here, inside the
		// ExecuteTools activity.
		RunStarter:   agentRuns,
		RunLifecycle: agentRuns,
		RunMessenger: agentRuns,
		// list_triggers executes here too.
		TriggerLister: triggertools.New(triggerService),
		// http__request executes here, inside the ExecuteTools activity, and
		// resolves its `connection` for the run's owner through the same
		// source the action node uses: saved connections, plus GitHub tokens
		// delegated by control-plane when one is configured.
		IntegrationCredentials: integrationCredentials,
		WriteLedger:            writeLedger,
		// search_integrations / get_integration_schema execute here too, and
		// read the run owner's connections to report what is connected.
		CatalogSearch: catalogSearch,
		// The worktree tool executes here, on a worker with no access to the
		// user's files: its git work goes to the run's machine instead.
		RunMachine:      runMachine,
		MachineObserver: runMachine,
	})
	// Wire server-side tool execution so PlacementServer / PlacementAny
	// tools execute in the worker process without a daemon round-trip.
	serverExecutor := toolexec.NewLocalToolExecutor(toolsFactory)
	serverExecutor.SetMCPContextBinder(toolexec.NewDaemonMCPContextBinder(router))
	remoteExecutor.SetServerExecutor(serverExecutor)
	// Per-request daemon clients via NATS for server-side tools that still
	// need daemon filesystem/exec access, bound to the run's machine.
	remoteExecutor.SetDaemonClientFactory(toolexec.RemoteDaemonClients(router))

	// -----------------------------------------------------------------
	// 11. Start the worker
	// -----------------------------------------------------------------
	// The launcher a scheduled fire launches through. The prober is the daemon
	// router, which the worker does have — but triggers never ask for a
	// greenfield probe, so it is passed for completeness rather than for the
	// schedule path.
	triggerLauncher := runLauncher

	// One recorder per worker: it folds every response's rate-limit headers into
	// the account that served it, and benches an account the provider refused.
	accountRecorder := accountlimits.NewRecorder(repo, nil)
	defer accountRecorder.Close()

	handle, _, err := workersetup.StartWorker(&workersetup.Config{
		AccountLimits:          accountRecorder,
		TemporalClient:         temporalClient,
		Database:               repo,
		StreamingHub:           streamingHub,
		ToolsFactory:           toolsFactory,
		ToolExecutor:           remoteExecutor,
		DaemonRouter:           remoteExecutor.DaemonRouter(),
		MCPBinder:              toolexec.NewDaemonMCPContextBinder(router),
		ConfigProvider:         storedConfigProvider,
		TriggerLauncher:        triggerLauncher,
		IntegrationCredentials: integrationCredentials,
		WriteLedger:            writeLedger,
		TriggerPollers:         triggerPollers,
		TriggerCredentials:     triggerCredentials(integrationCredentials),
		ControlPlaneURL:        opts.ControlPlane.URL,
	})
	if err != nil {
		return fmt.Errorf("failed to start worker: %w", err)
	}

	// Workflow-event triggers: move run-event outbox rows into dispatch
	// workflows. Every worker runs a relay; rows are leased with SKIP LOCKED,
	// so they share the queue rather than duplicating it.
	relayCtx, stopRelay := context.WithCancel(ctx)
	defer stopRelay()
	go workflowevent.NewRelay(repo, temporalClient, v2workflow.SharedTaskQueue).Run(relayCtx)

	// Account usage: persist the rate-limit headers every LLM response carries, and poll the
	// providers' usage APIs for accounts those headers do not cover. Every worker runs a
	// poller; accounts are leased with SKIP LOCKED, so they share the queue. An expired access
	// token is refreshed through the same per-account coordinator the LLM drivers use, so a
	// poll never consumes a refresh token a live request is about to consume.
	go (&accountlimits.Poller{
		Store: repo,
		Fetcher: &accountlimits.Fetcher{
			Store:  repo,
			Tokens: drivers.RefreshingTokens{Store: repo},
		},
	}).Run(relayCtx)

	// -----------------------------------------------------------------
	// 12. Health endpoint
	// -----------------------------------------------------------------
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","service":"temporal-worker"}`))
	})
	healthMux.Handle("/metrics", observability.MetricsHandler())
	healthMux.HandleFunc("/ready", drainer.ReadinessHandler([]serverkit.ReadyCheck{
		{Name: "db", Check: repo.Ping},
		{Name: "temporal", Check: func(ctx context.Context) error {
			_, err := temporalClient.CheckHealth(ctx, &client.CheckHealthRequest{})
			return err
		}},
		{Name: "nats", Check: func(context.Context) error {
			if natsChecker != nil && !natsChecker() {
				return fmt.Errorf("disconnected")
			}
			return nil
		}},
		{Name: "nats-streaming", Check: func(context.Context) error {
			if !streamingHub.IsConnected() {
				return fmt.Errorf("disconnected")
			}
			return nil
		}},
	}, 0, slog.Default()))
	// The Temporal worker is running: /ready now answers from its checks.
	drainer.MarkReady()

	healthServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", opts.HealthPort),
		Handler:           healthMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		logging.Info("Health endpoint started", "port", opts.HealthPort)
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logging.Error("Health endpoint failed", "error", err)
		}
	}()

	debugserver.Start(opts.PprofPort)

	logging.Info("temporal-worker started successfully")

	// -----------------------------------------------------------------
	// 13. Wait for shutdown signal or worker exit
	// -----------------------------------------------------------------
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logging.Info("Received shutdown signal", "signal", sig)
	case <-handle.Done:
		if handle.Err != nil {
			logging.Error("Worker stopped with error", "error", handle.Err)
		} else {
			logging.Info("Worker stopped unexpectedly")
		}
	case <-ctx.Done():
		logging.Info("Context cancelled")
	}

	// -----------------------------------------------------------------
	// 14. Graceful shutdown
	// -----------------------------------------------------------------
	// Same sequence as the other two mains, for consistency: flip readiness,
	// pause, then stop, bounded by the budget. The pause buys the worker
	// nothing directly (nothing routes to it), but keeping one drain shape
	// across the three mains is what makes the grace-period arithmetic
	// checkable from the deployment alone.
	drainer.Begin() // logs the flip and the pause ("drain: …" phase lines)

	shutdownCtx, cancel := drainer.ShutdownContext()
	defer cancel()

	handle.Worker.Stop()

	// Bounded by the shutdown budget rather than a hardcoded 15s: the wait
	// has to fit inside the grace period the platform derived from
	// SHUTDOWN_TIMEOUT, and a constant cannot track a value it never reads.
	select {
	case <-handle.Done:
		logging.Info("Worker stopped successfully")
	case <-shutdownCtx.Done():
		logging.Warn("Worker did not stop within the shutdown budget",
			"shutdown_timeout", drainer.ShutdownTimeout)
	}

	// Health server LAST, on its own short budget: the shutdown context may
	// be spent by now.
	healthCtx, cancelHealth := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelHealth()
	if err := healthServer.Shutdown(healthCtx); err != nil {
		logging.Error("Health server shutdown error", "error", err)
	}
	// The shutdown's total ("drain: complete"), before telemetry is flushed
	// so the point is exported.
	drainer.Complete()

	analytics.Shutdown()

	// Flush any pending telemetry (Sentry) events before exit.
	telemetry.Flush(5)

	logging.Info("temporal-worker shut down gracefully")
	return nil
}

// resolveImageGenerator adapts the driver layer's image-model selection to the
// narrow interface the generate_image tool declares. The selector arrives from
// the tool's bound `model` parameter — tags:[image-gen] unless a human bound
// something else — and ResolveImageGenerator pins the image output modality on
// top of it, so no selector can degrade into a text model.
func resolveImageGenerator(ctx context.Context, userID string, selector models.ModelSelector) (tools.ImageGenerator, error) {
	return drivers.ResolveImageGenerator(ctx, userID, selector)
}

// resolveVideoGenerator adapts the driver layer's video-model selection to the
// narrow interface the generate_video tool declares.
func resolveVideoGenerator(ctx context.Context, userID string, selector models.ModelSelector) (tools.VideoGenerator, error) {
	return drivers.ResolveVideoGenerator(ctx, userID, selector)
}
