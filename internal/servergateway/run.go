// Copyright (c) 2025 Reliant Labs

// Package servergateway implements the daemon-gateway server bootstrap logic.
//
// It hosts ToolsDaemonService (bidi gRPC streams to tools-daemon processes) and
// a NATS bridge that forwards tool execution requests from api-server replicas
// and Temporal workers to the correct daemon stream.
//
// It also hosts a frontend-facing gRPC server that exposes FileSystemService,
// BackgroundService, TerminalService, and DaemonService as proxy services.
// Browsers call these endpoints; the proxy services route requests to the
// appropriate daemon via NATS.
package servergateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/cors"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/certs"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/envutil"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/patauth"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Options holds all configurable values for the daemon-gateway server.
type Options struct {
	// ToolsDaemonPort is the port for the daemon bidi-streaming gRPC server.
	ToolsDaemonPort int
	// FrontendPort is the port for the frontend-facing proxy gRPC server.
	FrontendPort int
	// HealthPort is the port for the health/readiness HTTP endpoint.
	HealthPort int
	// BindAddress is the network interface to bind to (e.g. "0.0.0.0").
	BindAddress string

	// JWTPublicKey is the PEM-encoded public key for frontend JWT auth.
	JWTPublicKey string
	// CORSAllowedOrigins is a comma-separated list of allowed origins.
	CORSAllowedOrigins string

	// DatabaseDriver is "sqlite" or "postgres".
	DatabaseDriver string
	// DatabaseURL is the Postgres connection string (required when driver=postgres).
	DatabaseURL string
	// DataDir is the directory for local data (logs, certs, sqlite DB).
	DataDir string

	// NATSURL is the NATS server connection URL.
	NATSURL string

	// TLSCertFile is the path to the TLS certificate file.
	TLSCertFile string
	// TLSKeyFile is the path to the TLS private key file.
	TLSKeyFile string
	// DisableTLS disables TLS entirely.
	DisableTLS bool
}

// Run bootstraps and runs the daemon-gateway server. It blocks until a
// shutdown signal is received or the context is cancelled, then performs
// graceful shutdown.
func Run(ctx context.Context, opts Options) error {
	// -------------------------------------------------------------------------
	// 1. Validate required options
	// -------------------------------------------------------------------------
	if opts.NATSURL == "" {
		return fmt.Errorf("daemon-gateway: NATS_URL is required")
	}
	if opts.DatabaseDriver == "postgres" && opts.DatabaseURL == "" {
		return fmt.Errorf("daemon-gateway: DATABASE_URL is required when DATABASE_DRIVER=postgres")
	}

	corsAllowedOrigins := strings.Split(opts.CORSAllowedOrigins, ",")

	// -------------------------------------------------------------------------
	// 2. Initialize subsystems
	// -------------------------------------------------------------------------
	startTime := time.Now()

	logLevel := logging.GetLogLevel()
	logging.SetupWithRotation(logLevel, false, &logging.RotationConfig{
		Filename:   filepath.Join(opts.DataDir, "logs", "daemon-gateway.log"),
		MaxSizeMB:  50,
		MaxBackups: 3,
		MaxAgeDays: 30,
		Compress:   true,
	})
	defer logging.Close() //nolint:errcheck

	// Initialize observability (Prometheus metrics + OTel tracing)
	obsCfg := observability.ConfigFromEnv("reliant-gateway")
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

	logging.Info("Starting Reliant daemon-gateway",
		"daemon_port", opts.ToolsDaemonPort,
		"frontend_port", opts.FrontendPort,
		"health_port", opts.HealthPort,
		"db_driver", opts.DatabaseDriver,
		"nats_url", opts.NATSURL,
	)

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
	defer func() {
		logging.Info("Closing database")
		if err := repo.Close(); err != nil {
			logging.Error("Error closing database", "error", err)
		}
	}()
	logging.Info("Database initialized", "driver", opts.DatabaseDriver)

	// PAT validator for daemon authentication
	patValidator := patauth.NewDBPATValidator(repo)
	logging.Info("PAT-based daemon authentication initialized")

	// NATS connection
	nc, err := natsutil.Connect(opts.NATSURL)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}
	defer nc.Close()
	logging.Info("Connected to NATS", "url", opts.NATSURL)

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
		logging.Info("TLS disabled via DISABLE_TLS=true")
	}

	// -------------------------------------------------------------------------
	// 3. Daemon services
	// -------------------------------------------------------------------------

	// ToolsDaemonService holds bidi gRPC streams to daemon processes
	toolsDaemonService := services.NewToolsDaemonService(repo)
	defer toolsDaemonService.Close()

	// RemoteExecutor is used by DaemonServer to wire the local daemon router
	remoteExecutor := toolexec.NewRemoteExecutor(nil)

	// NATS tool bridge: subscribes to NATS subjects and forwards to local daemon streams
	toolBridge := toolexec.NewNATSToolBridge(nc, toolsDaemonService)
	if err := toolBridge.Start(); err != nil {
		return fmt.Errorf("failed to start NATS tool bridge: %w", err)
	}
	defer func() { _ = toolBridge.Close() }()
	logging.Info("NATS tool bridge started — forwarding tool requests to daemon streams")

	// Daemon gRPC server (bidi streaming endpoint for tools-daemon connections)
	daemonSrv := grpcserver.NewDaemonServer(&grpcserver.DaemonConfig{
		Port:               opts.ToolsDaemonPort,
		BindAddress:        opts.BindAddress,
		ToolsDaemonService: toolsDaemonService,
		ToolExecutor:       remoteExecutor,
		PATValidator:       patValidator,
		TLSCertFile:        tlsCertFile,
		TLSKeyFile:         tlsKeyFile,
	})
	if err := daemonSrv.Start(); err != nil {
		return fmt.Errorf("failed to start daemon gRPC server: %w", err)
	}
	logging.Info("Daemon gRPC server started", "port", opts.ToolsDaemonPort)

	// -------------------------------------------------------------------------
	// 3b. Frontend-facing proxy gRPC server
	// -------------------------------------------------------------------------

	// NATS-based daemon router for proxy services
	daemonRouter := toolexec.NewNATSDaemonRouter(nc)

	// Proxy services route browser requests through NATS to the correct daemon
	fsProxy := services.NewFileSystemProxyService(daemonRouter, repo)
	bgProxy := services.NewBackgroundProxyService(daemonRouter)
	termProxy := services.NewTerminalProxyService(daemonRouter)
	daemonProxy := services.NewDaemonProxyService(daemonRouter)

	// Build Connect handler options (interceptors)
	var handlerOpts []connect.HandlerOption
	handlerOpts = append(handlerOpts,
		connect.WithInterceptors(interceptors.NewObservabilityInterceptor()),
		connect.WithInterceptors(interceptors.NewRecoveryInterceptor()),
		connect.WithInterceptors(interceptors.NewErrorReporterInterceptor()),
		connect.WithInterceptors(interceptors.NewTimeoutInterceptor().Interceptor()),
	)
	authInterceptor, err := interceptors.NewAuthInterceptor(opts.JWTPublicKey, nil)
	if err != nil {
		if envutil.GetEnv("RELIANT_ENV", "") != "dev" {
			return fmt.Errorf("frontend auth interceptor required — set JWT_PUBLIC_KEY or RELIANT_ENV=dev: %w", err)
		}
		logging.Warn("Frontend auth interceptor disabled (dev mode)", "error", err)
	}
	if authInterceptor != nil {
		handlerOpts = append(handlerOpts, connect.WithInterceptors(authInterceptor))
	}

	// Register ConnectRPC service handlers
	frontendMux := http.NewServeMux()
	fsPath, fsHandler := reliantv1connect.NewFileSystemServiceHandler(fsProxy, handlerOpts...)
	bgPath, bgHandler := reliantv1connect.NewBackgroundServiceHandler(bgProxy, handlerOpts...)
	termPath, termHandler := reliantv1connect.NewTerminalServiceHandler(termProxy, handlerOpts...)
	daemonPath, daemonHandler := reliantv1connect.NewDaemonServiceHandler(daemonProxy, handlerOpts...)
	frontendMux.Handle(fsPath, fsHandler)
	frontendMux.Handle(bgPath, bgHandler)
	frontendMux.Handle(termPath, termHandler)
	frontendMux.Handle(daemonPath, daemonHandler)

	// WebSocket terminal handler (browser bidi terminal I/O)
	var jwtValidator *auth.JWTValidator
	if opts.JWTPublicKey != "" {
		jwtValidator, _ = auth.NewJWTValidator(opts.JWTPublicKey)
	}
	frontendMux.HandleFunc("/api/v2/terminal/ws", services.TerminalWSHandler(daemonRouter, jwtValidator))

	// CORS middleware
	allowCreds := true
	for _, o := range corsAllowedOrigins {
		if o == "*" {
			allowCreds = false
			break
		}
	}
	corsHandler := cors.Handler(cors.Options{
		AllowedOrigins:   corsAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: allowCreds,
		MaxAge:           86400,
	})

	// Build handler with h2c or TLS
	//
	// CORS and security-headers middleware must wrap frontendMux *inside*
	// h2c.NewHandler so they apply to HTTP/2 frames on hijacked connections.
	var frontendHandler http.Handler
	innerHandler := corsHandler(gatewaySecurityHeaders(frontendMux))
	if tlsCertFile != "" && tlsKeyFile != "" {
		frontendHandler = innerHandler
	} else {
		frontendHandler = h2c.NewHandler(innerHandler, &http2.Server{})
	}

	frontendAddr := fmt.Sprintf("%s:%d", opts.BindAddress, opts.FrontendPort)
	frontendSrv := &http.Server{
		Addr:              frontendAddr,
		Handler:           frontendHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if tlsCertFile != "" && tlsKeyFile != "" {
			logging.Info("Starting frontend gRPC server with TLS", "address", frontendAddr)
			if err := frontendSrv.ListenAndServeTLS(tlsCertFile, tlsKeyFile); err != nil && err != http.ErrServerClosed {
				logging.Error("Frontend server failed", "error", err)
			}
		} else {
			logging.Info("Starting frontend gRPC server (h2c)", "address", frontendAddr)
			if err := frontendSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logging.Error("Frontend server failed", "error", err)
			}
		}
	}()
	logging.Info("Frontend gRPC server started", "port", opts.FrontendPort)

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
	healthMux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var failures []string

		if err := repo.Ping(r.Context()); err != nil {
			failures = append(failures, "db: "+err.Error())
		}
		if !nc.IsConnected() {
			failures = append(failures, "nats: disconnected")
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

	// -------------------------------------------------------------------------
	// Startup complete
	// -------------------------------------------------------------------------
	logging.Info("Reliant daemon-gateway ready",
		"startup_duration", time.Since(startTime),
		"daemon_port", opts.ToolsDaemonPort,
		"frontend_port", opts.FrontendPort,
		"health_port", opts.HealthPort,
	)

	// -------------------------------------------------------------------------
	// 5. Graceful shutdown
	// -------------------------------------------------------------------------
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

	// Stop health endpoint first
	logging.Info("Stopping health endpoint")
	healthShutdownCtx, healthCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer healthCancel()
	if err := healthServer.Shutdown(healthShutdownCtx); err != nil {
		logging.Error("Error stopping health endpoint", "error", err)
	}

	// Stop frontend server
	logging.Info("Stopping frontend gRPC server")
	frontendShutdownCtx, frontendCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer frontendCancel()
	if err := frontendSrv.Shutdown(frontendShutdownCtx); err != nil {
		logging.Error("Error stopping frontend gRPC server", "error", err)
	}

	// Stop daemon server (drains connections)
	logging.Info("Stopping daemon gRPC server")
	if err := daemonSrv.Stop(shutdownCtx); err != nil {
		logging.Error("Error stopping daemon gRPC server", "error", err)
	}

	// Note: database is closed via defer above

	logging.Info("Daemon-gateway shut down gracefully")
	return nil
}

// gatewaySecurityHeaders adds security-related HTTP headers to responses.
func gatewaySecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}