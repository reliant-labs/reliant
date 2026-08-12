// Copyright (c) 2025 Reliant Labs
package serverworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/configadapter"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workersetup"
)

// Options holds all configurable values for the Temporal worker.
type Options struct {
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

	// -----------------------------------------------------------------
	// 2. Logging
	// -----------------------------------------------------------------
	logLevel := logging.GetLogLevel()
	logging.SetupWithRotation(logLevel, false, &logging.RotationConfig{
		Filename:   filepath.Join(opts.DataDir, "logs", "temporal-worker.log"),
		MaxSizeMB:  50,
		MaxBackups: 3,
		MaxAgeDays: 30,
		Compress:   true,
	})
	defer logging.Close() //nolint:errcheck

	// Initialize observability (Prometheus metrics + OTel tracing)
	obsCfg := observability.ConfigFromEnv("reliant-worker")
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
	local.SetLocalConfig(nil)

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
		Driver:  dbDriver,
		DataDir: opts.DataDir,
		URL:     opts.DatabaseURL,
		Migrate: db.MigrateWait,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer func() { _ = repo.Close() }()

	// API key provider (allows LLM drivers to resolve per-user keys from DB)
	drivers.InitializeAPIKeyProvider(repo)

	// -----------------------------------------------------------------
	// 5. Temporal client
	// -----------------------------------------------------------------
	temporalClient, err := temporal.NewExternalClient(ctx, temporal.ExternalClientConfig{
		Host:      opts.TemporalHost,
		Port:      opts.TemporalPort,
		Namespace: opts.TemporalNamespace,
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
	toolsFactory := tools.NewToolsFactory(&tools.ToolsOptions{
		Repo: repo,
	})
	remoteExecutor := toolexec.NewRemoteExecutor(nil)

	// Stored config provider
	storedConfigProvider := config.NewStoredConfigProvider(configadapter.NewRepoConfigStore(repo))

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
	telemetry.SetReporter(telemetry.NewReporterFromEnv(
		config.IsDevelopmentEnvironment() || config.IsTestEnvironment()))

	// -----------------------------------------------------------------
	// 10. Tool execution routing via NATS
	// -----------------------------------------------------------------
	router := toolexec.NewNATSDaemonRouter(nc, toolexec.WithDatabase(repo))
	remoteExecutor.SetDaemonRouter(router)
	natsChecker := nc.IsConnected
	logging.Info("Tool execution routing via NATS")

	// Wire server-side tool execution so ToolRunsOnServer / ToolRunsAnywhere
	// tools execute in the worker process without a daemon round-trip.
	serverExecutor := toolexec.NewLocalToolExecutor(toolsFactory)
	serverExecutor.SetMCPContextBinder(toolexec.NewDaemonMCPContextBinder(router))
	remoteExecutor.SetServerExecutor(serverExecutor)
	// Per-request daemon clients via NATS for server-side tools that still
	// need daemon filesystem/exec access.
	remoteExecutor.SetDaemonClientFactory(func(userID string) daemon.Client {
		return daemon.NewRemoteClient(router, userID)
	})

	// -----------------------------------------------------------------
	// 11. Start the worker
	// -----------------------------------------------------------------
	handle, _, err := workersetup.StartWorker(&workersetup.Config{
		TemporalClient: temporalClient,
		Database:       repo,
		StreamingHub:   streamingHub,
		ToolsFactory:   toolsFactory,
		ToolExecutor:   remoteExecutor,
		DaemonRouter:   remoteExecutor.DaemonRouter(),
		MCPBinder:      toolexec.NewDaemonMCPContextBinder(router),
		ConfigProvider: storedConfigProvider,
	})
	if err != nil {
		return fmt.Errorf("failed to start worker: %w", err)
	}

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
	healthMux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var failures []string

		if err := repo.Ping(r.Context()); err != nil {
			failures = append(failures, "db: "+err.Error())
		}
		if _, err := temporalClient.CheckHealth(r.Context(), &client.CheckHealthRequest{}); err != nil {
			failures = append(failures, "temporal: "+err.Error())
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
	logging.Info("Shutting down temporal-worker")
	handle.Worker.Stop()

	select {
	case <-handle.Done:
		logging.Info("Worker stopped successfully")
	case <-time.After(15 * time.Second):
		logging.Warn("Worker stop timed out")
	}

	// Shut down health server
	healthCtx, healthCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer healthCancel()
	if err := healthServer.Shutdown(healthCtx); err != nil {
		logging.Error("Health server shutdown error", "error", err)
	}

	analytics.Shutdown()

	// Flush any pending telemetry (Sentry) events before exit.
	telemetry.Flush(5)

	logging.Info("temporal-worker shut down gracefully")
	return nil
}