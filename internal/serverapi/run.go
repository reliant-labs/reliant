// Copyright (c) 2025 Reliant Labs

// Package serverapi implements the api-server bootstrap: `reliant server api`
// builds every collaborator the stateless API server needs and serves it
// (Run).
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: a composition root dials NATS, Postgres and Temporal to build the collaborators it hands out; the boundaries are those collaborators, and this moves into forge's internal/app in P3c
//forge:exclude-contract: reliant's hand-wired api-server composition root (`reliant server api`); it constructs services rather than being one, and forge's Serve replaces it in P3c (research/forge-migration/PLAN.md)
package serverapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/internal/handlers/project"
	scenariorunner "github.com/reliant-labs/reliant/internal/workflow/scenario/runner"
	"github.com/reliant-labs/reliant/internal/worktreesweep"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/certs"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/debugserver"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/queueddelivery"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
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

	// CORS
	CORSAllowedOrigins []string

	// Domain whitelist
	AllowedEmailDomains []string

	// DisableForgeAPI turns the forge RPC surface off at this server
	// (ApiServerConfig.disable_forge_api, RELIANT_DISABLE_FORGE_API).
	DisableForgeAPI bool

	// TLS
	TLSCertFile string
	TLSKeyFile  string
	DisableTLS  bool

	// JWT
	JWTPublicKey     string
	JWTPublicKeyFile string
	JWKSURL          string

	// AuthMode is auth.ModeSupabase or auth.ModeAPIKey (AUTH_MODE), and
	// AuthAPIKey the shared key apikey mode checks (AUTH_API_KEY).
	AuthMode   string
	AuthAPIKey string
	// RequireEmailVerifiedClaim is AUTH_REQUIRE_EMAIL_VERIFIED_CLAIM; see
	// auth.RequireEmailVerifiedClaim.
	RequireEmailVerifiedClaim bool

	// AuthorizationServer is the public URL of the control plane that issues
	// this deployment's rlat_ tokens (RELIANT_AUTHORIZATION_SERVER).
	AuthorizationServer string
	// OAuthProvider is the provider the desktop sign-in logs in through
	// (RELIANT_AUTH_URL / RELIANT_AUTH_KEY).
	OAuthProvider auth.Provider
	// OAuthIssuers are the authorization servers whose access tokens the
	// connector (MCP) endpoint accepts (MCP_OAUTH_ISSUERS).
	OAuthIssuers []string
	// MaxUploadSize is the largest attachment accepted, in bytes
	// (MAX_UPLOAD_SIZE).
	MaxUploadSize int64
	// WorktreeAutoArchive is RELIANT_WORKTREE_AUTO_ARCHIVE; see worktreesweep.
	WorktreeAutoArchive worktreesweep.AutoArchiveMode

	// Log is the configured log level and format (the root config's
	// LogConfig).
	Log logging.Settings

	// ControlPlane is the control plane hosting this deployment; the zero
	// value is self-hosted (ServerProcessConfig's ControlPlaneConfig).
	ControlPlane controlplane.Endpoint

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
	// LLM is how this server calls models (LLMConfig).
	LLM drivers.Settings
	// PayloadGCHorizon is how long an unreferenced Temporal payload blob is
	// kept (RELIANT_PAYLOAD_BLOB_GC_HORIZON); zero is
	// claimcheck.DefaultGCHorizon.
	PayloadGCHorizon time.Duration
	// Preview is how this role builds a remote daemon's preview URLs
	// (PreviewConfig).
	Preview tools.PreviewSettings
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
	if opts.NATS.URL == "" {
		return fmt.Errorf("NATS_URL is required (daemon routing goes through NATS)")
	}

	streamingDriver, err := streaming.ParseStreamingDriver(opts.StreamingDriver)
	if err != nil {
		return fmt.Errorf("invalid STREAMING_DRIVER %q: %w", opts.StreamingDriver, err)
	}

	// JWT public key: explicit value > file
	jwtPublicKey := opts.JWTPublicKey
	if jwtPublicKey == "" && opts.JWTPublicKeyFile != "" {
		data, err := os.ReadFile(opts.JWTPublicKeyFile)
		if err != nil {
			return fmt.Errorf("failed to read JWT_PUBLIC_KEY_FILE %s: %w", opts.JWTPublicKeyFile, err)
		}
		jwtPublicKey = string(data)
	}

	// JWKS URL (RELIANT_JWKS_URL / --jwks-url), the alternative to a PEM key
	jwksURL := opts.JWKSURL

	authSettings := auth.Settings{
		Mode:                      opts.AuthMode,
		APIKey:                    opts.AuthAPIKey,
		PublicKeyPEM:              jwtPublicKey,
		JWKSURL:                   jwksURL,
		RequireEmailVerifiedClaim: opts.RequireEmailVerifiedClaim,
	}

	// -----------------------------------------------------------------
	// 2. Initialize subsystems
	// -----------------------------------------------------------------
	startTime := time.Now()

	// The readiness gate and rollout drain budget. Built before anything
	// else so the readiness handler below can close over it.
	drainer := serverkit.NewDrainer(serverkit.Config{PreStopDelay: opts.PreStopDelay, ShutdownTimeout: opts.ShutdownTimeout})

	logLevel := logging.GetLogLevel(opts.Log)
	logging.SetupWithRotation(opts.Log, logLevel, false, &logging.RotationConfig{
		Filename:   filepath.Join(opts.DataDir, "logs", "reliant.log"),
		MaxSizeMB:  50,
		MaxBackups: 3,
		MaxAgeDays: 30,
		Compress:   true,
	})
	defer logging.Close() //nolint:errcheck

	// Forge owns the OTel runtime; Sentry remains independently initialized below.
	obsProvider, err := observability.Init(observability.NewConfig("reliant-api-server", opts.Observability))
	if err != nil {
		return fmt.Errorf("initialize observability: %w", err)
	}
	defer func() {
		if err := obsProvider.Shutdown(); err != nil {
			logging.WarnContext(ctx, "Failed to shutdown observability", "error", err)
		}
	}()

	logging.InfoContext(ctx, "Starting Reliant API server (stateless)",
		"grpc_port", opts.GRPCPort,
		"temporal", fmt.Sprintf("%s:%d", opts.TemporalHost, opts.TemporalPort),
		"db_driver", opts.DatabaseDriver,
		"data_dir", opts.DataDir,
		"auth_mode", authSettings.ModeName(),
	)

	// Ensure RELIANT_DATA_DIR is set for activities that need to locate files
	if err := os.Setenv("RELIANT_DATA_DIR", opts.DataDir); err != nil {
		logging.WarnContext(ctx, "Failed to set RELIANT_DATA_DIR env var", "error", err)
	}

	// Global model registry
	// The LLM layer's process settings, before anything can call a model.
	drivers.Configure(opts.LLM)
	tools.ConfigurePreviewURLs(opts.Preview)

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
		Driver:    dbDriver,
		DataDir:   opts.DataDir,
		URL:       opts.DatabaseURL,
		DirectURL: opts.DatabaseDirectURL,
		Pool:      opts.DatabasePool,
		Migrate:   db.MigrateApply,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	logging.InfoContext(ctx, "Database initialized", "driver", opts.DatabaseDriver)
	// The database, the Temporal client and the daemon router close after
	// every node has stopped (deferred, in reverse): the listener's handlers
	// and the loops use them until then.
	defer func() {
		logging.InfoContext(ctx, "Closing database")
		if err := repo.Close(); err != nil {
			logging.ErrorContext(ctx, "Error closing database", "error", err)
		}
	}()

	// Credential vault: required when hosted, generated when self-hosted.
	// Fails startup on a missing or malformed key.
	vaultKeys, err := db.BootVault(ctx, repo, opts.ControlPlane.Hosted(), opts.DataDir, opts.VaultKey)
	if err != nil {
		return err
	}
	conns, err := wireConnections(repo, vaultKeys, opts.Webhooks.OAuthClients, opts.PublicURL, opts.CORSAllowedOrigins, opts.ControlPlane.Hosted())
	if err != nil {
		return err
	}
	catalogSearch, err := wireCatalogSearch(repo, opts.ControlPlane.Hosted())
	if err != nil {
		return err
	}
	inputOptions, err := wireInputOptions(repo, conns.tokens, opts.ControlPlane)
	if err != nil {
		return err
	}

	// API key provider (allows LLM drivers to resolve per-user keys from DB)
	drivers.InitializeAPIKeyProvider(repo)
	keyHealer := drivers.InstallReliantKeyHealer(repo, opts.ControlPlane.URL, opts.ControlPlane.InternalServiceSecret)

	// Claim-check store for large Temporal payloads. The worker shares the
	// same table; both processes must use it or neither can read the other's
	// histories. The api-server owns the schema, so it also owns GC (the
	// payload-gc node).
	payloadStore := claimcheck.NewPostgresStore(repo.ORM())
	gcHorizon := opts.PayloadGCHorizon
	if gcHorizon <= 0 {
		gcHorizon = claimcheck.DefaultGCHorizon
	}

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
	defer func() {
		logging.InfoContext(ctx, "Closing Temporal client")
		temporalClient.Close()
	}()
	logging.InfoContext(ctx, "Connected to external Temporal server", "host", opts.TemporalHost, "port", opts.TemporalPort)

	// Tools factory (catalog metadata only; the api-server executes no tools)
	toolsFactory := tools.NewToolsFactory(&tools.ToolsOptions{
		Repo:            repo,
		ControlPlaneURL: opts.ControlPlane.URL,
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
		VideoJobs:              videojobs.NewSQLStore(repo.ORM()),
		// run_scenario / write_scenario execute on the real runtime via the
		// scenario runner; injected because the runner imports this package's
		// dependents.
		ScenarioRunner: scenariorunner.RunScenario,
	})
	// Streaming hub
	streamingHub, err := streaming.NewStreamingHub(streaming.StreamingConfig{
		Driver: streamingDriver,
		NATS:   opts.NATS,
	})
	if err != nil {
		return fmt.Errorf("failed to create streaming hub: %w", err)
	}
	defer func() { _ = streamingHub.Close() }()

	// Single NATS connection for update hubs and daemon routing
	nc, err := natsutil.Connect(opts.NATS)
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

	// Analytics
	analytics.Start(opts.Analytics, repo)

	// Telemetry — Sentry in prod (when SENTRY_DSN is set), noop in dev/test.
	telemetry.SetReporter(telemetry.NewReporter(opts.Sentry))

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
		logging.InfoContext(ctx, "TLS enabled", "cert", tlsCertFile)
	} else {
		tlsCertFile = ""
		tlsKeyFile = ""
		logging.InfoContext(ctx, "TLS disabled via DISABLE_TLS=true, using plaintext HTTP")
	}

	// Daemon routing: reuses the same NATS connection
	daemonRouter := toolexec.NewNATSDaemonRouter(nc, daemonRouterOptions(repo, opts.ControlPlane.URL)...)
	defer func() { _ = daemonRouter.Close() }()
	natsChecker := nc.IsConnected
	logging.InfoContext(ctx, "Using NATS daemon router — daemon services run in separate daemon-gateway process")

	// Backgrounded tool calls end when their process does, and only the daemon
	// running the process can say so. The reconciler asks on every pass; set
	// before its first pass is past the startup delay, and read only by it.
	reconciler.SetBackgroundProcessDaemons(backgroundProcessDaemons{router: daemonRouter, repo: repo})

	// -----------------------------------------------------------------
	// 3. The API and its loops
	// -----------------------------------------------------------------

	// Inbound triggers: the receivers record an event and start its fire on
	// the worker; launching never happens here.
	github.ConfigureBot(opts.GitHubBot)
	inboundRegistry, err := webhook.RegistryFor(opts.Webhooks)
	if err != nil {
		return fmt.Errorf("integration webhook providers: %w", err)
	}
	triggerInbound := webhook.NewInbound(repo,
		triggers.NewIntake(repo, temporalClient, v2workflow.SharedTaskQueue).
			WithWorkflows(triggers.LaunchWorkflows{Repo: repo}).
			// Who caused an event is resolved against the write ledger before
			// any filter reads it.
			WithEnrichers(triggers.NewOriginEnricher(repo.IntegrationWrites(), repo, opts.GitHubAppSlug)),
		inboundRegistry, vaultKeys, opts.PublicURL).
		WithConnectionSecrets(conns.tokens)
	// GitHub events are access-gated: each owner's repository access is
	// refreshed when a trigger is activated and every few minutes while one
	// exists. Every replica runs the loop; leases share the work.
	ghAccess, err := wireGitHubAccess(repo, vaultKeys, opts.ControlPlane, opts.Webhooks)
	if err != nil {
		return err
	}
	if ghAccess != nil {
		triggerInbound.Access = map[string]webhook.AccessRefresher{ghaccess.IntegrationID: gitHubAccess{ghAccess}}
	}
	// "Only from" stores GitHub user ids and shows logins: this looks people
	// up, as the caller, in both directions.
	triggerSenders, err := wireGitHubSenders(repo, vaultKeys, opts.ControlPlane, opts.Webhooks)
	if err != nil {
		return err
	}

	// Webhook triggers declared `source: alerting` are delivered to by the
	// control plane's alerting: TriggerService provisions their receivers and
	// the trigger reconciler sweeps them. Self-hosted, they are plain webhooks.
	triggerAlerting := wireTriggerAlerting(repo, opts.ControlPlane, opts.PublicURL)

	api, err := grpcserver.NewAPI(&grpcserver.Config{
		Auth:            authSettings,
		Connections:     conns.service,
		CatalogSearch:   catalogSearch,
		InputOptions:    inputOptions,
		TriggerOptions:  optionsChecker{svc: inputOptions},
		OAuthRoutes:     conns.oauth,
		TriggerInbound:  triggerInbound,
		TriggerSenders:  triggerSenders,
		TriggerAlerting: triggerAlerting,
		// Connector/MCP surface. PUBLIC_URL is this server's externally
		// reachable base URL, used to tell a user where to point a
		// third-party MCP client and to build the OAuth discovery document.
		// MCP_OAUTH_ISSUERS names the authorization servers whose tokens the
		// connector endpoint accepts (typically the Supabase project URL);
		// without it, connectors authenticate with `rlat_` connector credentials
		// only and OAuth discovery is not advertised.
		PublicURL:           opts.PublicURL,
		OAuthIssuers:        opts.OAuthIssuers,
		AuthorizationServer: opts.AuthorizationServer,
		OAuthProvider:       opts.OAuthProvider,
		MaxUploadSize:       opts.MaxUploadSize,
		WorktreeAutoArchive: opts.WorktreeAutoArchive,
		DisableForgeAPI:     opts.DisableForgeAPI,
		ControlPlane:        opts.ControlPlane,
		Database:            repo,
		SQL:                 repo.DB.SQLDB(),
		ORM:                 repo.ORM(),
		ConnectorGrants:     connectorgrant.NewSQLStore(connectorgrant.Deps{DB: repo.ORM()}),
		ToolsFactory:        toolsFactory,
		TemporalClient:      temporalClient,
		StreamingHub:        streamingHub,
		UserUpdateHub:       userUpdateHub,
		ChatUpdateHub:       chatUpdateHub,
		PauseService:        pauseService,
		SharedTaskQueue:     v2workflow.SharedTaskQueue,
		DaemonRouter:        daemonRouter,
		NATSChecker:         natsChecker,
	})
	if err != nil {
		return fmt.Errorf("failed to build the API: %w", err)
	}
	// The API listener: the address browsers, the CLI and `rlat_` tokens
	// call, serving the API through a chain that authenticates users (and
	// only them: every service the API mounts declares the user audience,
	// and serverkit checks it against the chain's).
	chain, err := grpcserver.NewUserChain(grpcserver.UserChainDeps{
		Auth:                api.Auth(),
		AllowedEmailDomains: opts.AllowedEmailDomains,
	})
	if err != nil {
		return err
	}
	listener, err := grpcserver.NewAPIListener(grpcserver.APIListenerDeps{
		Port:               opts.GRPCPort,
		BindAddress:        opts.BindAddress,
		TLSCertFile:        tlsCertFile,
		TLSKeyFile:         tlsKeyFile,
		CORSAllowedOrigins: opts.CORSAllowedOrigins,
		Chain:              chain,
		Routes:             api,
	})
	if err != nil {
		return err
	}

	// Messages sent while a chat's machine was down: wake the runs waiting
	// for it when one of the user's machines connects, and deliver the ones
	// whose run already ended. The reconciler sweeps for connects this misses
	// and continues the message of a run it ends as wedged.
	queued := queueddelivery.NewInstrumented(queueddelivery.Deps{Store: repo, Machines: daemonRouter, Deliverer: api.ChatService(), Signaler: temporalClient})
	reconciler.SetQueuedDelivery(queued)

	scheduleSyncer := triggers.NewSyncer(
		temporalClient.ScheduleClient(), repo, v2workflow.SharedTaskQueue).
		WithPolledIntegrations(inboundRegistry.IsPolled)

	nodes := apiNodes(apiComponents{
		payloadStore:      payloadStore,
		payloadGCHorizon:  gcHorizon,
		keyHealer:         keyHealer,
		reconciler:        reconciler,
		gitHubAccess:      ghAccess,
		projects:          api.ProjectService(),
		queued:            queued,
		nc:                nc,
		worktreeSweeper:   api.WorktreeSweeper(),
		scheduleSyncer:    scheduleSyncer,
		triggerReconciler: triggers.NewReconciler(repo, triggers.LaunchWorkflows{Repo: repo}, scheduleSyncer).WithAlerting(triggerAlerting),
		triggerRedriver:   triggers.NewRedriver(repo, temporalClient, v2workflow.SharedTaskQueue),
		listener:          listener,
	})

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
			"auth_mode": authSettings.ModeName(),
		})
	})
	healthMux.HandleFunc("/ready", drainer.ReadinessHandler(append([]serverkit.ReadyCheck{
		{Name: "db", Check: repo.Ping},
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
	}, lifecyclekit.ReadyChecks(nodes...)...), 0, slog.Default()))

	// The probes are the root's, not a node's, as in the gateway and the
	// worker: served from before the first node starts (503 "not_ready"
	// until Supervise marks the process ready, which waits for the API
	// listener to be bound) until after the last one stopped (503
	// "draining" through the whole drain), so kubelet and the load balancer
	// always get an answer rather than a refused connection.
	healthAddr := fmt.Sprintf("%s:%d", opts.BindAddress, opts.HealthPort)
	healthServer := &http.Server{
		Addr:              healthAddr,
		Handler:           healthMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	healthLn, err := net.Listen("tcp", healthAddr)
	if err != nil {
		return fmt.Errorf("failed to bind health endpoint: %w", err)
	}
	go func() {
		logging.InfoContext(ctx, "Health endpoint started", "address", healthAddr)
		if err := healthServer.Serve(healthLn); err != nil && err != http.ErrServerClosed {
			logging.ErrorContext(ctx, "Health endpoint failed", "error", err)
		}
	}()

	// -----------------------------------------------------------------
	// 6. Run, then drain
	// -----------------------------------------------------------------
	// Supervise starts the nodes in order and marks the process ready once
	// the API listener is bound. On SIGTERM (or ctx ending) it flips
	// readiness and keeps serving for PRE_STOP_DELAY, so EndpointSlices and
	// the GKE NEG drop this pod BEFORE it stops serving — without the pause
	// the listener closes while the pod is still a routing target, which is
	// connection-refused on every rollout. Then it stops the nodes in
	// reverse inside one SHUTDOWN_TIMEOUT budget — the listener first (see
	// apiNodes) — logs each phase ("drain: …"), and flushes telemetry last.
	logging.InfoContext(ctx, "Starting Reliant API server nodes",
		"setup_duration", time.Since(startTime),
		"grpc_port", opts.GRPCPort,
		"pprof_port", opts.PprofPort,
		"health_port", opts.HealthPort,
	)
	stopCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	runErr := lifecyclekit.Supervise(stopCtx, lifecyclekit.Policy{
		Drainer: drainer,
		Logger:  slog.Default(),
		Flush:   flushTelemetry,
	}, nodes...)

	// Health endpoint LAST — it is what answered 503 "draining" for the
	// whole drain. Its own short budget: the shutdown budget is usually
	// spent by now, and its only clients are probes that take microseconds.
	healthCtx, cancelHealth := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelHealth()
	if err := healthServer.Shutdown(healthCtx); err != nil {
		logging.WarnContext(ctx, "Health endpoint did not stop cleanly", "error", err)
	}

	// Note: the daemon router, NATS, the Temporal client, the database and
	// OTel are closed via the defers above, after every node has stopped.
	if runErr != nil {
		return fmt.Errorf("api-server: %w", runErr)
	}
	logging.InfoContext(ctx, "API server shut down gracefully")
	return nil
}

// apiComponents are the parts of the api role that have a lifecycle.
// keyHealer and gitHubAccess are nil when this deployment has none.
type apiComponents struct {
	payloadStore      *claimcheck.PostgresStore
	payloadGCHorizon  time.Duration
	keyHealer         *drivers.ReliantKeyHealer
	reconciler        *reconciliation.Reconciler
	gitHubAccess      *ghaccess.Refresher
	projects          *project.Service
	queued            queueddelivery.Service
	nc                *nats.Conn
	worktreeSweeper   *worktreesweep.Sweeper
	scheduleSyncer    *triggers.Syncer
	triggerReconciler *triggers.Reconciler
	triggerRedriver   *triggers.Redriver
	listener          *grpcserver.APIListener
}

// apiNodes are the api role's components in START order; lifecyclekit.Supervise
// stops them in reverse:
//
//  1. api-listener: stops taking calls first. Its drain ends every
//     long-lived stream as it begins (each with the status its client
//     reconnects on, against a replica that is still serving) and waits for
//     the unary calls in flight, while everything they use is still running.
//  2. the loops — trigger redrive, reconcile and startup sync, the worktree
//     sweep, queued delivery and the daemon-events consumer, the GitHub
//     access refresh, the workflow reconciler, the Reliant-key sweep and the
//     payload GC — each ending with its context. None takes traffic, and none
//     depends on another. A failure in one is logged, not fatal: each is
//     repaired by its next pass or the next replica.
//
// What they all use — the daemon router, NATS, the Temporal client and the
// database — closes after the last of them (Run's defers).
func apiNodes(c apiComponents) []lifecyclekit.Node {
	nodes := []lifecyclekit.Node{
		{
			// The claim-check store's GC: the api-server owns the schema, so
			// it owns the unreferenced-payload sweep too.
			Name: "payload-gc",
			Run: func(ctx context.Context) error {
				c.payloadStore.RunGC(ctx, c.payloadGCHorizon)
				return nil
			},
		},
	}
	if c.keyHealer != nil {
		nodes = append(nodes, lifecyclekit.Node{
			// Re-mints every user's Reliant key that control-plane no longer
			// honours, hourly.
			Name: "reliant-key-sweep",
			Run: func(ctx context.Context) error {
				drivers.RunReliantKeySweeps(ctx, c.keyHealer, time.Hour)
				return nil
			},
		})
	}
	nodes = append(nodes, lifecyclekit.Node{
		// Background workflow reconciliation: stuck-task recovery, wedged
		// runs, queued messages whose connect the consumer missed.
		Name: "workflow-reconciler",
		Run: func(ctx context.Context) error {
			c.reconciler.StartBackgroundReconciliation(ctx)
			<-ctx.Done()
			return nil
		},
		Close: func(context.Context) error {
			c.reconciler.Stop()
			return nil
		},
	})
	if c.gitHubAccess != nil {
		nodes = append(nodes, lifecyclekit.Node{
			// GitHub events are access-gated: each owner's repository access
			// is refreshed while a trigger exists. Every replica runs it;
			// leases share the work.
			Name: "github-access",
			Run: func(ctx context.Context) error {
				c.gitHubAccess.Run(ctx)
				return nil
			},
		})
	}
	return append(nodes,
		lifecyclekit.Node{
			// daemon.v1.events.connected on the DAEMON_EVENTS JetStream
			// stream heals each user's project directories the moment their
			// workspace daemon comes online. The api-server never sees daemon
			// connections itself — daemons connect to the daemon-gateway,
			// which publishes the events.
			Name: "daemon-events-consumer",
			Run: func(ctx context.Context) error {
				if err := c.projects.StartDaemonEventConsumer(ctx, c.nc); err != nil && ctx.Err() == nil {
					logging.WarnContext(ctx, "daemon-events consumer exited with error", "error", err)
				}
				return nil
			},
		},
		lifecyclekit.Node{
			Name: "queued-delivery",
			Run: func(ctx context.Context) error {
				if err := c.queued.Run(ctx, c.nc); err != nil && ctx.Err() == nil {
					logging.WarnContext(ctx, "queued-delivery consumer exited with error; the reconciler's sweep still delivers", "error", err)
				}
				return nil
			},
		},
		lifecyclekit.Node{
			// Settles archived worktrees' directories on their daemons:
			// removes what is provably safe, records what is held, and locks
			// live worktrees as reliant-owned. Every replica runs it; a
			// daemon's answers are idempotent.
			Name: "worktree-sweeper",
			Run: func(ctx context.Context) error {
				c.worktreeSweeper.Run(ctx, worktreeSweepInterval)
				return nil
			},
		},
		lifecyclekit.Node{
			// Converges every trigger's Temporal Schedule onto its row, and
			// drops schedules whose row is gone. The DB is the truth, so this
			// repairs drift left by a write that landed while Temporal was
			// unreachable — and an orphan schedule keeps firing for a trigger
			// nobody can see or stop. Once, with backoff, beside the
			// listener: it races a cold Temporal, and refusing to serve until
			// schedules converge would turn an ordering problem into an
			// outage.
			Name: "trigger-schedule-sync",
			Run: func(ctx context.Context) error {
				triggers.SyncAllOnStartup(ctx, c.scheduleSyncer)
				return nil
			},
		},
		lifecyclekit.Node{
			// Keeps activations of workflow-declared triggers projected from
			// their workflows: an edited cron reconverges its schedule, an
			// edited source re-routes. Fires read the declaration themselves,
			// so this bounds only routing lag.
			Name: "trigger-reconciler",
			Run: func(ctx context.Context) error {
				c.triggerReconciler.Run(ctx)
				return nil
			},
		},
		lifecyclekit.Node{
			// Restarts the fire of any inbound trigger event left pending —
			// the receiver recorded it and its start was lost. The receivers
			// live here, so the redrive does too.
			Name: "trigger-redriver",
			Run: func(ctx context.Context) error {
				c.triggerRedriver.Run(ctx)
				return nil
			},
		},
		lifecyclekit.Component("api-listener", c.listener),
	)
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

// wireTriggerAlerting builds the provisioner of `source: alerting` webhook
// triggers' alert receivers, or nil without a control plane. The calls are
// internal-service signed: the sweep runs with no caller, and a resumed
// trigger's owner may have no live session.
func wireTriggerAlerting(repo *db.Repo, cp controlplane.Endpoint, publicURL string) *triggers.Alerting {
	if !cp.Hosted() {
		return nil
	}
	secret := cp.InternalServiceSecret
	if secret == "" {
		logging.Warn("alert-triage automations are plain webhooks here: RELIANT_CONTROL_PLANE_URL is set but INTERNAL_SERVICE_SECRET is not")
		return nil
	}
	if strings.TrimSpace(publicURL) == "" {
		// The control plane's Alertmanager needs an absolute URL to post to;
		// a bare /hooks path would be refused at every enable.
		logging.Warn("alert-triage automations are plain webhooks here: PUBLIC_URL is not set, so Alertmanager has no URL to deliver to")
		return nil
	}
	receivers := controlplane.New(controlplane.Deps{
		BaseURL: cp.URL,
		Sign:    func() (string, error) { return auth.SignInternalServiceToken(secret) },
	})
	return triggers.NewAlerting(receivers, repo, publicURL)
}
