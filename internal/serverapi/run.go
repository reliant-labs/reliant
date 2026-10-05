// Copyright (c) 2025 Reliant Labs
package serverapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	scenariorunner "github.com/reliant-labs/reliant/internal/workflow/scenario/runner"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/certs"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/debugserver"
	"github.com/reliant-labs/reliant/internal/drain"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/videojobs"
	"github.com/reliant-labs/reliant/internal/workersetup"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"

	"github.com/reliant-labs/reliant/internal/workflow/reconciliation"
)

// Options holds all configurable values for the API server.
type Options struct {
	GRPCPort    int
	PprofPort   int
	BindAddress string

	// Database
	DatabaseDriver string
	DatabaseURL    string
	DataDir        string

	// Temporal
	TemporalHost      string
	TemporalPort      int
	TemporalNamespace string

	// NATS / streaming
	NATSURL         string
	StreamingDriver string

	// Health check
	HealthPort int

	// CORS
	CORSAllowedOrigins []string

	// Domain whitelist
	AllowedEmailDomains []string

	// TLS
	TLSCertFile string
	TLSKeyFile  string
	DisableTLS  bool

	// JWT
	JWTPublicKey     string
	JWTPublicKeyFile string
	JWKSURL          string
}

// Run boots the stateless API server with the given options. It blocks until
// a SIGINT/SIGTERM is received, then performs graceful shutdown.
func Run(ctx context.Context, opts Options) error {
	// -----------------------------------------------------------------
	// 1. Validate required config
	// -----------------------------------------------------------------
	if opts.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if opts.NATSURL == "" {
		return fmt.Errorf("NATS_URL is required (daemon routing goes through NATS)")
	}

	streamingDriver, err := streaming.ParseStreamingDriver(opts.StreamingDriver)
	if err != nil {
		return fmt.Errorf("invalid STREAMING_DRIVER %q: %w", opts.StreamingDriver, err)
	}

	// JWT public key: explicit value > file > env var
	jwtPublicKey := opts.JWTPublicKey
	if jwtPublicKey == "" && opts.JWTPublicKeyFile != "" {
		data, err := os.ReadFile(opts.JWTPublicKeyFile)
		if err != nil {
			return fmt.Errorf("failed to read JWT_PUBLIC_KEY_FILE %s: %w", opts.JWTPublicKeyFile, err)
		}
		jwtPublicKey = string(data)
	}
	if jwtPublicKey == "" {
		jwtPublicKey = os.Getenv("RELIANT_JWT_PUBLIC_KEY")
	}

	// JWKS URL: flag > env var (alternative to PEM key)
	jwksURL := opts.JWKSURL
	if jwksURL == "" {
		jwksURL = os.Getenv("RELIANT_JWKS_URL")
	}

	// Auth mode
	authMode := auth.GetAuthMode()

	// -----------------------------------------------------------------
	// 2. Initialize subsystems
	// -----------------------------------------------------------------
	startTime := time.Now()

	// Rollout drain budget, read from the PRE_STOP_DELAY / SHUTDOWN_TIMEOUT
	// env vars the deployment already sets. Built before anything else so
	// the readiness handler below can close over it.
	drainer := drain.New()

	logLevel := logging.GetLogLevel()
	logging.SetupWithRotation(logLevel, false, &logging.RotationConfig{
		Filename:   filepath.Join(opts.DataDir, "logs", "reliant.log"),
		MaxSizeMB:  50,
		MaxBackups: 3,
		MaxAgeDays: 30,
		Compress:   true,
	})
	defer logging.Close() //nolint:errcheck

	// Initialize observability (Prometheus metrics + OTel tracing)
	obsCfg := observability.ConfigFromEnv("reliant-api")
	obsProvider, err := observability.Init(obsCfg)
	if err != nil {
		logging.Warn("Failed to initialize observability", "error", err)
	} else {
		defer func() {
			if err := obsProvider.Shutdown(); err != nil {
				logging.Warn("Failed to shutdown observability", "error", err)
			}
		}()
	}

	logging.Info("Starting Reliant API server (stateless)",
		"grpc_port", opts.GRPCPort,
		"temporal", fmt.Sprintf("%s:%d", opts.TemporalHost, opts.TemporalPort),
		"db_driver", opts.DatabaseDriver,
		"data_dir", opts.DataDir,
		"auth_mode", authMode,
	)

	// Ensure RELIANT_DATA_DIR is set for activities that need to locate files
	if err := os.Setenv("RELIANT_DATA_DIR", opts.DataDir); err != nil {
		logging.Warn("Failed to set RELIANT_DATA_DIR env var", "error", err)
	}

	// Global model registry
	if err := models.InitGlobalRegistryWithUserConfig(nil); err != nil {
		return fmt.Errorf("failed to initialize model registry: %w", err)
	}

	// Database
	dbDriver, err := db.ParseDatabaseDriver(opts.DatabaseDriver)
	if err != nil {
		return fmt.Errorf("invalid DATABASE_DRIVER %q: %w", opts.DatabaseDriver, err)
	}

	// api-server is the single owner of the schema: it is the one process that
	// applies migrations. temporal-worker and gateway wait for it (see
	// db.MigrationPolicy). Keep it that way — a second migrator reintroduces
	// the startup race this policy exists to remove.
	repo, err := db.NewRepoFromConfig(db.DatabaseConfig{
		Driver:  dbDriver,
		DataDir: opts.DataDir,
		URL:     opts.DatabaseURL,
		Migrate: db.MigrateApply,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	logging.Info("Database initialized", "driver", opts.DatabaseDriver)

	// Credential vault: required when hosted, generated when self-hosted.
	// Fails startup on a missing or malformed key.
	vaultKeys, err := db.BootVault(ctx, repo, tokenauthority.ControlPlaneURL() != "", opts.DataDir)
	if err != nil {
		return err
	}
	conns, err := wireConnections(repo, vaultKeys, jwtPublicKey, jwksURL, strings.TrimSpace(os.Getenv("PUBLIC_URL")))
	if err != nil {
		return err
	}
	catalogSearch, err := wireCatalogSearch(repo)
	if err != nil {
		return err
	}

	// API key provider (allows LLM drivers to resolve per-user keys from DB)
	drivers.InitializeAPIKeyProvider(repo)

	// Claim-check store for large Temporal payloads. The worker shares the
	// same table; both processes must use it or neither can read the other's
	// histories. The api-server owns the schema, so it also owns GC.
	payloadStore := claimcheck.NewPostgresStore(repo.DB.SQLDB())
	go payloadStore.RunGC(ctx, claimcheck.GCHorizonFromEnv())

	// External Temporal client
	temporalClient, err := temporal.NewExternalClient(ctx, temporal.ExternalClientConfig{
		Host:         opts.TemporalHost,
		Port:         opts.TemporalPort,
		Namespace:    opts.TemporalNamespace,
		PayloadStore: payloadStore,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to Temporal: %w", err)
	}
	logging.Info("Connected to external Temporal server", "host", opts.TemporalHost, "port", opts.TemporalPort)

	// Tools factory (catalog metadata only; the api-server executes no tools)
	toolsFactory := tools.NewToolsFactory(&tools.ToolsOptions{
		Repo: repo,
		// Lets spawn_send wake a parent parked on its sub-agents instead of
		// leaving the message queued until one of them finishes.
		AgentMessageNotifier: temporal.NewAgentMessageNotifier(temporalClient, workersetup.ChatWorkflowLookup(repo)),
		// Lets spawn_stop reach the root workflow running a sub-agent. Shares
		// the one implementation the UI's cancel path uses.
		SpawnStopper: temporal.NewSpawnStopper(temporalClient, workersetup.ChatWorkflowLookup(repo), repo),
		// Binds generate_image to the driver layer's image-model selection.
		// Injected rather than imported: internal/llm/drivers already imports
		// internal/llm/tools, so the tool cannot reach drivers directly.
		ImageGeneratorResolver: resolveImageGenerator,
		// generate_video executes in the worker; the api-server only reads its
		// catalog metadata. The job store is what makes a render resumable.
		VideoGeneratorResolver: resolveVideoGenerator,
		VideoJobs:              videojobs.NewSQLStore(repo.DB.SQLDB()),
		// run_scenario / write_scenario execute on the real runtime via the
		// scenario runner; injected because the runner imports this package's
		// dependents.
		ScenarioRunner: scenariorunner.RunScenario,
	})
	// Streaming hub
	streamingHub, err := streaming.NewStreamingHub(streaming.StreamingConfig{
		Driver:  streamingDriver,
		NATSUrl: opts.NATSURL,
	})
	if err != nil {
		return fmt.Errorf("failed to create streaming hub: %w", err)
	}
	defer func() { _ = streamingHub.Close() }()

	// Single NATS connection for update hubs and daemon routing
	nc, err := natsutil.Connect(opts.NATSURL)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}
	defer nc.Close()

	// Update hubs for user and chat update event streaming
	var (
		userUpdateHub streaming.UpdateHub[db.UserUpdate]
		chatUpdateHub streaming.UpdateHub[db.ChatUpdate]
	)

	userUpdateHub = streaming.NewNATSUpdateHub[db.UserUpdate](nc, "user.updates", "UserUpdate")
	chatUpdateHub = streaming.NewNATSUpdateHub[db.ChatUpdate](nc, "chat.updates", "ChatUpdate")
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

	// Shared reset-attempt guard: bounds reset-and-replay attempts per workflow
	// so a deterministically-failing run is not reset forever. Shared between the
	// user-driven resume path (PauseService) and the reconciler's automatic
	// stuck-task recovery so both count against one per-workflow bound.
	resetGuard := v2workflow.NewResetAttemptGuard(v2workflow.DefaultMaxResetAttempts)

	// PauseService
	pauseService := v2workflow.NewPauseService(temporalClient, repo)
	pauseService.SetResetGuard(resetGuard)

	// Reconciler (background workflow reconciliation). Namespace must match
	// the Temporal client's so reset (stuck-task recovery) requests land in
	// the right namespace; task queue defaults to the shared worker queue.
	reconcilerCfg := reconciliation.DefaultConfig()
	reconcilerCfg.Namespace = opts.TemporalNamespace
	reconciler := reconciliation.NewReconciler(repo, temporalClient, reconcilerCfg)
	reconciler.SetResetGuard(resetGuard)
	reconciler.StartBackgroundReconciliation(ctx)
	logging.Info("Background reconciler started")

	// Analytics
	logging.Info("[Analytics] Initializing analytics")
	analyticsClient := analytics.NewClientFromSettings(ctx, "", true)
	analytics.SetClient(analyticsClient)
	analytics.SetPrivacyChecker(repo)

	// Telemetry — Sentry in prod (when SENTRY_DSN is set), noop in dev/test.
	telemetry.SetReporter(telemetry.NewReporterFromEnv(
		config.IsDevelopmentEnvironment() || config.IsTestEnvironment()))

	// TLS certificates
	tlsCertFile := opts.TLSCertFile
	tlsKeyFile := opts.TLSKeyFile
	if !opts.DisableTLS {
		if tlsCertFile == "" || tlsKeyFile == "" {
			certsDir := filepath.Join(opts.DataDir, "certs")
			certPaths, err := certs.EnsureCerts(certsDir)
			if err != nil {
				return fmt.Errorf("failed to ensure TLS certificates: %w", err)
			}
			tlsCertFile = certPaths.CertFile
			tlsKeyFile = certPaths.KeyFile
		}
		logging.Info("TLS enabled", "cert", tlsCertFile)
	} else {
		tlsCertFile = ""
		tlsKeyFile = ""
		logging.Info("TLS disabled via DISABLE_TLS=true, using plaintext HTTP")
	}

	// Daemon routing: reuses the same NATS connection
	daemonRouterOpts := []toolexec.NATSRouterOption{toolexec.WithDatabase(repo)}
	if cpURL := controlplane.BaseURLFromEnv(); cpURL != "" {
		// Lets the router distinguish "daemon exists but is still
		// provisioning" from "no daemon at all" via the control plane's
		// ResolveDaemon RPC — without this, a cloud daemon that hasn't
		// registered with THIS process's DB yet (still coming up) falls
		// straight through to the generic "no daemon available" error.
		daemonRouterOpts = append(daemonRouterOpts,
			toolexec.WithControlPlaneClient(reliantv1connect.NewDaemonRegistryServiceClient(http.DefaultClient, cpURL)))
	}
	daemonRouter := toolexec.NewNATSDaemonRouter(nc, daemonRouterOpts...)
	natsChecker := nc.IsConnected
	logging.Info("Using NATS daemon router — daemon services run in separate daemon-gateway process")

	// Backgrounded tool calls end when their process does, and only the daemon
	// running the process can say so. The reconciler asks on every pass; set
	// before its first pass is past the startup delay, and read only by it.
	reconciler.SetBackgroundProcessDaemons(backgroundProcessDaemons{router: daemonRouter, repo: repo})

	// -----------------------------------------------------------------
	// 3. Start servers
	// -----------------------------------------------------------------

	// Background process provider: always DB-backed
	bgProvider := services.NewDBBackgroundProcessProvider(repo, daemonRouter)

	// Inbound triggers: the receivers record an event and start its fire on
	// the worker; launching never happens here.
	inboundRegistry, err := webhook.RegistryFromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("integration webhook providers: %w", err)
	}
	triggerInbound := webhook.NewInbound(repo,
		triggers.NewIntake(repo, temporalClient, v2workflow.SharedTaskQueue).
			WithWorkflows(triggers.LaunchWorkflows{Repo: repo}),
		inboundRegistry, vaultKeys, strings.TrimSpace(os.Getenv("PUBLIC_URL"))).
		WithConnectionSecrets(conns.tokens)
	// GitHub events are access-gated: each owner's repository access is
	// refreshed when a trigger is activated and every few minutes while one
	// exists. Every replica runs the loop; leases share the work.
	ghAccess, err := wireGitHubAccess(repo, vaultKeys, os.Getenv)
	if err != nil {
		return err
	}
	if ghAccess != nil {
		triggerInbound.Access = map[string]webhook.AccessRefresher{ghaccess.IntegrationID: gitHubAccess{ghAccess}}
		go ghAccess.Run(ctx)
	}

	grpcSrv, err := grpcserver.NewServer(&grpcserver.Config{
		Port:           opts.GRPCPort,
		BindAddress:    opts.BindAddress,
		JWTPublicKey:   jwtPublicKey,
		JWKSURL:        jwksURL,
		Connections:    conns.service,
		CatalogSearch:  catalogSearch,
		OAuthRoutes:    conns.oauth,
		TriggerInbound: triggerInbound,
		// Connector/MCP surface. PUBLIC_URL is this server's externally
		// reachable base URL, used to tell a user where to point a
		// third-party MCP client and to build the OAuth discovery document.
		// MCP_OAUTH_ISSUERS names the authorization servers whose tokens the
		// connector endpoint accepts (typically the Supabase project URL);
		// without it, connectors authenticate with `rlat_` connector credentials
		// only and OAuth discovery is not advertised.
		PublicURL:           strings.TrimSpace(os.Getenv("PUBLIC_URL")),
		OAuthIssuers:        splitAndTrim(os.Getenv("MCP_OAUTH_ISSUERS")),
		CORSAllowedOrigins:  opts.CORSAllowedOrigins,
		AllowedEmailDomains: opts.AllowedEmailDomains,
		Database:            repo,
		ToolsFactory:        toolsFactory,
		TemporalClient:      temporalClient,
		StreamingHub:        streamingHub,
		UserUpdateHub:       userUpdateHub,
		ChatUpdateHub:       chatUpdateHub,
		PauseService:        pauseService,
		SharedTaskQueue:     v2workflow.SharedTaskQueue,
		DaemonRouter:        daemonRouter,
		BackgroundProvider:  bgProvider,
		NATSChecker:         natsChecker,
		TLSCertFile:         tlsCertFile,
		TLSKeyFile:          tlsKeyFile,
	})
	if err != nil {
		return fmt.Errorf("failed to create gRPC server: %w", err)
	}
	if err := grpcSrv.Start(); err != nil {
		return fmt.Errorf("failed to start gRPC server: %w", err)
	}
	logging.Info("gRPC/Connect server started", "port", opts.GRPCPort)

	// Subscribe to daemon.v1.events.connected on the DAEMON_EVENTS
	// JetStream stream so we can heal each user's project directories the
	// moment their workspace daemon comes online. The api-server's local
	// ToolsDaemonService never sees daemon connections directly — daemons
	// connect to the daemon-gateway process, which publishes events here.
	if projectSvc := grpcSrv.ProjectService(); projectSvc != nil {
		go func() {
			if err := projectSvc.StartDaemonEventConsumer(ctx, nc); err != nil {
				logging.Warn("daemon-events consumer exited with error", "error", err)
			}
		}()
	}

	// Converge every trigger's Temporal Schedule onto its row, and drop
	// schedules whose row is gone. The DB is the truth, so this repairs drift
	// left by a write that landed while Temporal was unreachable — and an
	// orphan schedule keeps firing for a trigger nobody can see or stop.
	//
	// In the background with backoff: this races a cold Temporal, and
	// refusing to serve until schedules converge would turn an ordering
	// problem into an outage.
	scheduleSyncer := triggers.NewSyncer(
		temporalClient.ScheduleClient(), repo, v2workflow.SharedTaskQueue).
		WithPolledIntegrations(inboundRegistry.IsPolled)
	go triggers.SyncAllOnStartup(ctx, scheduleSyncer)

	// Keep activations of workflow-declared triggers projected from their
	// workflows: an edited cron reconverges its schedule, an edited source
	// re-routes. Fires read the declaration themselves, so this bounds only
	// routing lag. Here, beside SyncAll, because it converges the same
	// schedules.
	go triggers.NewReconciler(repo, triggers.LaunchWorkflows{Repo: repo}, scheduleSyncer).Run(ctx)

	// Restart the fire of any inbound trigger event left pending — the
	// receiver recorded it and its start was lost. The receivers live here,
	// so the redrive does too.
	go triggers.NewRedriver(repo, temporalClient, v2workflow.SharedTaskQueue).Run(ctx)

	// -----------------------------------------------------------------
	// 4. pprof debug server
	// -----------------------------------------------------------------
	debugserver.Start(opts.PprofPort)

	// -----------------------------------------------------------------
	// 5. Health endpoint
	// -----------------------------------------------------------------
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":    "ok",
			"service":   "api-server",
			"auth_mode": auth.GetAuthMode(),
		})
	})
	healthMux.HandleFunc("/ready", drainer.ReadinessHandler("api-server",
		drain.ReadinessCheck{Name: "db", Check: repo.Ping},
		drain.ReadinessCheck{Name: "nats", Check: func(context.Context) error {
			if natsChecker != nil && !natsChecker() {
				return fmt.Errorf("disconnected")
			}
			return nil
		}},
		drain.ReadinessCheck{Name: "nats-streaming", Check: func(context.Context) error {
			if !streamingHub.IsConnected() {
				return fmt.Errorf("disconnected")
			}
			return nil
		}},
	))

	healthAddr := fmt.Sprintf("%s:%d", opts.BindAddress, opts.HealthPort)
	healthServer := &http.Server{
		Addr:              healthAddr,
		Handler:           healthMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		logging.Info("Health endpoint started", "address", healthAddr)
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logging.Error("Health endpoint failed", "error", err)
		}
	}()

	// -----------------------------------------------------------------
	// Startup complete
	// -----------------------------------------------------------------
	logging.Info("Reliant API server ready",
		"startup_duration", time.Since(startTime),
		"grpc_port", opts.GRPCPort,
		"pprof_port", opts.PprofPort,
		"health_port", opts.HealthPort,
	)

	_ = slog.LevelInfo // keep slog import alive for logging.Setup fallback

	// -----------------------------------------------------------------
	// 6. Graceful shutdown
	// -----------------------------------------------------------------
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logging.Info("Received shutdown signal, beginning graceful shutdown", "signal", sig)
	case <-ctx.Done():
		logging.Info("Context cancelled, beginning graceful shutdown")
	}

	// Flip /ready to 503 and hold the listener open for PRE_STOP_DELAY so
	// EndpointSlices and the GKE NEG drop this pod BEFORE it stops serving.
	// Without the pause the listener closes while the pod is still a routing
	// target, which is connection-refused on every rollout. The health
	// server deliberately stays up through all of this — it is shut down
	// last, below, so the probe gets a real 503 rather than a refused
	// connection kubelet only notices at its next period.
	logging.Info("Draining: readiness flipped to not-ready",
		"pre_stop_delay", drainer.PreStopDelay(),
		"shutdown_timeout", drainer.ShutdownTimeout())
	drainer.Begin()

	shutdownCtx, cancel := drainer.ShutdownContext()
	defer cancel()

	// Stop gRPC server
	logging.Info("Stopping gRPC server")
	if err := grpcSrv.Stop(shutdownCtx); err != nil {
		logging.Error("Error stopping gRPC server", "error", err)
	}

	// Stop reconciler
	logging.Info("Stopping reconciler")
	reconciler.Stop()

	// Close Temporal client
	logging.Info("Closing Temporal client")
	temporalClient.Close()

	// Close daemon router
	if daemonRouter != nil {
		_ = daemonRouter.Close()
	}

	// Close database
	logging.Info("Closing database")
	if err := repo.Close(); err != nil {
		logging.Error("Error closing database", "error", err)
	}

	// Health endpoint LAST: it served the 503 "draining" answer for the
	// whole window above, which is the only thing that tells kubelet and the
	// LB to stop routing here. Shutting it down first would have replaced
	// that answer with connection-refused.
	logging.Info("Stopping health endpoint")
	if err := drain.ShutdownHealthServer(healthServer); err != nil {
		logging.Error("Error stopping health endpoint", "error", err)
	}

	// Flush analytics
	logging.Info("[Analytics] Shutting down analytics")
	analytics.Shutdown()

	// Flush telemetry
	logging.Info("[Telemetry] Flushing pending events")
	telemetry.Flush(5)

	logging.Info("API server shut down gracefully")
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

// splitAndTrim parses a comma-separated environment list, dropping empties so
// a trailing comma or a blank setting does not produce a phantom entry.
func splitAndTrim(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// resolveVideoGenerator adapts the driver layer's video-model selection to the
// narrow interface the generate_video tool declares.
func resolveVideoGenerator(ctx context.Context, userID string, selector models.ModelSelector) (tools.VideoGenerator, error) {
	return drivers.ResolveVideoGenerator(ctx, userID, selector)
}
