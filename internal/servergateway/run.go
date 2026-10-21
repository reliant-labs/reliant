// Copyright (c) 2025 Reliant Labs

// Package servergateway implements the daemon-gateway server bootstrap logic.
//
// It hosts ToolsDaemonService (bidi gRPC streams to tools-daemon processes) and
// a NATS bridge that forwards tool execution requests from api-server replicas
// and Temporal workers to the correct daemon stream.
package servergateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/internal/certs"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/daemonactivity"
	"github.com/reliant-labs/reliant/internal/daemonevents"
	"github.com/reliant-labs/reliant/internal/daemonquery"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/db"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/config"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

// Options holds all configurable values for the daemon-gateway server.
type Options struct {
	// ToolsDaemonPort is the port for the daemon bidi-streaming gRPC server.
	ToolsDaemonPort int
	// HealthPort is the port for the health/readiness HTTP endpoint.
	HealthPort int
	// PreStopDelay and ShutdownTimeout are the rollout drain budget
	// (PRE_STOP_DELAY / SHUTDOWN_TIMEOUT) as serverkit.Drainer takes it: zero
	// is serverkit's default (5s / 30s), a negative PreStopDelay no pause.
	PreStopDelay    time.Duration
	ShutdownTimeout time.Duration
	// BindAddress is the network interface to bind to (e.g. "0.0.0.0").
	BindAddress string

	// DatabaseDriver is the database driver (postgres).
	DatabaseDriver string
	// DatabaseURL is the Postgres connection string (required).
	DatabaseURL string
	// DatabaseDirectURL is the session-capable URL for session-scoped work
	// (db.DatabaseConfig.DirectURL); empty when DatabaseURL already is one.
	DatabaseDirectURL string
	// DatabasePool sizes the pool and bounds the waits on the database
	// (the server roles' DatabaseConfig).
	DatabasePool db.Pool
	// DataDir is the directory for local data (logs, certs).
	DataDir string

	// NATS is the NATS server and its credentials.
	NATS natsutil.Endpoint

	// PprofAddr is the pprof debug listener (host:port); empty serves none.
	PprofAddr string

	// TLSCertFile is the path to the TLS certificate file.
	TLSCertFile string
	// TLSKeyFile is the path to the TLS private key file.
	TLSKeyFile string
	// DisableTLS disables TLS entirely.
	DisableTLS bool

	// Log is the configured log level and format (the root config's
	// LogConfig).
	Log logging.Settings

	// ControlPlane is the control plane hosting this deployment; the zero
	// value is self-hosted (ServerProcessConfig's ControlPlaneConfig).
	ControlPlane controlplane.Endpoint

	// Observability is where this role exports telemetry and the
	// environment it files it under (ObservabilityConfig).
	Observability observability.Settings
}

// Run bootstraps and runs the daemon-gateway server. It blocks until a
// shutdown signal is received or the context is cancelled, then performs
// graceful shutdown.
func Run(ctx context.Context, opts Options) error {
	// -------------------------------------------------------------------------
	// 1. Validate required options
	// -------------------------------------------------------------------------
	if opts.NATS.URL == "" {
		return fmt.Errorf("daemon-gateway: NATS_URL is required")
	}
	if opts.DatabaseURL == "" {
		return fmt.Errorf("daemon-gateway: DATABASE_URL is required")
	}

	// -------------------------------------------------------------------------
	// 2. Initialize subsystems
	// -------------------------------------------------------------------------
	startTime := time.Now()

	// The readiness gate and rollout drain budget.
	drainer := serverkit.NewDrainer(serverkit.Config{PreStopDelay: opts.PreStopDelay, ShutdownTimeout: opts.ShutdownTimeout})

	logLevel := logging.GetLogLevel(opts.Log)
	logging.SetupWithRotation(opts.Log, logLevel, false, &logging.RotationConfig{
		Filename:   filepath.Join(opts.DataDir, "logs", "daemon-gateway.log"),
		MaxSizeMB:  50,
		MaxBackups: 3,
		MaxAgeDays: 30,
		Compress:   true,
	})
	defer logging.Close() //nolint:errcheck

	// Forge owns the OTel runtime; Sentry remains independently initialized below.
	obsProvider, err := observability.Init(observability.NewConfig("daemon-gateway", opts.Observability))
	if err != nil {
		return fmt.Errorf("initialize observability: %w", err)
	}
	defer func() {
		if err := obsProvider.Shutdown(); err != nil {
			logging.WarnContext(ctx, "Failed to shutdown observability", "error", err)
		}
	}()

	logging.InfoContext(ctx, "Starting Reliant daemon-gateway",
		"daemon_port", opts.ToolsDaemonPort,
		"health_port", opts.HealthPort,
		"db_driver", opts.DatabaseDriver,
		"nats_url", opts.NATS.URL,
	)

	// pprof, off unless PprofAddr (PPROF_ADDR) names an address.
	//
	// The gateway had no profiler at all, and that is what turned a memory
	// question into guesswork: dev's gateway holds ~1.06 GB of anonymous
	// memory at rest while prod's holds 18 MB — the same binary, the same
	// role, 60x apart — and there was no way to ask the process what it was
	// holding. Every other answer available (cgroup memory.stat, container
	// logs, the deployment spec) says how MUCH, never WHAT.
	//
	// Off by default because this serves a debug surface with no auth: it
	// binds only when the operator sets the address, and the address is
	// theirs to keep on loopback or a cluster-internal port. control-plane's
	// AppConfig already projects PPROF_ADDR for its own binaries; the gateway
	// simply never read it, so the knob existed on one side of the wire only.
	startPprofServer(opts.PprofAddr)

	// Database
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
	defer func() {
		logging.InfoContext(ctx, "Closing database")
		if err := repo.Close(); err != nil {
			logging.ErrorContext(ctx, "Error closing database", "error", err)
		}
	}()
	logging.InfoContext(ctx, "Database initialized", "driver", opts.DatabaseDriver)

	// Daemon credentials are `rlat_` access tokens, resolved by the ONE token
	// authority — control-plane's Introspect when hosted, reliant's own store
	// when self-hosted. Checked once per connect, UNCACHED: a revoked daemon
	// credential is refused on its next connect.
	var sqlDB *sql.DB
	if repo.DB != nil {
		sqlDB = repo.DB.SQLDB()
	}
	tokenAuthority, authorityMode, err := tokenauthority.New(tokenauthority.DepsFor(opts.ControlPlane, sqlDB))
	if err != nil {
		return fmt.Errorf("daemon credential authority: %w", err)
	}
	logging.InfoContext(ctx, "daemon credential authentication initialized", "authority", authorityMode)

	// NATS connection
	nc, err := natsutil.Connect(opts.NATS)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}
	defer nc.Close()
	logging.InfoContext(ctx, "Connected to NATS", "url", opts.NATS.URL)

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
		logging.InfoContext(ctx, "TLS disabled via DISABLE_TLS=true")
	}

	// -------------------------------------------------------------------------
	// 3. Daemon services
	// -------------------------------------------------------------------------

	// State publisher: emits daemon.v1.state.* lifecycle events on the
	// core-NATS bus. The control-plane derivation consumer fans these out
	// to the derived stores (daemon_attachment, daemons.status, workspace
	// CR). See .dev/simplification-proposal.md, Step 3.
	statePublisher := daemonstate.NewPublisher(nc)

	// User-update hub, publish-only: the gateway is the one process that
	// receives daemon heartbeats and local-model inventories, and it forwards
	// them as ephemeral (never persisted) user updates on the same subject the
	// api-server's StreamUserUpdates subscribes to. Unwired, every heartbeat
	// and the local-models refetch were silently discarded at the source —
	// the api-server's copy of this wiring was removed when the api-server
	// and gateway were split, and the gateway never picked it up.
	userUpdateHub := streaming.NewNATSUpdateHub[db.UserUpdate](nc, "user.updates", "UserUpdate")

	// ToolsDaemonService holds bidi gRPC streams to daemon processes
	toolsDaemonService, err := tools_daemon.New(tools_daemon.Deps{
		Logger:         slog.Default(),
		Config:         &config.Config{},
		Repo:           repo,
		UserUpdateHub:  userUpdateHub,
		StatePublisher: statePublisher,
		// Delivers a tool result whose request reached the daemon through
		// another replica (or an earlier stream) to the worker waiting on
		// the request's result subject. This is what lets a call survive a
		// gateway rollout.
		ResultPublisher: toolexec.NewToolResultPublisher(nc),
	})
	if err != nil {
		_ = userUpdateHub.Close()
		return fmt.Errorf("tools_daemon service: %w", err)
	}
	defer func() { _ = userUpdateHub.Close() }()
	logging.InfoContext(ctx, "Daemon state publisher wired",
		"subjectPrefix", daemonwire.SubjectPrefix,
	)

	// Reliant-side derivation consumer: subscribes to the same subject and
	// mirrors the control plane's lifecycle, removal and registry-snapshot
	// events onto the daemons table, and reaps expired attachment leases.
	// Liveness (daemon_attachment) is NOT its to write: ToolsDaemonService
	// writes each stream's lease itself, in stream order.
	derivation := daemonstate.NewDerivation(nc, repo)
	// A control-plane lifecycle transition (provisioning → running, suspend,
	// an OOM kill) changes what ListDaemons reports, so tell the owner's web
	// clients to refetch rather than leaving them to poll for it.
	derivation.NotifyLifecycleApplied(toolsDaemonService.PublishDaemonListChanged)

	// JetStream context — shared by the tool bridge (pending command drain)
	// and the daemon lifecycle event publisher.
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("failed to create JetStream context: %w", err)
	}

	// NATS tool bridge: per-user subscriptions created/destroyed on daemon connect/disconnect
	toolBridge := toolexec.NewNATSToolBridge(nc, js, toolsDaemonService)
	toolsDaemonService.AddConnectionListener(toolBridge)
	toolsDaemonService.AddConnectionListener(toolexec.NewDaemonLifecyclePublisher(nc))
	if err := toolBridge.Start(); err != nil {
		return fmt.Errorf("failed to start NATS tool bridge: %w", err)
	}
	logging.InfoContext(ctx, "NATS tool bridge started — per-daemon subscriptions on connect/disconnect")

	// Daemon lifecycle event publisher: forwards connect/disconnect to the
	// control-plane admin-server via the DAEMON_EVENTS JetStream stream so
	// `controlplane.daemons.status` stays in sync with reality.
	eventStreamCtx, eventStreamCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := daemonevents.EnsureStream(eventStreamCtx, js); err != nil {
		eventStreamCancel()
		return fmt.Errorf("failed to ensure %s stream: %w", daemonevents.StreamDaemonEvents, err)
	}
	eventStreamCancel()
	eventPublisher, err := daemonevents.NewPublisher(nc)
	if err != nil {
		return fmt.Errorf("failed to build daemon event publisher: %w", err)
	}
	toolsDaemonService.AddConnectionListener(eventPublisher)
	logging.InfoContext(ctx, "Daemon lifecycle event publisher started",
		"stream", daemonevents.StreamDaemonEvents,
		"subjects", daemonevents.SubjectDaemonEventsAll,
	)

	// Pull-architecture status responder. Subscribes per-daemon to
	// daemon.v1.query.<daemonID>.status when a connection is registered and
	// answers from in-memory state. Subject-based routing means the
	// control-plane's request reaches whichever gateway holds the bidi
	// stream — no service registry, no DB column to keep in sync.
	statusResponder := daemonquery.NewResponder(nc, toolsDaemonService)
	toolsDaemonService.AddConnectionListener(statusResponder)
	logging.InfoContext(ctx, "Daemon status responder started",
		"subjectPattern", daemonquery.SubjectQueryPrefix+"<daemonID>.status",
	)

	// Per-user any-live responder. Subscribes to
	// daemon.v1.query.user.<userID>.any-live when a user's FIRST daemon
	// connects to this gateway and unsubscribes when their LAST one
	// disconnects, answering from the in-memory connection map. This gives
	// daemonliveness.ReachableByUser the same NATS-first pull-RPC path the
	// per-daemon variant already has — no responders anywhere means no
	// gateway holds a stream for the user.
	userLivenessResponder := daemonquery.NewUserResponder(nc, toolsDaemonService)
	toolsDaemonService.AddConnectionListener(userLivenessResponder)
	logging.InfoContext(ctx, "Per-user daemon liveness responder started",
		"subjectPattern", daemonquery.SubjectQueryPrefix+"user.<userID>.any-live",
	)

	// Cross-process activity ping subscriber. The control-plane LLM proxy
	// publishes to daemon.v1.activity.user.<userID> when forwarding a call;
	// we bump lastActivity for that user's daemons so the status query
	// reflects real usage between heartbeats.
	activitySubscriber := daemonactivity.NewSubscriber()
	if err := activitySubscriber.Start(nc, toolsDaemonService); err != nil {
		_ = toolBridge.Close()
		return fmt.Errorf("failed to start daemon activity subscriber: %w", err)
	}
	logging.InfoContext(ctx, "Daemon activity subscriber started",
		"subjectPattern", daemonactivity.SubjectWildcard,
	)

	// The daemon listener: the address daemons dial, serving
	// ToolsDaemonService's bidi streams through a chain that authenticates
	// daemon credentials (and only them: ToolsDaemonService declares the
	// daemon audience, and serverkit checks it against the chain's).
	daemonChain, err := grpcserver.NewDaemonChain(grpcserver.DaemonChainDeps{DaemonTokens: tokenAuthority})
	if err != nil {
		_ = toolBridge.Close()
		activitySubscriber.Stop()
		return err
	}
	daemonListener, err := grpcserver.NewDaemonListener(grpcserver.DaemonListenerDeps{
		Port:               opts.ToolsDaemonPort,
		BindAddress:        opts.BindAddress,
		TLSCertFile:        tlsCertFile,
		TLSKeyFile:         tlsKeyFile,
		Chain:              daemonChain,
		ToolsDaemonService: toolsDaemonService,
	})
	if err != nil {
		_ = toolBridge.Close()
		activitySubscriber.Stop()
		return err
	}

	// Outbound daemon connector: dial engine driven by ManagedDaemonReconciler.
	// The connector publishes connect-failure events via the same
	// daemonevents.Publisher used for connected/disconnected so the control
	// plane can persist a reason on the daemon row.
	connector := NewDaemonConnector(toolsDaemonService, eventPublisher)
	managedReconciler := NewManagedDaemonReconciler(js, connector)

	nodes := gatewayNodes(gatewayComponents{
		toolsDaemon:        toolsDaemonService,
		derivation:         derivation,
		toolBridge:         toolBridge,
		activitySubscriber: activitySubscriber,
		listener:           daemonListener,
		reconciler:         managedReconciler,
		connector:          connector,
	})

	// -------------------------------------------------------------------------
	// 4. Health endpoint
	// -------------------------------------------------------------------------
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","service":"daemon-gateway"}`))
	})
	healthMux.Handle("/metrics", observability.MetricsHandler())
	healthMux.HandleFunc("/ready", drainer.ReadinessHandler(append([]serverkit.ReadyCheck{
		{Name: "db", Check: repo.Ping},
		{Name: "nats", Check: func(context.Context) error {
			if !nc.IsConnected() {
				return fmt.Errorf("disconnected")
			}
			return nil
		}},
	}, lifecyclekit.ReadyChecks(nodes...)...), 0, slog.Default()))
	// /flow-health: the APP-FLOW assertion (daemon-connected invariant). The
	// gateway OWNS the attachment registry, so it asserts internally and
	// exposes a STATUS-ONLY 200/503 — no per-daemon detail — that `forge
	// smoke` curls. It takes BOTH the registry (repo) and this process's live
	// connection map (toolsDaemonService): the registry alone cannot tell a
	// daemon that should be connected from a row nobody ever cleaned up. See
	// flowhealth.go.
	healthMux.HandleFunc("/flow-health", flowHealthHandler(repo, toolsDaemonService))

	// The probes are the root's, not a node's: served from before the first
	// node starts (answering 503 "not_ready" until Supervise marks the
	// process ready) until after the last one stopped (503 "draining" through
	// the whole drain), so kubelet and the load balancer always get an
	// answer rather than a refused connection.
	healthAddr := fmt.Sprintf("%s:%d", opts.BindAddress, opts.HealthPort)
	healthServer := &http.Server{
		Addr:              healthAddr,
		Handler:           healthMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	healthLn, err := net.Listen("tcp", healthAddr)
	if err != nil {
		_ = toolBridge.Close()
		activitySubscriber.Stop()
		return fmt.Errorf("failed to bind health endpoint: %w", err)
	}
	go func() {
		logging.InfoContext(ctx, "Health endpoint started", "address", healthAddr)
		if err := healthServer.Serve(healthLn); err != nil && err != http.ErrServerClosed {
			logging.ErrorContext(ctx, "Health endpoint failed", "error", err)
		}
	}()

	// -------------------------------------------------------------------------
	// 5. Run, then drain
	// -------------------------------------------------------------------------
	// Supervise starts the nodes in order and marks the process ready. On
	// SIGTERM (or ctx ending) it flips readiness and keeps serving for
	// PRE_STOP_DELAY, so this replica leaves EndpointSlices before anything
	// stops accepting; then it stops the nodes in reverse, inside one
	// SHUTDOWN_TIMEOUT budget — see gatewayNodes for why that order is the
	// daemon hand-over — and logs each phase ("drain: …").
	logging.InfoContext(ctx, "Starting Reliant daemon-gateway nodes",
		"setup_duration", time.Since(startTime),
		"daemon_port", opts.ToolsDaemonPort,
		"health_port", opts.HealthPort,
	)
	stopCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	runErr := lifecyclekit.Supervise(stopCtx, lifecyclekit.Policy{Drainer: drainer, Logger: slog.Default()}, nodes...)

	// Health endpoint LAST — it is what answered 503 "draining" for the
	// whole drain. Its own short budget: the shutdown budget is usually
	// spent by now, and its only clients are probes that take microseconds.
	healthCtx, cancelHealth := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelHealth()
	if err := healthServer.Shutdown(healthCtx); err != nil {
		logging.WarnContext(ctx, "Health endpoint did not stop cleanly", "error", err)
	}

	// Note: NATS, the database and telemetry are closed via defers above.
	if runErr != nil {
		return fmt.Errorf("daemon-gateway: %w", runErr)
	}
	logging.InfoContext(ctx, "Daemon-gateway shut down gracefully")
	return nil
}

// gatewayComponents are the parts of the gateway that have a lifecycle.
type gatewayComponents struct {
	toolsDaemon        *tools_daemon.Service
	derivation         *daemonstate.Derivation
	toolBridge         *toolexec.NATSToolBridge
	activitySubscriber *daemonactivity.Subscriber
	listener           *grpcserver.DaemonListener
	reconciler         *ManagedDaemonReconciler
	connector          *DaemonConnector
}

// gatewayNodes are the gateway's components in START order, each after what
// it depends on; lifecyclekit.Supervise stops them in reverse. The stop order
// is the daemon hand-over, and it is load-bearing:
//
//  1. managed-reconciler: outbound gateway→daemon dials stop first, so the
//     connector does not redial a daemon this replica is about to drop.
//  2. daemon-listener: its drain is make-before-break. As it begins, every
//     ConnectDaemon stream hands its daemon over (tools_daemon's
//     handOverOnDrain: stop routing here, then GatewayDrain naming the
//     drain's hard deadline); the daemon opens a stream to a replica that
//     is already serving and closes this one once nothing is in flight on
//     it. The drain waits for that, and ends what is still open at the
//     deadline with Unavailable, the daemon's redial signal.
//  3. activity-subscriber, tool-bridge, state-derivation: the NATS sides,
//     after no daemon stream is left to route to.
//  4. tools-daemon: ends anything the service still holds (an outbound
//     connection), and stops the stale-connection sweeper.
func gatewayNodes(c gatewayComponents) []lifecyclekit.Node {
	return []lifecyclekit.Node{
		{
			Name: "tools-daemon",
			// The stale-connection sweeper: nukes connections whose
			// application-level stream has died but whose TCP socket is
			// still ESTABLISHED (observed in production — see Layer B of the
			// half-open-cleanup fix).
			Run: func(ctx context.Context) error {
				c.toolsDaemon.Start(ctx)
				return nil
			},
			Close: func(context.Context) error {
				c.toolsDaemon.Close()
				return nil
			},
		},
		{
			// Mirrors the control plane's lifecycle, removal and
			// registry-snapshot events onto the daemons table. A failure is
			// logged, not fatal: the daemon streams do not depend on it.
			Name: "state-derivation",
			Run: func(ctx context.Context) error {
				logging.InfoContext(ctx, "Daemon state derivation consumer started", "subject", daemonwire.SubjectWildcard)
				if err := c.derivation.Start(ctx); err != nil && ctx.Err() == nil {
					logging.ErrorContext(ctx, "Daemon state derivation consumer failed", "error", err)
				}
				return nil
			},
		},
		{
			Name:  "tool-bridge",
			Close: func(context.Context) error { return c.toolBridge.Close() },
		},
		{
			Name: "activity-subscriber",
			Close: func(context.Context) error {
				c.activitySubscriber.Stop()
				return nil
			},
		},
		lifecyclekit.Component("daemon-listener", c.listener),
		{
			// A failure is logged, not fatal: inbound daemons do not
			// depend on it.
			Name: "managed-reconciler",
			Run: func(ctx context.Context) error {
				logging.InfoContext(ctx, "Managed daemon reconciler started")
				if err := c.reconciler.Start(ctx); err != nil && ctx.Err() == nil {
					logging.ErrorContext(ctx, "ManagedDaemonReconciler failed", "error", err)
				}
				return nil
			},
			Close: func(context.Context) error {
				c.connector.CloseAll()
				return nil
			},
		},
	}
}

// startPprofServer serves net/http/pprof on addr, or does nothing when addr is
// empty.
//
// Its own mux, not http.DefaultServeMux's handlers on a shared listener: the
// profiler must not be reachable from any port the gateway already serves
// traffic on. Failure to listen is logged and never fatal — a debug surface
// that cannot bind must not take the gateway down with it.
func startPprofServer(addr string) {
	if strings.TrimSpace(addr) == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	logging.Info("pprof server starting", "addr", addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.Warn("pprof server stopped", "addr", addr, "error", err)
		}
	}()
}
