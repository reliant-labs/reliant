// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"reflect"

	"connectrpc.com/connect"
	"github.com/go-chi/cors"
	"go.temporal.io/sdk/client"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/pat"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// Server is the Connect/gRPC server
type Server struct {
	server      *http.Server
	mux         *http.ServeMux
	tlsCertFile string
	tlsKeyFile  string

	toolExecutor *toolexec.RemoteExecutor

	// ToolsDaemonService for remote tool execution
	toolsDaemonService *services.ToolsDaemonService

	router toolexec.DaemonRouter
}

// Config holds gRPC server configuration
type Config struct {
	Port               int
	BindAddress        string              // Bind address (default: "127.0.0.1", use "0.0.0.0" for containers)
	JWTPublicKey       string              // RSA public key for JWT validation
	CORSAllowedOrigins []string            // Allowed CORS origins; defaults to ["*"] if empty
	Database           db.Repository       // Database repository
	ToolsFactory       *tools.ToolsFactory // Tools factory for catalog service
	TemporalClient     client.Client       // Temporal client for workflow operations
	MCPManager         *mcp.Manager        // MCP manager for server management

	StreamingHub    streaming.StreamingHub             // Streaming hub for ephemeral events
	UserUpdateHub   streaming.UpdateHub[db.UserUpdate] // Update hub for user-level events
	ChatUpdateHub   streaming.UpdateHub[db.ChatUpdate] // Update hub for chat-level events
	PauseService    *workflow.PauseService             // Pause service for unified pause/resume operations
	SharedTaskQueue string                             // Shared workflow task queue name

	ToolExecutor       *toolexec.RemoteExecutor     // Optional remote tool executor to bind to daemon service
	ToolsDaemonService *services.ToolsDaemonService // Optional pre-created daemon service to share across startup wiring
	DaemonRouter       toolexec.DaemonRouter        // Optional pre-created daemon router; built from ToolsDaemonService if nil

	BackgroundProvider services.BackgroundProcessProvider // Provider for background process state

	NATSChecker func() bool // Optional: returns true if NATS is connected; nil means NATS not in use
	LocalMode   bool        // true in monolith mode, false in cloud/distributed mode

	// TLS configuration for HTTP/2 support
	TLSCertFile string // Path to TLS certificate file
	TLSKeyFile  string // Path to TLS private key file
}

// NewServer creates a new Connect/gRPC server.
// Returns an error if the auth interceptor fails to initialize in cloud mode (LocalMode=false).
func NewServer(cfg *Config) (*Server, error) {
	mux := http.NewServeMux()
	database := cfg.Database

	// Create auth interceptor
	// Public methods (no auth required)
	publicMethods := []string{
		"/reliant.v1.SystemService/Health",
		"/reliant.v1.SystemService/Ready",
		"/reliant.v1.SystemService/Info",
		"/reliant.v1.SystemService/Version",
		// DevAuth methods for browser dev mode - allows auth to work before JWT is available
		"/reliant.v1.SystemService/DevAuthLoad",
		"/reliant.v1.SystemService/DevAuthSave",
		"/reliant.v1.SystemService/DevAuthClear",
	}

	authInterceptor, err := interceptors.NewAuthInterceptor(cfg.JWTPublicKey, publicMethods)
	if err != nil {
		if !cfg.LocalMode {
			return nil, fmt.Errorf("auth interceptor required in cloud mode: %w", err)
		}
		logging.Warn("Auth interceptor disabled in local mode", "error", err)
	}

	// Order matters: recovery (outermost) -> error reporter -> timeout -> auth (innermost).
	opts := newHandlerOptions(interceptors.NewTimeoutInterceptor().Interceptor(), authInterceptor)

	// Tools daemon service for remote tool execution (initialized early
	// because other services depend on it for config loading)
	toolsDaemonService := cfg.ToolsDaemonService
	if toolsDaemonService == nil {
		if cfg.LocalMode {
			toolsDaemonService = services.NewToolsDaemonService(database)
		} else {
			toolsDaemonService = services.NewToolsDaemonServiceWithoutMonitor(database)
		}
	}

	// Build a DaemonRouter for services that need transport-agnostic daemon access.
	router := cfg.DaemonRouter
	if router == nil {
		router = toolexec.NewLocalDaemonRouter(toolsDaemonService)
	}

	// Create services
	systemService := services.NewSystemService(database, cfg.TemporalClient, cfg.NATSChecker, cfg.StreamingHub, cfg.LocalMode)
	planService := services.NewPlanService(database)
	taskService := services.NewTaskService(database)
	catalogService := services.NewCatalogService(cfg.ToolsFactory)
	projectService := services.NewProjectService(database, router)
	worktreeService := services.NewWorktreeService(database, cfg.TemporalClient, router)
	approvalService := services.NewApprovalService(database, cfg.TemporalClient)
	yieldService := services.NewYieldService(database, cfg.PauseService)
	chatService := services.NewChatService(database, cfg.TemporalClient, cfg.PauseService, cfg.SharedTaskQueue)
	messageService := services.NewMessageService(database)
	settingsService := services.NewSettingsService(database, router)
	mcpService := services.NewMCPService(database, router)
	workflowService := services.NewWorkflowService(database, router)
	scenarioService := services.NewScenarioService(database, router)
	packageCommandsService := services.NewPackageCommandsService(database)

	streamingService := services.NewStreamingService(database, cfg.StreamingHub, cfg.UserUpdateHub, cfg.ChatUpdateHub)

	attachmentService := services.NewAttachmentService(database)
	presetService := services.NewPresetService(database)

	patService := pat.NewService(database)
	daemonRegistryService := services.NewDaemonRegistryService(database, router, patService)
	daemonProxyService := services.NewDaemonProxyService(router)
	toolCallService := services.NewToolCallService(database, cfg.TemporalClient, router)

	// Register services with Connect handlers
	// Connect generates HTTP handlers that work with standard net/http
	systemPath, systemHandler := reliantv1connect.NewSystemServiceHandler(systemService, opts...)
	planPath, planHandler := reliantv1connect.NewPlanServiceHandler(planService, opts...)
	taskPath, taskHandler := reliantv1connect.NewTaskServiceHandler(taskService, opts...)
	catalogPath, catalogHandler := reliantv1connect.NewCatalogServiceHandler(catalogService, opts...)
	projectPath, projectHandler := reliantv1connect.NewProjectServiceHandler(projectService, opts...)
	worktreePath, worktreeHandler := reliantv1connect.NewWorktreeServiceHandler(worktreeService, opts...)
	approvalPath, approvalHandler := reliantv1connect.NewApprovalServiceHandler(approvalService, opts...)
	yieldPath, yieldHandler := reliantv1connect.NewYieldServiceHandler(yieldService, opts...)
	chatPath, chatHandler := reliantv1connect.NewChatServiceHandler(chatService, opts...)
	messagePath, messageHandler := reliantv1connect.NewMessageServiceHandler(messageService, opts...)
	settingsPath, settingsHandler := reliantv1connect.NewSettingsServiceHandler(settingsService, opts...)
	mcpPath, mcpHandler := reliantv1connect.NewMCPServiceHandler(mcpService, opts...)
	workflowPath, workflowHandler := reliantv1connect.NewWorkflowServiceHandler(workflowService, opts...)
	scenarioPath, scenarioHandler := reliantv1connect.NewScenarioServiceHandler(scenarioService, opts...)
	packageCommandsPath, packageCommandsHandler := reliantv1connect.NewPackageCommandsServiceHandler(packageCommandsService, opts...)
	toolCallPath, toolCallHandler := reliantv1connect.NewToolCallServiceHandler(toolCallService, opts...)

	streamingPath, streamingHandler := reliantv1connect.NewStreamingServiceHandler(streamingService, opts...)

	attachmentPath, attachmentHandler := reliantv1connect.NewAttachmentServiceHandler(attachmentService, opts...)
	presetPath, presetHandler := reliantv1connect.NewPresetServiceHandler(presetService, opts...)

	// FileSystem, Background, and Terminal services: in local/monolith mode with a
	// daemon router, use proxy services that forward requests through the daemon so
	// the browser can connect through the monolith's gRPC port for everything.
	// Otherwise fall back to the DB-backed / provider-backed implementations.
	var filesystemPath string
	var filesystemHandler http.Handler
	var backgroundPath string
	var backgroundHandler http.Handler
	var terminalPath string
	var terminalHandler http.Handler

	if cfg.LocalMode && router != nil {
		logging.Info("Registering proxy services on monolith gRPC server (FileSystem, Background, Terminal)")

		fsProxy := services.NewFileSystemProxyService(router, database)
		filesystemPath, filesystemHandler = reliantv1connect.NewFileSystemServiceHandler(fsProxy, opts...)

		bgProxy := services.NewBackgroundProxyService(router)
		backgroundPath, backgroundHandler = reliantv1connect.NewBackgroundServiceHandler(bgProxy, opts...)

		termProxy := services.NewTerminalProxyService(router)
		terminalPath, terminalHandler = reliantv1connect.NewTerminalServiceHandler(termProxy, opts...)

		// WebSocket terminal handler (browser bidi terminal I/O)
		var jwtValidator *auth.JWTValidator
		if cfg.JWTPublicKey != "" {
			jwtValidator, _ = auth.NewJWTValidator(cfg.JWTPublicKey)
		}
		mux.HandleFunc("/api/v2/terminal/ws", services.TerminalWSHandler(router, jwtValidator))
	} else {
		// Non-local mode: use DB-backed FileSystem and provider-backed Background
		filesystemService := services.NewFileSystemService(database)
		filesystemPath, filesystemHandler = reliantv1connect.NewFileSystemServiceHandler(filesystemService, opts...)

		if cfg.BackgroundProvider != nil {
			backgroundService := services.NewBackgroundService(cfg.BackgroundProvider)
			backgroundPath, backgroundHandler = reliantv1connect.NewBackgroundServiceHandler(backgroundService, opts...)
		}
	}

	// Daemon registry APIs remain on the app gRPC server.
	// The ToolsDaemonService streaming endpoint itself is hosted on the dedicated
	// daemon listener (see daemon_server.go).
	daemonRegistryPath, daemonRegistryHandler := reliantv1connect.NewDaemonRegistryServiceHandler(daemonRegistryService, opts...)
	daemonPath, daemonHandler := reliantv1connect.NewDaemonServiceHandler(daemonProxyService, opts...)

	mux.Handle(systemPath, systemHandler)
	mux.Handle(planPath, planHandler)
	mux.Handle(taskPath, taskHandler)
	mux.Handle(catalogPath, catalogHandler)
	mux.Handle(projectPath, projectHandler)
	mux.Handle(worktreePath, worktreeHandler)
	mux.Handle(approvalPath, approvalHandler)
	mux.Handle(yieldPath, yieldHandler)
	mux.Handle(chatPath, chatHandler)
	mux.Handle(messagePath, messageHandler)
	mux.Handle(settingsPath, settingsHandler)
	mux.Handle(mcpPath, mcpHandler)
	mux.Handle(workflowPath, workflowHandler)
	mux.Handle(scenarioPath, scenarioHandler)
	if filesystemHandler != nil {
		mux.Handle(filesystemPath, filesystemHandler)
	}
	mux.Handle(packageCommandsPath, packageCommandsHandler)
	mux.Handle(toolCallPath, toolCallHandler)

	mux.Handle(streamingPath, streamingHandler)

	mux.Handle(attachmentPath, attachmentHandler)
	mux.Handle(presetPath, presetHandler)

	mux.Handle(daemonRegistryPath, daemonRegistryHandler)
	mux.Handle(daemonPath, daemonHandler)

	if backgroundHandler != nil {
		mux.Handle(backgroundPath, backgroundHandler)
	}
	if terminalHandler != nil {
		mux.Handle(terminalPath, terminalHandler)
	}

	// Create CORS handler that wraps the entire mux
	// MaxAge of 24 hours to cache preflight responses and reduce OPTIONS requests
	corsOrigins := cfg.CORSAllowedOrigins
	if len(corsOrigins) == 0 {
		corsOrigins = []string{"*"}
	}
	allowCreds := true
	for _, o := range corsOrigins {
		if o == "*" {
			allowCreds = false
			break
		}
	}
	corsHandler := cors.Handler(cors.Options{
		AllowedOrigins:   corsOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms", "X-CSRF-Token", "traceparent", "tracestate", "sentry-trace", "baggage"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: allowCreds,
		MaxAge:           86400,
	})

	// Create HTTP server
	// If TLS certs are provided, use HTTPS (enables native HTTP/2)
	// Otherwise fall back to h2c (HTTP/2 without TLS) for backward compatibility
	bindAddr := cfg.BindAddress
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", bindAddr, cfg.Port)

	// IMPORTANT: CORS and security-headers middleware must wrap the mux
	// *inside* h2c.NewHandler so they apply to HTTP/2 frames on hijacked
	// connections. h2c.NewHandler's prior-knowledge path hijacks the TCP
	// connection and calls ServeConn with its inner handler, completely
	// bypassing any middleware that wraps h2c from the outside.
	var handler http.Handler
	innerHandler := corsHandler(securityHeaders(mux))
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		// TLS mode: HTTP/2 is automatic with TLS
		handler = innerHandler
		logging.Info("gRPC server configured for HTTPS/HTTP2", "cert", cfg.TLSCertFile)
	} else {
		// No TLS: use h2c for HTTP/2 over cleartext
		handler = h2c.NewHandler(innerHandler, &http2.Server{})
		logging.Info("gRPC server configured for h2c (HTTP/2 cleartext)")
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,

		// IMPORTANT: ToolsDaemonService uses long-lived bidi streaming.
		// ReadTimeout applies to the ENTIRE request body read duration; if non-zero,
		// it will terminate healthy daemon streams at a fixed interval.
		// Keep ReadHeaderTimeout for slowloris protection while allowing streaming bodies.
		ReadTimeout:       0,
		ReadHeaderTimeout: transport.ServerReadHeaderTimeout,
		WriteTimeout:      0,                           // Streaming responses should not be write-time-limited globally.
		IdleTimeout:       transport.ServerIdleTimeout, // Keep-alive idle timeout between requests.
	}

	if cfg.ToolExecutor != nil {
		cfg.ToolExecutor.SetDaemonRouter(router)
	}

	return &Server{
		server:             srv,
		mux:                mux,
		tlsCertFile:        cfg.TLSCertFile,
		tlsKeyFile:         cfg.TLSKeyFile,
		toolExecutor:       cfg.ToolExecutor,
		toolsDaemonService: toolsDaemonService,
		router:             router,
	}, nil
}

// ToolsDaemonService returns the tools daemon service for remote execution.
func (s *Server) ToolsDaemonService() *services.ToolsDaemonService {
	return s.toolsDaemonService
}

// DaemonRouter returns the daemon router used by this server.
func (s *Server) DaemonRouter() toolexec.DaemonRouter {
	return s.router
}

// Start starts the gRPC server
func (s *Server) Start() error {
	if s.tlsCertFile != "" && s.tlsKeyFile != "" {
		// Load TLS certificate
		cert, err := tls.LoadX509KeyPair(s.tlsCertFile, s.tlsKeyFile)
		if err != nil {
			return fmt.Errorf("failed to load TLS cert: %w", err)
		}

		// Explicitly configure TLS for HTTP/2
		// Go's http.Server only auto-enables HTTP/2 under certain conditions.
		// We explicitly configure it to ensure HTTP/2 is always used.
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
			NextProtos:   []string{"h2", "http/1.1"}, // Prefer HTTP/2
			MinVersion:   tls.VersionTLS12,
		}
		s.server.TLSConfig = tlsConfig

		// Configure HTTP/2 server settings
		http2Server := &http2.Server{
			MaxConcurrentStreams: 1000, // Allow many concurrent streams per connection
			IdleTimeout:          transport.ServerIdleTimeout,
		}
		if err := http2.ConfigureServer(s.server, http2Server); err != nil {
			return fmt.Errorf("failed to configure HTTP/2: %w", err)
		}

		logging.Info("Starting Connect/gRPC server with TLS (HTTPS/HTTP2)",
			"address", s.server.Addr,
			"maxStreams", http2Server.MaxConcurrentStreams,
		)
		go func() {
			// Use ListenAndServeTLS with empty strings since TLSConfig is already set
			if err := s.server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				logging.Error("gRPC server failed", "error", err)
			}
		}()
	} else {
		logging.Info("Starting Connect/gRPC server (h2c)", "address", s.server.Addr)
		go func() {
			if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logging.Error("gRPC server failed", "error", err)
			}
		}()
	}

	return nil
}

// IsTLS returns whether the server is configured to use TLS
func (s *Server) IsTLS() bool {
	return s.tlsCertFile != "" && s.tlsKeyFile != ""
}

// Stop gracefully stops the gRPC server
func (s *Server) Stop(ctx context.Context) error {
	logging.Info("Stopping Connect/gRPC server")
	return s.server.Shutdown(ctx)
}

// Mux returns the HTTP mux for mounting additional handlers
func (s *Server) Mux() *http.ServeMux {
	return s.mux
}

// newHandlerOptions builds the standard Connect handler options with the shared
// interceptor ordering: recovery -> error reporter -> timeout -> auth.
func newHandlerOptions(timeoutInterceptor connect.Interceptor, authInterceptors ...connect.Interceptor) []connect.HandlerOption {
	all := newInterceptors(timeoutInterceptor, authInterceptors...)
	opts := make([]connect.HandlerOption, 0, len(all))
	for _, i := range all {
		opts = append(opts, connect.WithInterceptors(i))
	}
	return opts
}

// newInterceptors returns the ordered interceptor slice used by both main and daemon servers.
func newInterceptors(timeoutInterceptor connect.Interceptor, authInterceptors ...connect.Interceptor) []connect.Interceptor {
	out := []connect.Interceptor{
		interceptors.NewObservabilityInterceptor(), // outermost: metrics + tracing
		interceptors.NewRecoveryInterceptor(),
		interceptors.NewErrorReporterInterceptor(),
		timeoutInterceptor,
	}
	for _, ai := range authInterceptors {
		if isNilInterceptor(ai) {
			continue
		}
		out = append(out, ai)
	}
	return out
}

func isNilInterceptor(i connect.Interceptor) bool {
	if i == nil {
		return true
	}
	v := reflect.ValueOf(i)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// securityHeaders adds security-related HTTP headers to responses
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")
		// Enable XSS filter in browsers
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		// Referrer policy
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}