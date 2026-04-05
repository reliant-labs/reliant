// Copyright (c) 2025 Reliant Labs
package serverapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof" //nolint:gosec // G108: pprof is intentionally exposed for debugging
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/api"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/certs"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/toolexec"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/reconciliation"
)

// Options holds all configurable values for the API server.
type Options struct {
	APIPort     int
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

	// TLS
	TLSCertFile string
	TLSKeyFile  string
	DisableTLS  bool

	// JWT
	JWTPublicKey     string
	JWTPublicKeyFile string
}

// Run boots the stateless API server with the given options. It blocks until
// a SIGINT/SIGTERM is received, then performs graceful shutdown.
func Run(ctx context.Context, opts Options) error {
	// -----------------------------------------------------------------
	// 1. Validate required config
	// -----------------------------------------------------------------
	if opts.DatabaseDriver == "postgres" && opts.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required when DATABASE_DRIVER=postgres")
	}
	if opts.NATSURL == "" {
		return fmt.Errorf("NATS_URL is required (daemon routing goes through NATS)")
	}

	streamingDriver, err := streaming.ParseStreamingDriver(opts.StreamingDriver)
	if err != nil {
		return fmt.Errorf("invalid STREAMING_DRIVER %q: %w", opts.StreamingDriver, err)
	}

	// api-server is always stateless — force NATS for cross-process events
	if streamingDriver == streaming.DriverMemory {
		streamingDriver = streaming.DriverNATS
		logging.Info("api-server: forcing STREAMING_DRIVER to nats (memory driver cannot receive cross-process events)")
	}

	// JWT public key: explicit value > file > embedded
	jwtPublicKey := opts.JWTPublicKey
	if jwtPublicKey == "" && opts.JWTPublicKeyFile != "" {
		data, err := os.ReadFile(opts.JWTPublicKeyFile)
		if err != nil {
			return fmt.Errorf("failed to read JWT_PUBLIC_KEY_FILE %s: %w", opts.JWTPublicKeyFile, err)
		}
		jwtPublicKey = string(data)
	}
	if jwtPublicKey == "" {
		jwtPublicKey = auth.GetJWTPublicKey()
	}

	// -----------------------------------------------------------------
	// 2. Initialize subsystems
	// -----------------------------------------------------------------
	startTime := time.Now()

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
		"api_port", opts.APIPort,
		"grpc_port", opts.GRPCPort,
		"temporal", fmt.Sprintf("%s:%d", opts.TemporalHost, opts.TemporalPort),
		"db_driver", opts.DatabaseDriver,
		"data_dir", opts.DataDir,
	)

	// Ensure RELIANT_DATA_DIR is set for activities that need to locate files
	if err := os.Setenv("RELIANT_DATA_DIR", opts.DataDir); err != nil {
		logging.Warn("Failed to set RELIANT_DATA_DIR env var", "error", err)
	}

	// Global model registry
	if err := models.InitGlobalRegistryWithUserConfig(nil); err != nil {
		return fmt.Errorf("failed to initialize model registry: %w", err)
	}
	local.SetLocalConfig(nil)

	// Database
	dbDriver, err := db.ParseDatabaseDriver(opts.DatabaseDriver)
	if err != nil {
		return fmt.Errorf("invalid DATABASE_DRIVER %q: %w", opts.DatabaseDriver, err)
	}

	repo, err := db.NewRepoFromConfig(db.DatabaseConfig{
		Driver:  dbDriver,
		DataDir: opts.DataDir,
		URL:     opts.DatabaseURL,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	logging.Info("Database initialized", "driver", opts.DatabaseDriver)

	// API key provider (allows LLM drivers to resolve per-user keys from DB)
	drivers.InitializeAPIKeyProvider(repo)

	// External Temporal client
	temporalClient, err := temporal.NewExternalClient(ctx, temporal.ExternalClientConfig{
		Host:      opts.TemporalHost,
		Port:      opts.TemporalPort,
		Namespace: opts.TemporalNamespace,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to Temporal: %w", err)
	}
	logging.Info("Connected to external Temporal server", "host", opts.TemporalHost, "port", opts.TemporalPort)

	// Tools factory + remote executor
	mcpManager := mcp.NewManager()
	toolsFactory := tools.NewToolsFactory(&tools.ToolsOptions{
		Repo:       repo,
		MCPManager: mcpManager,
	})
	remoteExecutor := toolexec.NewRemoteExecutor(nil)

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

	if streamingDriver == streaming.DriverNATS && opts.NATSURL != "" {
		userUpdateHub = streaming.NewNATSUpdateHub[db.UserUpdate](nc, "user.updates", "UserUpdate")
		chatUpdateHub = streaming.NewNATSUpdateHub[db.ChatUpdate](nc, "chat.updates", "ChatUpdate")
		logging.Info("Update hubs initialized (NATS)")
	} else {
		userUpdateHub = streaming.NewMemoryUpdateHub[db.UserUpdate]("UserUpdate")
		chatUpdateHub = streaming.NewMemoryUpdateHub[db.ChatUpdate]("ChatUpdate")
		logging.Info("Update hubs initialized (memory)")
	}
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

	// PauseService
	pauseService := v2workflow.NewPauseService(temporalClient, repo)

	// Reconciler (background workflow reconciliation)
	reconciler := reconciliation.NewReconciler(repo, temporalClient, nil)
	reconciler.StartBackgroundReconciliation(ctx)
	logging.Info("Background reconciler started")

	// Analytics
	logging.Info("[Analytics] Initializing analytics")
	analyticsClient := analytics.NewClientFromSettings(ctx, "", true)
	analytics.SetClient(analyticsClient)
	analytics.SetPrivacyChecker(repo)

	// Telemetry (noop in standalone mode — Sentry is optional)
	telemetry.SetReporter(telemetry.NewNoopReporter())

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
	daemonRouter := toolexec.NewNATSDaemonRouter(nc, toolexec.WithDatabase(repo))
	natsChecker := nc.IsConnected
	remoteExecutor.SetDaemonRouter(daemonRouter)
	logging.Info("Using NATS daemon router — daemon services run in separate daemon-gateway process")

	// Wire server-side tool execution
	serverExecutor := toolexec.NewLocalToolExecutor(toolsFactory)
	remoteExecutor.SetServerExecutor(serverExecutor)
	remoteExecutor.SetDaemonClientFactory(func(userID string) daemon.Client {
		return daemon.NewRemoteClient(daemonRouter, userID)
	})

	// -----------------------------------------------------------------
	// 3. Start servers
	// -----------------------------------------------------------------

	// HTTP REST API server
	apiServer := api.NewServer(&api.Config{
		Port:               opts.APIPort,
		BindAddress:        opts.BindAddress,
		JWTPublicKey:       jwtPublicKey,
		CORSAllowedOrigins: opts.CORSAllowedOrigins,
		TLSCertFile:        tlsCertFile,
		TLSKeyFile:         tlsKeyFile,
		NATSChecker:        natsChecker,
	}, repo, opts.DataDir)
	if err := apiServer.Start(); err != nil {
		return fmt.Errorf("failed to start API server: %w", err)
	}
	logging.Info("HTTP API server started", "port", opts.APIPort)

	// Background process provider: always DB-backed
	bgProvider := services.NewDBBackgroundProcessProvider(repo, daemonRouter)

	grpcSrv, err := grpcserver.NewServer(&grpcserver.Config{
		Port:               opts.GRPCPort,
		BindAddress:        opts.BindAddress,
		JWTPublicKey:       jwtPublicKey,
		CORSAllowedOrigins: opts.CORSAllowedOrigins,
		Database:           repo,
		ToolsFactory:       toolsFactory,
		TemporalClient:     temporalClient,
		MCPManager:         mcpManager,
		StreamingHub:       streamingHub,
		UserUpdateHub:      userUpdateHub,
		ChatUpdateHub:      chatUpdateHub,
		PauseService:       pauseService,
		SharedTaskQueue:    v2workflow.SharedTaskQueue,
		ToolExecutor:       remoteExecutor,
		DaemonRouter:       daemonRouter,
		BackgroundProvider: bgProvider,
		NATSChecker:        natsChecker,
		TLSCertFile:        tlsCertFile,
		TLSKeyFile:         tlsKeyFile,
	})
	if err != nil {
		return fmt.Errorf("failed to create gRPC server: %w", err)
	}
	if err := grpcSrv.Start(); err != nil {
		return fmt.Errorf("failed to start gRPC server: %w", err)
	}
	logging.Info("gRPC/Connect server started", "port", opts.GRPCPort)

	// -----------------------------------------------------------------
	// 4. pprof debug server
	// -----------------------------------------------------------------
	go func() {
		pprofMux := http.NewServeMux()
		pprofMux.HandleFunc("/debug/pprof/", http.DefaultServeMux.ServeHTTP)
		pprofMux.HandleFunc("/debug/db", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"pending_writes": %d, "peak_pending_writes": %d}`,
				db.GetPendingWrites(), db.GetPeakPendingWrites())
		})
		pprofMux.HandleFunc("/debug/db/reset-peak", func(w http.ResponseWriter, r *http.Request) {
			db.ResetPeakPendingWrites()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"status": "ok", "message": "peak reset"}`)
		})

		pprofMux.Handle("/metrics", observability.MetricsHandler())

		pprofAddr := fmt.Sprintf("127.0.0.1:%d", opts.PprofPort)
		logging.Info("Starting pprof server", "address", pprofAddr)
		//nolint:gosec // G114: pprof server on localhost, timeouts not needed
		if err := http.ListenAndServe(pprofAddr, pprofMux); err != nil {
			logging.Error("pprof server failed", "error", err)
		}
	}()

	// -----------------------------------------------------------------
	// 5. Health endpoint
	// -----------------------------------------------------------------
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","service":"api-server"}`))
	})
	healthMux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var failures []string

		if err := repo.Ping(r.Context()); err != nil {
			failures = append(failures, "db: "+err.Error())
		}
		if natsChecker != nil && !natsChecker() {
			failures = append(failures, "nats: disconnected")
		}
		if !streamingHub.IsConnected() {
			failures = append(failures, "nats-streaming: disconnected")
		}

		if len(failures) > 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "not_ready",
				"reason": fmt.Sprintf("%v", failures),
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

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
		"api_port", opts.APIPort,
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Stop health endpoint
	logging.Info("Stopping health endpoint")
	healthShutdownCtx, healthCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer healthCancel()
	if err := healthServer.Shutdown(healthShutdownCtx); err != nil {
		logging.Error("Error stopping health endpoint", "error", err)
	}

	// Stop gRPC server
	logging.Info("Stopping gRPC server")
	if err := grpcSrv.Stop(shutdownCtx); err != nil {
		logging.Error("Error stopping gRPC server", "error", err)
	}

	// Close ToolsDaemonService (stops stale-daemon monitor goroutine)
	if tds := grpcSrv.ToolsDaemonService(); tds != nil {
		tds.Close()
	}

	// Stop API server
	logging.Info("Stopping API server")
	if err := apiServer.Stop(shutdownCtx); err != nil {
		logging.Error("Error stopping API server", "error", err)
	}

	// Stop reconciler
	logging.Info("Stopping reconciler")
	reconciler.Stop()

	// Close Temporal client
	logging.Info("Closing Temporal client")
	temporalClient.Close()

	// Close MCP manager
	logging.Info("Closing MCP manager")
	if err := mcpManager.Close(); err != nil {
		logging.Error("Error closing MCP manager", "error", err)
	}

	// Close daemon router
	if daemonRouter != nil {
		_ = daemonRouter.Close()
	}

	// Close database
	logging.Info("Closing database")
	if err := repo.Close(); err != nil {
		logging.Error("Error closing database", "error", err)
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
