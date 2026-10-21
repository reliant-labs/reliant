// Copyright (c) 2025 Reliant Labs
package serverworker

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/threads"
	scenariorunner "github.com/reliant-labs/reliant/internal/workflow/scenario/runner"

	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/agentruns"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
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
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
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
	// DatabasePool sizes the pool and bounds the waits on the database
	// (the server roles' DatabaseConfig).
	DatabasePool db.Pool
	DataDir      string

	// Temporal
	TemporalHost      string
	TemporalPort      int
	TemporalNamespace string

	// NATS / streaming
	NATS            natsutil.Endpoint
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
	// value is self-hosted (ServerProcessConfig's ControlPlaneConfig).
	ControlPlane controlplane.Endpoint

	// Log is the configured log level and format (the root config's
	// LogConfig).
	Log logging.Settings

	// PublicURL is this deployment's externally reachable base URL
	// (PUBLIC_URL); empty reports webhook URLs as bare paths.
	PublicURL string
	// VaultKey is the credential vault's key (RELIANT_VAULT_KEY); see
	// vault.Boot.
	VaultKey string
	// Webhooks decides which integrations deliver events here: the role's
	// IntegrationsConfig and the deployment's OAuth clients, which the
	// connections registry reads too.
	Webhooks webhook.RegistryConfig
	// GitHubAppSlug is the GitHub App Reliant's own writes appear as
	// (RELIANT_GITHUB_APP_SLUG); empty attributes no event to the App.
	GitHubAppSlug string
	// GitHubBot is that App's bot as GitHub names it in a webhook's sender.
	GitHubBot github.OurBot
	// Observability is where this role exports telemetry and the
	// environment it files it under (ObservabilityConfig).
	Observability observability.Settings
	// Sentry is where this role reports errors (SentryConfig); only a
	// prod-tier process reports.
	Sentry telemetry.SentryConfig
	// Analytics is where product analytics events go (AnalyticsConfig).
	Analytics analytics.Settings
	// LLM is how this worker calls models (LLMConfig).
	LLM drivers.Settings
	// Preview is how this role builds a remote daemon's preview URLs
	// (PreviewConfig).
	Preview tools.PreviewSettings
}

// Run boots the Temporal worker with the given options and runs its
// components under lifecyclekit.Supervise. It blocks until a SIGINT/SIGTERM
// is received, ctx ends or a component fails, then drains (see workerNodes).
func Run(ctx context.Context, opts Options) error {
	// -----------------------------------------------------------------
	// 1. Validate required config
	// -----------------------------------------------------------------
	if opts.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if opts.NATS.URL == "" {
		return fmt.Errorf("NATS_URL is required (tool routing and streaming go through NATS)")
	}

	// The readiness gate and rollout drain budget, from the role config's PRE_STOP_DELAY and SHUTDOWN_TIMEOUT.
	// The budget is what bounds the Temporal worker's Stop, and that is the
	// part that has to fit inside the pod's grace period (control-plane states
	// 60s for the worker; the default 5s + 30s fits). Nothing routes to the
	// worker, so its pre-stop pause buys nothing — the Temporal worker keeps
	// polling through it — and PRE_STOP_DELAY=-1s on the workload removes it.
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
	obsProvider, err := observability.Init(observability.NewConfig("reliant-temporal-worker", opts.Observability))
	if err != nil {
		return fmt.Errorf("initialize observability: %w", err)
	}
	defer func() {
		if err := obsProvider.Shutdown(); err != nil {
			logging.WarnContext(ctx, "Failed to shutdown observability", "error", err)
		}
	}()

	logging.InfoContext(ctx, "Starting temporal-worker",
		"temporal_host", opts.TemporalHost,
		"temporal_port", opts.TemporalPort,
		"db_driver", opts.DatabaseDriver,
		"data_dir", opts.DataDir,
	)

	// Ensure RELIANT_DATA_DIR is set for activities that need to locate files
	if err := os.Setenv("RELIANT_DATA_DIR", opts.DataDir); err != nil {
		logging.WarnContext(ctx, "Failed to set RELIANT_DATA_DIR env var", "error", err)
	}

	// -----------------------------------------------------------------
	// 3. Global model registry
	// -----------------------------------------------------------------
	// The LLM layer's process settings, before anything can call a model.
	drivers.Configure(opts.LLM)
	tools.ConfigurePreviewURLs(opts.Preview)

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
		Pool:      opts.DatabasePool,
		Migrate:   db.MigrateWait,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer func() { _ = repo.Close() }()

	// Credential vault: required when hosted, generated when self-hosted.
	// Fails startup on a missing or malformed key.
	vaultKeys, err := db.BootVault(ctx, repo, opts.ControlPlane.Hosted(), opts.DataDir, opts.VaultKey)
	if err != nil {
		return err
	}
	connResolver, err := newConnectionResolver(repo, vaultKeys, opts.Webhooks.OAuthClients)
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

	// API key provider (allows LLM drivers to resolve per-user keys from DB)
	drivers.InitializeAPIKeyProvider(repo)
	// The healer only: the api role runs its background sweep.
	_ = drivers.InstallReliantKeyHealer(repo, opts.ControlPlane.URL, opts.ControlPlane.InternalServiceSecret)

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
		PayloadStore: claimcheck.NewPostgresStore(repo.ORM()),
	})
	if err != nil {
		return fmt.Errorf("failed to connect to Temporal: %w", err)
	}
	defer temporalClient.Close()

	logging.InfoContext(ctx, "Connected to external Temporal server",
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
		Driver: streamingDriver,
		NATS:   opts.NATS,
	})
	if err != nil {
		return fmt.Errorf("failed to create streaming hub: %w", err)
	}
	defer func() { _ = streamingHub.Close() }()

	logging.InfoContext(ctx, "Streaming hub initialized", "driver", opts.StreamingDriver)

	// -----------------------------------------------------------------
	// 8. NATS connection + update hubs
	// -----------------------------------------------------------------
	nc, err := natsutil.Connect(opts.NATS)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}
	defer nc.Close()

	userUpdateHub := streaming.NewNATSUpdateHub[db.UserUpdate](nc, "user.updates", "UserUpdate")
	chatUpdateHub := streaming.NewNATSUpdateHub[db.ChatUpdate](nc, "chat.updates", "ChatUpdate")
	logging.InfoContext(ctx, "Update hubs initialized (NATS)")
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
	analytics.Start(opts.Analytics, repo)

	// Telemetry — Sentry in prod (when SENTRY_DSN is set), noop in dev/test.
	telemetry.SetReporter(telemetry.NewReporter(opts.Sentry))

	// -----------------------------------------------------------------
	// 10. Tool execution routing via NATS
	// -----------------------------------------------------------------
	daemonRouterOpts := []toolexec.NATSRouterOption{toolexec.WithDatabase(repo)}
	if cp := opts.ControlPlane; cp.Hosted() {
		cpDeps := controlplane.Deps{BaseURL: cp.URL}
		if secret := cp.InternalServiceSecret; secret != "" {
			cpDeps.Sign = func() (string, error) { return auth.SignInternalServiceToken(secret) }
		}
		cpAdapter := controlplane.New(cpDeps)
		// Daemon records come from repo; the control plane is wired only to
		// resume a suspended managed machine (see serverapi's
		// daemonRouterOptions).
		daemonRouterOpts = append(daemonRouterOpts,
			toolexec.WithDaemonResumer(cpAdapter),
			// The worker alone may fall back to a trigger's delegated
			// token: an unattended fire has no user JWT.
			toolexec.WithControlPlaneCredentials(automationcred.New(automationcred.Deps{Keys: repo})))
		// A run that resumes after a hold wakes its machine as the worker
		// (the owner's session token may have expired meanwhile); see
		// toolexec.RunWaker. That needs the worker's internal-service
		// identity; without one, WakeForRun wakes with the owner's token.
		if cpDeps.Sign != nil {
			daemonRouterOpts = append(daemonRouterOpts, toolexec.WithRunResumer(cpAdapter))
		}
	}
	router := toolexec.NewNATSDaemonRouter(nc, daemonRouterOpts...)
	remoteExecutor.SetDaemonRouter(router)
	logging.InfoContext(ctx, "Tool execution routing via NATS")

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
	github.ConfigureBot(opts.GitHubBot)
	triggerPollers, err := webhook.RegistryFor(opts.Webhooks)
	if err != nil {
		return fmt.Errorf("integration pollers: %w", err)
	}

	// list_triggers reads as the calling chat's owner through the same
	// TriggerService the api-server serves. Its inbound half needs the
	// provider registry (which integrations deliver events here) and the
	// vault (signed webhooks). PublicURL (PUBLIC_URL) is used when the worker has it;
	// without it a webhook's URL is reported as a bare path.
	triggerSyncer, triggerFires := trigger.TemporalBackend(temporalClient, repo, v2workflow.SharedTaskQueue, triggerPollers.IsPolled)
	triggerService, err := trigger.New(trigger.Deps{
		Logger:    slog.Default(),
		Config:    &typedconfig.Config{},
		Repo:      repo,
		Syncer:    triggerSyncer,
		Fires:     triggerFires,
		PublicURL: opts.PublicURL,
		Sealer:    vaultKeys,
		Catalog:   triggerPollers,
		Intake: triggers.NewIntake(repo, temporalClient, v2workflow.SharedTaskQueue).
			WithWorkflows(triggers.LaunchWorkflows{Repo: repo}).
			WithEnrichers(triggers.NewOriginEnricher(repo.IntegrationWrites(), repo, opts.GitHubAppSlug)),
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
		VideoJobs:              videojobs.NewSQLStore(repo.ORM()),
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
	// 11. The Temporal worker and the background loops
	// -----------------------------------------------------------------
	// The launcher a scheduled fire launches through. The prober is the daemon
	// router, which the worker does have — but triggers never ask for a
	// greenfield probe, so it is passed for completeness rather than for the
	// schedule path.
	triggerLauncher := runLauncher

	// One recorder per worker: it folds every response's rate-limit headers into
	// the account that served it, and benches an account the provider refused.
	accountRecorder := accountlimits.NewRecorder(repo, nil)

	temporalWorker, _, err := workersetup.NewWorker(&workersetup.Config{
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
		accountRecorder.Close()
		return fmt.Errorf("failed to create worker: %w", err)
	}

	nodes := workerNodes(workerComponents{
		accountRecorder: accountRecorder,
		writeLedger:     writeLedger,
		writes:          repo.IntegrationWrites(),
		// Account usage: poll the providers' usage APIs for accounts the
		// rate-limit headers do not cover. Every worker runs a poller;
		// accounts are leased with SKIP LOCKED, so they share the queue. An
		// expired access token is refreshed through the same per-account
		// coordinator the LLM drivers use, so a poll never consumes a refresh
		// token a live request is about to consume.
		usagePoller: &accountlimits.Poller{
			Store: repo,
			Fetcher: &accountlimits.Fetcher{
				Store:  repo,
				Tokens: drivers.RefreshingTokens{Store: repo},
			},
		},
		// Workflow-event triggers: move run-event outbox rows into dispatch
		// workflows. Every worker runs a relay; rows are leased with SKIP
		// LOCKED, so they share the queue rather than duplicating it.
		relay:          workflowevent.NewRelay(repo, temporalClient, v2workflow.SharedTaskQueue),
		temporalWorker: temporalWorker,
	})

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
	healthMux.HandleFunc("/ready", drainer.ReadinessHandler(append([]serverkit.ReadyCheck{
		{Name: "db", Check: repo.Ping},
		{Name: "temporal", Check: func(ctx context.Context) error {
			_, err := temporalClient.CheckHealth(ctx, &client.CheckHealthRequest{})
			return err
		}},
		{Name: "nats", Check: func(context.Context) error {
			if !nc.IsConnected() {
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
	}, lifecyclekit.ReadyChecks(nodes...)...), 0, slog.Default()))

	// The probes are the root's, not a node's, as in the gateway: served from
	// before the first node starts (503 "not_ready" until Supervise marks the
	// process ready, which waits for the Temporal worker to be polling) until
	// after the last one stopped (503 "draining" through the whole drain).
	healthAddr := fmt.Sprintf(":%d", opts.HealthPort)
	healthServer := &http.Server{
		Addr:              healthAddr,
		Handler:           healthMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	healthLn, err := net.Listen("tcp", healthAddr)
	if err != nil {
		accountRecorder.Close()
		return fmt.Errorf("failed to bind health endpoint: %w", err)
	}
	go func() {
		logging.InfoContext(ctx, "Health endpoint started", "port", opts.HealthPort)
		if err := healthServer.Serve(healthLn); err != nil && err != http.ErrServerClosed {
			logging.ErrorContext(ctx, "Health endpoint failed", "error", err)
		}
	}()

	debugserver.Start(opts.PprofPort)

	// -----------------------------------------------------------------
	// 13. Run, then drain
	// -----------------------------------------------------------------
	// Supervise starts the nodes in order and marks the process ready once
	// the Temporal worker is polling. On SIGTERM (or ctx ending) it flips
	// readiness, pauses for PRE_STOP_DELAY, then stops the nodes in reverse
	// inside one SHUTDOWN_TIMEOUT budget — the Temporal worker first (see
	// workerNodes) — logs each phase ("drain: …"), and flushes telemetry last.
	logging.InfoContext(ctx, "Starting temporal-worker nodes", "health_port", opts.HealthPort)
	stopCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	runErr := lifecyclekit.Supervise(stopCtx, lifecyclekit.Policy{
		Drainer: drainer,
		Logger:  slog.Default(),
		Flush:   flushTelemetry,
	}, nodes...)

	// Health endpoint LAST — it is what answered 503 "draining" for the
	// whole drain. Its own short budget: the shutdown budget may be spent by
	// now, and its only clients are probes that take microseconds.
	healthCtx, cancelHealth := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelHealth()
	if err := healthServer.Shutdown(healthCtx); err != nil {
		logging.WarnContext(ctx, "Health endpoint did not stop cleanly", "error", err)
	}

	// Note: NATS, the Temporal client, the database and OTel are closed via
	// the defers above, after every node has stopped.
	if runErr != nil {
		return fmt.Errorf("temporal-worker: %w", runErr)
	}
	logging.InfoContext(ctx, "temporal-worker shut down gracefully")
	return nil
}

// workerComponents are the parts of the worker that have a lifecycle.
type workerComponents struct {
	accountRecorder *accountlimits.Recorder
	writeLedger     writeledger.Service
	writes          writeledger.Pruner
	usagePoller     *accountlimits.Poller
	relay           *workflowevent.Relay
	temporalWorker  *workersetup.Worker
}

// workerNodes are the worker's components in START order, each after what it
// depends on; lifecyclekit.Supervise stops them in reverse:
//
//  1. temporal-worker: stops taking work first. Its Drain stops polling,
//     gives in-flight activities a short grace, then cancels the ones still
//     running and waits for them to report — releasing them, so Temporal
//     re-dispatches them to a live replica rather than waiting out their
//     heartbeat timeout (see workersetup.Worker). The Temporal client it
//     reports through closes after Supervise returns.
//     It is also the one node whose Ready gates the process's readiness.
//  2. workflow-event-relay, account-usage-poller, write-ledger-prune: the
//     background loops, which take no Temporal tasks; each ends with its
//     context.
//  3. account-recorder: last, because the activities above record into it;
//     its Close flushes the rate-limit state they recorded.
func workerNodes(c workerComponents) []lifecyclekit.Node {
	return []lifecyclekit.Node{
		{
			Name: "account-recorder",
			Close: func(context.Context) error {
				c.accountRecorder.Close()
				return nil
			},
		},
		{
			Name: "write-ledger-prune",
			Run: func(ctx context.Context) error {
				c.writeLedger.PruneLoop(ctx, c.writes)
				return nil
			},
		},
		lifecyclekit.Component("account-usage-poller", c.usagePoller),
		lifecyclekit.Component("workflow-event-relay", c.relay),
		lifecyclekit.Component("temporal-worker", c.temporalWorker),
	}
}

// flushTelemetry is the root's last step, after every node has stopped: any
// pending Sentry events. Product events need no flush: each is written to the
// log as it happens.
func flushTelemetry(context.Context) error {
	telemetry.Flush(5)
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
