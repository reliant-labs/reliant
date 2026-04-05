// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof" //nolint:gosec // G108: pprof is intentionally exposed for development/debugging
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/api"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/certs"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/envutil"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	grpcinterceptors "github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/integration"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/monolith"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/pidlock"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

func newMonolithCmd() *cobra.Command {
	var dataDir string

	cmd := &cobra.Command{
		Use:   "monolith",
		Short: "Run the full Reliant monolith (embedded Temporal + SQLite + API + daemon)",
		Long: `Starts all Reliant components in a single process: embedded Temporal server,
SQLite database, HTTP API, gRPC server, and in-process tools daemon.

This is the local development mode used by the Electron desktop app.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMonolith(cmd, args, &dataDir)
		},
	}

	cmd.Flags().StringVar(&dataDir, "data-dir", envOrDefault("RELIANT_DATA_DIR", "./data"), "Base data directory (default: ./data or RELIANT_DATA_DIR)")

	return cmd
}

func runMonolith(_ *cobra.Command, _ []string, dataDir *string) error {
	// Load configuration from environment
	// Frontend port 0 means auto-assign (find free consecutive ports)
	frontendPort := envutil.GetEnvInt("TEMPORAL_FRONTEND_PORT", 0)

	// Acquire PID lock to ensure only one backend runs per data directory.
	// This prevents zombie processes from holding the temporal.db lock.
	lock := pidlock.New(*dataDir)
	if err := lock.AcquireWithRetry(); err != nil {
		return fmt.Errorf("failed to acquire PID lock (another backend may be running for %s): %w", *dataDir, err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			logging.Warn("Failed to release PID lock", "error", err)
		}
	}()

	// Initialize logging with rotation
	// Logs go to ./data/logs/reliant.log with automatic rotation
	logLevel := logging.GetLogLevel()
	logging.SetupWithRotation(logLevel, false, &logging.RotationConfig{
		Filename:   filepath.Join(*dataDir, "logs", "reliant.log"),
		MaxSizeMB:  50,   // Rotate when log reaches 50MB
		MaxBackups: 3,    // Keep 3 old log files
		MaxAgeDays: 30,   // Delete logs older than 30 days
		Compress:   true, // Compress rotated logs
	})
	// Ensure logs are flushed on shutdown
	defer logging.Close() //nolint:errcheck // Best-effort cleanup on shutdown

	// Initialize observability (Prometheus metrics + OTel tracing)
	obsCfg := observability.ConfigFromEnv("reliant-monolith")
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

	// Ensure RELIANT_DATA_DIR is set for workflow activities that need to locate files
	// This is critical for attachment loading in the LLM context building
	if err := os.Setenv("RELIANT_DATA_DIR", *dataDir); err != nil {
		logging.Warn("Failed to set RELIANT_DATA_DIR env var", "error", err)
	}

	// Initialize global model registry with embedded defaults.
	// Runtime activities rehydrate per-project settings via stored config snapshots.
	if err := models.InitGlobalRegistryWithUserConfig(nil); err != nil {
		return fmt.Errorf("failed to initialize default model registry: %w", err)
	}
	local.SetLocalConfig(nil)

	// Emit a model@driver thinking capability matrix for observability/debugging.
	thinkingMatrixPath := filepath.Join(*dataDir, "thinking_capability_matrix.json")
	if err := models.WriteUserVisibleThinkingCapabilityMatrix(thinkingMatrixPath); err != nil {
		logging.Warn("Failed to write thinking capability matrix", "path", thinkingMatrixPath, "error", err)
	} else {
		logging.Info("Wrote thinking capability matrix", "path", thinkingMatrixPath)
	}

	// Note: MCP servers are loaded per-project on-demand, not at startup
	// This allows the backend to work with multiple projects without needing
	// to know all project paths upfront. The MCP handler will load configs
	// from each project's directory when needed.

	// Determine Temporal log level based on environment
	// Production: warn (reduce noise), Development: info
	temporalLogLevel := "info"
	if config.IsProductionEnvironment() {
		temporalLogLevel = "warn"
	}

	cfg := &integration.Config{
		DatabasePath: *dataDir,
		TemporalConfig: &temporal.ServerConfig{
			DatabasePath: filepath.Join(*dataDir, "temporal.db"),
			Namespaces:   []string{"default", "reliant"},
			FrontendPort: frontendPort,
			Ephemeral:    false,
			LogLevel:     temporalLogLevel,
		},
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		// MCPServers are loaded per-project on-demand
	}

	// Initialize analytics (will be replaced after DB is ready with user-specific settings)
	logging.Info("[Statsig] Initializing analytics")
	analyticsClient := analytics.NewClientFromSettings(context.Background(), "", true)
	analytics.SetClient(analyticsClient)

	// Create server
	startTime := time.Now()
	logging.Info("Creating Reliant V2 server")
	serverCreateStart := time.Now()
	server, err := integration.NewServer(cfg)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}
	logging.Info("✓ Server created", "duration", time.Since(serverCreateStart))

	// Configure analytics to respect per-user privacy settings from the database
	logging.Info("[Analytics] Configuring privacy checker")
	analytics.SetPrivacyChecker(server.Database())
	logging.Info("[Analytics] Privacy checker configured - analytics will now respect user privacy settings")

	// Initialize Sentry for crash/error reporting
	// Skip in development mode to reduce noise
	if !config.IsDevelopmentEnvironment() {
		initializeSentry()
	} else {
		logging.Info("[Sentry] Skipping initialization in development mode")
		telemetry.SetReporter(telemetry.NewNoopReporter())
	}

	// Start integration server (Temporal + Workers)
	ctx := context.Background()
	logging.Info("Starting integration server (Temporal + Workers)")
	serverStartTime := time.Now()
	if err := server.Start(ctx); err != nil {
		return fmt.Errorf("failed to start integration server: %w", err)
	}
	logging.Info("✓ Integration server started", "duration", time.Since(serverStartTime))

	// Use embedded JWT public key
	jwtPublicKey := auth.SupabasePublicKeyPEM
	logging.Info("Using embedded ES256 JWT public key")

	// Generate/load TLS certificates for HTTP/2 support (unless DISABLE_TLS is set)
	// TLS certificate configuration. Priority:
	// 1. DISABLE_TLS=true → no TLS
	// 2. TLS_CERT_FILE + TLS_KEY_FILE → externally-provided certs (e.g., mkcert)
	// 3. Otherwise → self-signed certs stored in data dir
	var tlsCertFile, tlsKeyFile string
	if os.Getenv("DISABLE_TLS") == "true" {
		logging.Info("TLS disabled via DISABLE_TLS=true, using HTTP (no TLS)")
	} else if envCert, envKey := os.Getenv("TLS_CERT_FILE"), os.Getenv("TLS_KEY_FILE"); envCert != "" && envKey != "" {
		tlsCertFile = envCert
		tlsKeyFile = envKey
		logging.Info("Using externally-provided TLS certificates", "cert", tlsCertFile, "key", tlsKeyFile)
	} else {
		certsDir := filepath.Join(*dataDir, "certs")
		certPaths, err := certs.EnsureCerts(certsDir)
		if err != nil {
			return fmt.Errorf("failed to ensure TLS certificates: %w", err)
		}
		tlsCertFile = certPaths.CertFile
		tlsKeyFile = certPaths.KeyFile
	}

	// CORS origins: "*" → wildcard, otherwise comma-separated list of origins
	corsOriginsRaw := envutil.GetEnv("CORS_ALLOWED_ORIGINS", "*")
	var corsAllowedOrigins []string
	if corsOriginsRaw == "*" {
		corsAllowedOrigins = []string{"*"}
	} else {
		corsAllowedOrigins = strings.Split(corsOriginsRaw, ",")
	}

	// Bind address: default 127.0.0.1 for local, 0.0.0.0 for containers
	bindAddress := envutil.GetEnv("BIND_ADDRESS", "127.0.0.1")

	// Start HTTP API server (with TLS if certs are available)
	apiPort := envutil.GetEnvInt("API_PORT", 8080)
	if tlsCertFile != "" {
		logging.Info("Starting HTTPS API server with TLS", "port", apiPort)
	} else {
		logging.Info("Starting HTTP API server without TLS", "port", apiPort)
	}
	apiStartTime := time.Now()
	apiServer := api.NewServer(&api.Config{
		Port:                  apiPort,
		BindAddress:           bindAddress,
		JWTPublicKey:          jwtPublicKey,
		CORSAllowedOrigins:    corsAllowedOrigins,
		TLSCertFile:           tlsCertFile,
		TLSKeyFile:            tlsKeyFile,
		ManagesLocalProcesses: true,
	}, server.Database(), server.DataDir())
	if err := apiServer.Start(); err != nil {
		return fmt.Errorf("failed to start API server: %w", err)
	}
	logging.Info("✓ API server started", "duration", time.Since(apiStartTime))

	// Start gRPC/Connect server
	grpcPort := envutil.GetEnvInt("GRPC_PORT", 9090)
	toolsDaemonPort := envutil.GetEnvInt("TOOLS_DAEMON_PORT", 9190)
	if tlsCertFile != "" {
		logging.Info("Starting gRPC/Connect server with TLS", "port", grpcPort)
		logging.Info("Starting dedicated tools-daemon gRPC server with TLS", "port", toolsDaemonPort)
	} else {
		logging.Info("Starting gRPC/Connect server without TLS (h2c)", "port", grpcPort)
		logging.Info("Starting dedicated tools-daemon gRPC server without TLS (h2c)", "port", toolsDaemonPort)
	}
	grpcStartTime := time.Now()
	remoteToolExecutor, ok := server.ToolExecutor().(*toolexec.RemoteExecutor)
	if !ok {
		return fmt.Errorf("integration server ToolExecutor is not a *RemoteExecutor — monolith requires RemoteExecutor")
	}

	// The integration server creates ToolsDaemonService + LocalDaemonRouter
	// during Start(), so the worker has a fully wired DaemonRouter from the start.
	// Reuse those instances here for the gRPC and daemon servers.
	sharedToolsDaemonService := server.ToolsDaemonService()
	if sharedToolsDaemonService == nil {
		return fmt.Errorf("failed to initialize shared tools daemon service")
	}

	// Create memory update hubs for user and chat events
	userUpdateHub := streaming.NewMemoryUpdateHub[db.UserUpdate]("UserUpdate")
	chatUpdateHub := streaming.NewMemoryUpdateHub[db.ChatUpdate]("ChatUpdate")
	defer func() { _ = userUpdateHub.Close() }()
	defer func() { _ = chatUpdateHub.Close() }()

	// Wire repo update notifiers to push events to update hubs
	concreteRepo, ok := server.Database().(*db.Repo)
	if !ok {
		return fmt.Errorf("integration server Database is not a *db.Repo — cannot wire update notifiers")
	}
	concreteRepo.SetUpdateNotifiers(
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

	grpcSrv, err := grpcserver.NewServer(&grpcserver.Config{
		Port:               grpcPort,
		BindAddress:        bindAddress,
		JWTPublicKey:       jwtPublicKey,
		CORSAllowedOrigins: corsAllowedOrigins,
		Database:           server.Database(),
		ToolsFactory:       server.ToolsFactory(),
		TemporalClient:     server.Client(),
		StreamingHub:       server.StreamingHub(),
		UserUpdateHub:      userUpdateHub,
		ChatUpdateHub:      chatUpdateHub,
		PauseService:       server.PauseService(),
		SharedTaskQueue:    server.SharedTaskQueueName(),
		ToolExecutor:       remoteToolExecutor,
		ToolsDaemonService: sharedToolsDaemonService,
		DaemonRouter:       server.DaemonRouter(),
		BackgroundProvider: services.NewLocalBackgroundProcessProvider(),
		TLSCertFile:        tlsCertFile,
		TLSKeyFile:         tlsKeyFile,
		LocalMode:          true,
	})
	if err != nil {
		logging.Warn("gRPC server auth setup issue (local mode, non-fatal)", "error", err)
	}
	if err := grpcSrv.Start(); err != nil {
		return fmt.Errorf("failed to start gRPC server: %w", err)
	}

	daemonUserID, err := resolveDaemonUserID(server.Database())
	if err != nil {
		return fmt.Errorf("failed to resolve in-process daemon identity: %w", err)
	}

	daemonStarter := monolith.NewLazyDaemonStarter(monolith.LazyDaemonStarterConfig{
		Repo:                 server.Database(),
		SharedToolsDaemonSvc: sharedToolsDaemonService,
		RemoteToolExecutor:   remoteToolExecutor,
		TLSCertFile:          tlsCertFile,
		TLSKeyFile:           tlsKeyFile,
		ToolsDaemonPort:      toolsDaemonPort,
		DataDir:              *dataDir,
	})

	// Register the lazy starter with the LocalDaemonRouter so that daemon
	// commands can trigger daemon startup on first authenticated request.
	if localRouter, ok := server.DaemonRouter().(*toolexec.LocalDaemonRouter); ok {
		localRouter.SetLazyStarter(daemonStarter.EnsureStarted)
	}

	if daemonUserID != "" {
		if _, err := daemonStarter.EnsureStarted(daemonUserID); err != nil {
			return fmt.Errorf("failed to start in-process daemon at boot: %w", err)
		}
	} else {
		logging.Info("Starting in signed-out mode: in-process daemon will start on first authenticated request")
	}

	logging.Info("✓ gRPC/Connect server started (HTTPS/HTTP2)", "duration", time.Since(grpcStartTime))

	// Start pprof server for profiling and debugging
	// Use this to diagnose lock-ups, deadlocks, and performance issues
	pprofPort := envutil.GetEnvInt("PPROF_PORT", 6060)
	go func() {
		pprofMux := http.NewServeMux()

		// Register pprof handlers explicitly
		pprofMux.HandleFunc("/debug/pprof/", http.DefaultServeMux.ServeHTTP)

		// Add custom debug endpoint for DB write queue metrics
		pprofMux.HandleFunc("/debug/db", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"pending_writes": %d, "peak_pending_writes": %d}`,
				db.GetPendingWrites(), db.GetPeakPendingWrites())
		})

		// Reset peak endpoint
		pprofMux.HandleFunc("/debug/db/reset-peak", func(w http.ResponseWriter, r *http.Request) {
			db.ResetPeakPendingWrites()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"status": "ok", "message": "peak reset"}`)
		})

		pprofMux.Handle("/metrics", observability.MetricsHandler())

		pprofAddr := fmt.Sprintf("127.0.0.1:%d", pprofPort)
		logging.Info("Starting pprof server", "address", pprofAddr)
		//nolint:gosec // G114: pprof server on localhost, timeouts not needed
		if err := http.ListenAndServe(pprofAddr, pprofMux); err != nil {
			logging.Error("pprof server failed", "error", err)
		}
	}()

	logging.Info("✓✓✓ TOTAL STARTUP TIME", "duration", time.Since(startTime))

	fmt.Println("╔════════════════════════════════════════════════════════════╗")
	fmt.Println("║                    Reliant V2 Server                       ║")
	fmt.Println("╚════════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("✓ Data Directory:  ", *dataDir)
	fmt.Println("✓ Reliant DB:      ", filepath.Join(*dataDir, "reliant.db"))
	fmt.Println("✓ Temporal DB:     ", cfg.TemporalConfig.DatabasePath)
	fmt.Println("✓ Temporal UI:      http://", server.TemporalFrontendHostPort())
	fmt.Println("✓ HTTP API:         http://localhost:", apiPort)
	fmt.Println("✓ gRPC/Connect:     http://localhost:", grpcPort)
	fmt.Println("✓ Tools Daemon gRPC:http://localhost:", toolsDaemonPort)
	fmt.Printf("✓ pprof:            http://localhost:%d/debug/pprof/\n", pprofPort)
	fmt.Println()
	fmt.Println("Press Ctrl+C to stop...")

	// Wait for interrupt or parent process death (Suicide Pact)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// Monitor stdin for EOF (Parent process death) - but only in production mode.
	// In development mode (when started by Air hot reload), stdin is not piped,
	// so we skip this monitoring to avoid immediate shutdown.
	parentDeathCh := make(chan struct{})
	if !config.IsDevelopmentEnvironment() {
		// This implements the "Suicide Pact" pattern where the child process (this backend)
		// automatically exits when the parent process (Electron) closes its stdin pipe.
		go func() {
			buf := make([]byte, 1024)
			for {
				_, err := os.Stdin.Read(buf)
				if err != nil {
					// EOF or error means the pipe is closed/broken
					// This happens when the parent Electron process exits or crashes
					logging.Info("Parent process pipe closed (stdin), initiating shutdown", "error", err)
					close(parentDeathCh)
					return
				}
				// If we receive data (unexpected), just ignore it and continue monitoring
			}
		}()
	} else {
		logging.Info("Development mode: stdin monitoring disabled (Air hot reload)")
	}

	var shutdownReason string
	select {
	case sig := <-sigCh:
		shutdownReason = fmt.Sprintf("signal %s", sig.String())
	case <-parentDeathCh:
		shutdownReason = "parent process died (stdin closed)"
	}

	logging.Info("Received shutdown trigger, beginning graceful shutdown", "reason", shutdownReason)

	// Kill all background shell processes first to prevent orphaned processes
	// This is especially important for hot reload (air) scenarios
	logging.Info("Killing all background processes")
	shell.GetBackgroundManager().KillAllRunning()

	// Stop the process monitor goroutine
	logging.Info("Stopping process monitor")
	shell.GetProcessMonitor().Stop()

	// Stop servers with timeout
	// Must be longer than Server.Stop()'s internal timeout to allow graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	logging.Info("Stopping gRPC server")
	if err := grpcSrv.Stop(shutdownCtx); err != nil {
		logging.Error("Error stopping gRPC server", "error", err)
	} else {
		logging.Info("gRPC server stopped successfully")
	}

	daemonStarter.Shutdown(shutdownCtx)

	logging.Info("Stopping API server")
	if err := apiServer.Stop(shutdownCtx); err != nil {
		logging.Error("Error stopping API server", "error", err)
	} else {
		logging.Info("API server stopped successfully")
	}

	// Stop integration server (this includes Temporal and workers)
	logging.Info("Stopping integration server (Temporal + Workers)")
	if err := server.Stop(); err != nil {
		return fmt.Errorf("failed to stop integration server: %w", err)
	}

	// Shutdown analytics (flush pending events)
	logging.Info("[Statsig] Shutting down analytics")
	analytics.Shutdown()

	// Flush pending Sentry events
	logging.Info("[Sentry] Flushing pending events")
	telemetry.Flush(5)

	logging.Info("Server stopped successfully")
	// Force flush of any buffered logs
	os.Stdout.Sync() //nolint:errcheck
	os.Stderr.Sync() //nolint:errcheck

	return nil
}

// resolveDaemonUserID determines the user ID for the in-process daemon.
func resolveDaemonUserID(repo db.Repository) (string, error) {
	_ = repo // kept for future use; current resolution uses env + auth file

	if explicit := os.Getenv("RELIANT_DAEMON_USER_ID"); explicit != "" {
		return explicit, nil
	}

	// Keep daemon and request routing aligned with dev-mode auth bypass semantics.
	if config.IsDevelopmentEnvironment() {
		return grpcinterceptors.DevUser.Sub, nil
	}

	userID, err := auth.ReadUserIDFromAuthFile()
	if err != nil {
		logging.Warn("Failed reading auth session for daemon identity; starting without in-process daemon", "error", err)
		return "", nil
	}
	if userID == "" {
		logging.Info("No signed-in user found for daemon identity; starting without in-process daemon")
		return "", nil
	}

	return userID, nil
}

// initializeSentry sets up Sentry error reporting.
// Note: Sentry is initialized for backend error tracking regardless of per-user settings.
// Per-user privacy settings control whether user context is attached to errors.
func initializeSentry() {
	reporter, err := telemetry.NewSentryReporter(telemetry.SentryConfig{
		DSN:              os.Getenv("SENTRY_DSN"),
		Enabled:          os.Getenv("SENTRY_ENABLED") != "false",
		TracesSampleRate: parseFloatEnv(os.Getenv("SENTRY_TRACES_SAMPLE_RATE"), 0),
	})
	if err != nil {
		logging.Warn("[Sentry] Failed to initialize (continuing without crash reporting)", "error", err)
		telemetry.SetReporter(telemetry.NewNoopReporter())
		return
	}

	if reporter == nil {
		logging.Info("[Sentry] Disabled via SENTRY_ENABLED=false")
		telemetry.SetReporter(telemetry.NewNoopReporter())
		return
	}

	telemetry.SetReporter(reporter)
	logging.Info("[Sentry] Crash reporting initialized")
}

// parseFloatEnv parses a string as float64, returning defaultVal on failure or empty input.
func parseFloatEnv(s string, defaultVal float64) float64 {
	if s == "" {
		return defaultVal
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return defaultVal
	}
	return v
}
