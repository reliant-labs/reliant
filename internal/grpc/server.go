// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"strings"

	"connectrpc.com/connect"
	"github.com/go-chi/cors"
	"github.com/reliant-labs/forge/pkg/observe"
	"go.opentelemetry.io/otel"
	"go.temporal.io/sdk/client"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcpserver"
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

	// ProjectService is exposed so callers can wire it to out-of-band
	// inputs (e.g. the daemon-events JetStream consumer in serverapi.Run).
	projectService *services.ProjectService

	router toolexec.DaemonRouter
}

// Config holds gRPC server configuration
type Config struct {
	Port                int
	BindAddress         string                             // Bind address (default: "127.0.0.1", use "0.0.0.0" for containers)
	JWTPublicKey        string                             // RSA public key for JWT validation
	JWKSURL             string                             // JWKS endpoint URL for JWT validation (alternative to JWTPublicKey)
	CORSAllowedOrigins  []string                           // Allowed CORS origins; defaults to ["*"] if empty
	AllowedEmailDomains []string                           // If non-empty, only these email domains may access the system
	Database            db.Repository                      // Database repository
	ToolsFactory        *tools.ToolsFactory                // Tools factory for catalog service
	TemporalClient      client.Client                      // Temporal client for workflow operations
	StreamingHub        streaming.StreamingHub             // Streaming hub for ephemeral events
	UserUpdateHub       streaming.UpdateHub[db.UserUpdate] // Update hub for user-level events
	ChatUpdateHub       streaming.UpdateHub[db.ChatUpdate] // Update hub for chat-level events
	PauseService        *workflow.PauseService             // Pause service for unified pause/resume operations
	SharedTaskQueue     string                             // Shared workflow task queue name

	ToolExecutor *toolexec.RemoteExecutor // Optional remote tool executor to bind to daemon service
	DaemonRouter toolexec.DaemonRouter    // Optional pre-created daemon router for transport-agnostic daemon access

	BackgroundProvider services.BackgroundProcessProvider // Provider for background process state

	NATSChecker func() bool // Optional: returns true if NATS is connected; nil means NATS not in use

	// TLS configuration for HTTP/2 support
	TLSCertFile string // Path to TLS certificate file
	TLSKeyFile  string // Path to TLS private key file

	// PublicURL is the externally reachable base URL of this API server, used
	// to tell users where to point a third-party MCP client (see
	// ConnectorService). Optional: when unset, connector responses carry the
	// bare path rather than a guessed hostname, since a wrong URL pasted into
	// ChatGPT fails in a way that is hard to diagnose from a phone.
	PublicURL string

	// OAuthIssuers lists the OAuth authorization servers whose tokens the
	// connector endpoint accepts — typically the deployment's Supabase project
	// URL. When set together with PublicURL, RFC 9728 discovery is advertised
	// so consumer MCP clients can run the OAuth flow. When unset, connectors
	// authenticate with rlnt_conn_ credentials only.
	OAuthIssuers []string
}

// NewServer creates a new Connect/gRPC server.
// Returns an error if the auth interceptor fails to initialize.
func NewServer(cfg *Config) (*Server, error) {
	mux := http.NewServeMux()
	database := cfg.Database

	// Create auth interceptor
	// Public methods (no auth required)
	// Managed-daemon-token RPCs are authenticated by the internal-service
	// interceptor (HS256 token signed with INTERNAL_SERVICE_SECRET), NOT by the
	// user-JWT auth interceptor. They MUST be listed as public for the user-JWT
	// interceptor so it does not also demand a Supabase JWT for them; the
	// InternalServiceInterceptor below enforces the real (operator) auth.
	managedDaemonTokenProcedures := []string{
		"/reliant.v1.DaemonTokenService/MintManagedDaemonToken",
		"/reliant.v1.DaemonTokenService/RevokeManagedDaemonToken",
	}

	publicMethods := []string{
		"/reliant.v1.SystemService/Health",
		"/reliant.v1.SystemService/Ready",
		"/reliant.v1.SystemService/Info",
		"/reliant.v1.SystemService/Version",
		"/reliant.v1.SystemService/StartOAuthSignIn",
		// DevAuth methods for browser dev mode - allows auth to work before JWT is available
		"/reliant.v1.SystemService/DevAuthLoad",
		"/reliant.v1.SystemService/DevAuthSave",
		"/reliant.v1.SystemService/DevAuthClear",
	}
	publicMethods = append(publicMethods, managedDaemonTokenProcedures...)

	authInterceptor, err := interceptors.NewAuthInterceptor(cfg.JWTPublicKey, cfg.JWKSURL, publicMethods)
	if err != nil {
		return nil, fmt.Errorf("auth interceptor setup failed: %w", err)
	}

	// One PAT service backs every rlnt_pat_ token (kind='daemon' for gateway
	// streams, kind='api' for user API auth). Api-kind bearers are
	// prefix-dispatched in the same auth interceptor as JWTs (daemon-kind
	// tokens are rejected there); the service also backs DaemonTokenService
	// (daemon-kind) and TokenService (api-kind user-token management), both
	// registered below.
	patService := pat.NewService(database)
	authInterceptor.SetAPITokenValidator(patService)
	domainWhitelistInterceptor := interceptors.NewDomainWhitelistInterceptor(cfg.AllowedEmailDomains)

	// Internal-service auth for the managed-daemon-token surface. Verifier reads
	// INTERNAL_SERVICE_SECRET from env (fail-closed when unset). This interceptor
	// is a no-op for every procedure not in managedDaemonTokenProcedures.
	internalServiceInterceptor, err := interceptors.NewInternalServiceInterceptor(
		auth.NewInternalServiceVerifierFromEnv(),
		managedDaemonTokenProcedures,
	)
	if err != nil {
		return nil, fmt.Errorf("internal-service interceptor setup failed: %w", err)
	}

	// Order matters: recovery (outermost) -> error reporter -> timeout -> internal-service -> auth -> domain whitelist (innermost).
	// internal-service runs before user-JWT auth so that, for the gated
	// procedures, the operator's identity is established and user-JWT auth then
	// skips them (they are in publicMethods).
	opts := newHandlerOptions(interceptors.NewTimeoutInterceptor().Interceptor(), internalServiceInterceptor, authInterceptor, domainWhitelistInterceptor)

	// Build a DaemonRouter for services that need transport-agnostic daemon access.
	// The api-server itself never accepts daemon bidi streams — daemons connect to
	// the daemon-gateway process (see internal/servergateway) and the api-server
	// reaches them through this router (NATS-backed in production). Anything that
	// looks like a ToolsDaemonService here used to be live in the pre-split
	// monolith days; it isn't anymore.
	router := cfg.DaemonRouter

	// Create services
	systemService := services.NewSystemService(database, cfg.TemporalClient, cfg.NATSChecker, cfg.StreamingHub)
	planService := services.NewPlanService(database)
	taskService := services.NewTaskService(database)
	catalogService := services.NewCatalogService(cfg.ToolsFactory)
	projectService := services.NewProjectService(database, router)
	worktreeService := services.NewWorktreeService(database, cfg.TemporalClient, router)
	repoService := services.NewRepoService(database, router)
	approvalService := services.NewApprovalService(database, cfg.PauseService)
	questionService := services.NewQuestionService(database, cfg.PauseService)
	chatService := services.NewChatService(database, cfg.TemporalClient, cfg.PauseService, cfg.SharedTaskQueue, cfg.StreamingHub, router)
	messageService := services.NewMessageService(database)
	settingsService := services.NewSettingsService(database, router)
	mcpService := services.NewMCPService(database, router)
	workflowService := services.NewWorkflowService(database, router)
	scenarioService := services.NewScenarioService(database, router)
	// PackageCommands is a browser-facing workspace service: on a cloud daemon
	// (router != nil) its filesystem discovery must run on the daemon, so it uses
	// the proxy. Without this it silently returned an empty command list because
	// discovery ran against the api-server's filesystem. See pickPackageCommandsService.
	packageCommandsService := pickPackageCommandsService(router, database)

	streamingService := services.NewStreamingService(database, cfg.StreamingHub, cfg.UserUpdateHub, cfg.ChatUpdateHub)

	attachmentService := services.NewAttachmentService(database)
	presetService := services.NewPresetService(database)

	daemonRegistryService := services.NewDaemonRegistryService(database, router)
	daemonTokenService := services.NewDaemonTokenService(patService)
	// TokenService manages user API tokens (api-kind PATs) — the Connect
	// replacement for the former /api/v1/tokens JSON surface. A thin wrapper
	// over the same pat.Service.
	tokenService := services.NewTokenService(patService)
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
	repoPath, repoHandler := reliantv1connect.NewRepoServiceHandler(repoService, opts...)
	approvalPath, approvalHandler := reliantv1connect.NewApprovalServiceHandler(approvalService, opts...)
	questionPath, questionHandler := reliantv1connect.NewQuestionServiceHandler(questionService, opts...)
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

	// FileSystem, Background, and Terminal services: when a daemon router is
	// available, use proxy services that forward requests through the daemon.
	// Otherwise fall back to the DB-backed / provider-backed implementations.
	var filesystemPath string
	var filesystemHandler http.Handler
	var backgroundPath string
	var backgroundHandler http.Handler
	var terminalPath string
	var terminalHandler http.Handler

	if router != nil {
		// Proxy services route browser requests through the daemon router (NATS in
		// distributed mode) to the connected daemon.
		logging.Info("Registering daemon proxy services (FileSystem, Background, Terminal)")

		fsProxy := services.NewFileSystemProxyService(router, database)
		filesystemPath, filesystemHandler = reliantv1connect.NewFileSystemServiceHandler(fsProxy, opts...)

		bgProxy := services.NewBackgroundProxyService(router)
		backgroundPath, backgroundHandler = reliantv1connect.NewBackgroundServiceHandler(bgProxy, opts...)

		termProxy := services.NewTerminalProxyService(router)
		terminalPath, terminalHandler = reliantv1connect.NewTerminalServiceHandler(termProxy, opts...)

		// WebSocket terminal handler (browser bidi terminal I/O)
		var wsValidator auth.TokenValidator
		switch auth.GetAuthMode() {
		case "apikey":
			if key := os.Getenv("AUTH_API_KEY"); key != "" {
				wsValidator, _ = auth.NewAPIKeyValidator(key)
			}
		default: // supabase
			if cfg.JWTPublicKey != "" {
				wsValidator, _ = auth.NewJWTValidator(cfg.JWTPublicKey)
			} else if cfg.JWKSURL != "" {
				wsValidator, _ = auth.LoadJWKS(context.Background(), cfg.JWKSURL)
			}
		}
		mux.HandleFunc("/api/v2/terminal/ws", services.TerminalWSHandler(router, wsValidator))
	} else {
		// No daemon router available: use DB-backed FileSystem and provider-backed Background.
		// These provide read-only / limited functionality without a connected daemon.
		filesystemService := services.NewFileSystemService(database)
		filesystemPath, filesystemHandler = reliantv1connect.NewFileSystemServiceHandler(filesystemService, opts...)

		if cfg.BackgroundProvider != nil {
			backgroundService := services.NewBackgroundService(cfg.BackgroundProvider)
			backgroundPath, backgroundHandler = reliantv1connect.NewBackgroundServiceHandler(backgroundService, opts...)
		}
	}

	// Daemon services on the app gRPC server (both JWT):
	//   - DaemonRegistryService: browser-driven list/get/resolve/resume
	//   - DaemonTokenService:    browser- or CLI-driven PAT CRUD
	// The ToolsDaemonService streaming endpoint (ConnectDaemon /
	// ConnectGateway) is hosted by the daemon-gateway, not the api-server —
	// see internal/grpc/daemon_server.go, which only the gateway constructs.
	// PAT validation for the CLI happens at stream-connect time over there;
	// no dedicated introspection RPC is needed here.
	daemonRegistryPath, daemonRegistryHandler := reliantv1connect.NewDaemonRegistryServiceHandler(daemonRegistryService, opts...)
	daemonTokenPath, daemonTokenHandler := reliantv1connect.NewDaemonTokenServiceHandler(daemonTokenService, opts...)
	tokenPath, tokenHandler := reliantv1connect.NewTokenServiceHandler(tokenService, opts...)
	daemonPath, daemonHandler := reliantv1connect.NewDaemonServiceHandler(daemonProxyService, opts...)

	// ConnectorService manages grants for third-party MCP clients. It is
	// mounted only when a SQL-backed repository is present, matching the /mcp
	// endpoint it administers — offering grant management without a working
	// endpoint would let a user mint credentials that cannot be redeemed.
	var connectorPath string
	var connectorHandler http.Handler
	if repo, ok := database.(*db.Repo); ok && repo != nil && repo.DB != nil {
		connectorService := services.NewConnectorService(
			connectorgrant.NewSQLStore(repo.DB.SQLDB()), cfg.PublicURL)
		connectorPath, connectorHandler = reliantv1connect.NewConnectorServiceHandler(connectorService, opts...)
	}

	mux.Handle(systemPath, systemHandler)
	mux.Handle(planPath, planHandler)
	mux.Handle(taskPath, taskHandler)
	mux.Handle(catalogPath, catalogHandler)
	mux.Handle(projectPath, projectHandler)
	mux.Handle(worktreePath, worktreeHandler)
	mux.Handle(repoPath, repoHandler)
	mux.Handle(approvalPath, approvalHandler)
	mux.Handle(questionPath, questionHandler)
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
	mux.Handle(daemonTokenPath, daemonTokenHandler)
	mux.Handle(tokenPath, tokenHandler)
	mux.Handle(daemonPath, daemonHandler)

	if connectorHandler != nil {
		mux.Handle(connectorPath, connectorHandler)
	}

	if backgroundHandler != nil {
		mux.Handle(backgroundPath, backgroundHandler)
	}
	if terminalHandler != nil {
		mux.Handle(terminalPath, terminalHandler)
	}

	// The CLI's api-token management and workflow-trigger surfaces are Connect
	// RPCs now (TokenService above, ChatService.CreateChat), not bespoke
	// /api/v1 JSON handlers — the CLI speaks the same authenticated Connect
	// path the web app does.

	// Connector MCP endpoint: third-party MCP clients (ChatGPT, Claude, and
	// their mobile apps) driving a cloud daemon under a connector grant.
	//
	// Authenticated by a rlnt_conn_ credential rather than the user JWT the
	// Connect handlers above use, so it is mounted as a plain HTTP route
	// outside the interceptor chain. Confinement is enforced daemon-side at
	// command dispatch (internal/daemonpolicy); this route resolves the grant
	// and terminates the protocol.
	mountConnectorMCP(mux, database, router, cfg.PublicURL, cfg.OAuthIssuers, connectorTokenValidator(cfg))

	// JSON health endpoint on the gRPC mux so the frontend can discover auth_mode
	// without needing to reach the dedicated health port.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":    "ok",
			"service":   "grpc-server",
			"auth_mode": auth.GetAuthMode(),
		})
	})

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

		// Keep ReadTimeout=0 / WriteTimeout=0 for any streaming RPCs the
		// api-server still hosts (e.g. UserUpdate / ChatUpdate). The bidi
		// daemon endpoints live on the gateway, but our other long-lived
		// streams have the same fixed-deadline-kills-healthy-stream problem.
		ReadTimeout:       0,
		ReadHeaderTimeout: transport.ServerReadHeaderTimeout,
		WriteTimeout:      0,
		IdleTimeout:       transport.ServerIdleTimeout,
	}

	if cfg.ToolExecutor != nil {
		cfg.ToolExecutor.SetDaemonRouter(router)
	}

	return &Server{
		server:         srv,
		mux:            mux,
		tlsCertFile:    cfg.TLSCertFile,
		tlsKeyFile:     cfg.TLSKeyFile,
		toolExecutor:   cfg.ToolExecutor,
		projectService: projectService,
		router:         router,
	}, nil
}

// ProjectService returns the project service so callers can wire it to
// out-of-band inputs (e.g. JetStream consumers in serverapi.Run).
func (s *Server) ProjectService() *services.ProjectService {
	return s.projectService
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
// interceptor chain.
func newHandlerOptions(timeoutInterceptor connect.Interceptor, authInterceptors ...connect.Interceptor) []connect.HandlerOption {
	all := newInterceptors(timeoutInterceptor, authInterceptors...)
	opts := make([]connect.HandlerOption, 0, len(all))
	for _, i := range all {
		opts = append(opts, connect.WithInterceptors(i))
	}
	return opts
}

// newInterceptors returns the ordered interceptor slice used by both main and daemon servers.
// The canonical chain is provided by forge's observe.DefaultMiddlewares:
//
//	Recovery → RequestID → Logging → Tracing → Metrics → Extras
//
// Reliant-specific interceptors (error reporter, timeout, auth) are passed as Extras.
func newInterceptors(timeoutInterceptor connect.Interceptor, authInterceptors ...connect.Interceptor) []connect.Interceptor {
	extras := []connect.Interceptor{
		interceptors.NewSlowRPCWatchdogInterceptor(),
		interceptors.NewErrorReporterInterceptor(),
		timeoutInterceptor,
	}
	for _, ai := range authInterceptors {
		if isNilInterceptor(ai) {
			continue
		}
		extras = append(extras, ai)
	}
	return observe.DefaultMiddlewares(observe.DefaultMiddlewareDeps{
		Tracer: otel.Tracer("reliant.grpc"),
		Extras: extras,
	})
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

// mountConnectorMCP mounts the connector MCP endpoint when its prerequisites
// are present.
//
// Both are genuinely optional at this layer: monolith/desktop deployments run
// without a daemon router (nothing to drive remotely), and some test harnesses
// construct a server without a SQL-backed repository. Rather than fail
// startup, the route is simply not mounted — an unmounted route 404s, which is
// the correct answer to "connect me to a workspace that cannot be reached".
// The reason is logged so a misconfigured deployment is diagnosable.
func mountConnectorMCP(
	mux *http.ServeMux,
	database db.Repository,
	router toolexec.DaemonRouter,
	publicURL string,
	oauthIssuers []string,
	tokenValidator mcpserver.OAuthTokenValidator,
) {
	if router == nil {
		logging.Info("connector MCP endpoint not mounted: no daemon router (expected in monolith/desktop mode)")
		return
	}

	repo, ok := database.(*db.Repo)
	if !ok || repo == nil || repo.DB == nil {
		logging.Info("connector MCP endpoint not mounted: no SQL-backed repository")
		return
	}

	// A connector request is often the first traffic a workspace has seen in
	// hours, so a suspended workspace is woken rather than reported as broken.
	// The wake is triggered and the call returns immediately — see
	// mcpserver.PollingWaker for why it does not wait.
	//
	// The resumer is nil when no control plane is configured (OSS: nothing can
	// schedule a pod), in which case "cannot be started" is reported honestly
	// instead of a retry that could never succeed.
	// Assigned through the interface only when non-nil: a typed nil pointer in
	// an interface is NOT nil, and would turn "no control plane" into a nil
	// dereference on the first suspended workspace.
	var resumer mcpserver.DaemonResumer
	if cp := mcpserver.NewControlPlaneResumer(controlPlaneBaseURL()); cp != nil {
		resumer = cp
	} else {
		logging.Info("connector workspace wake unavailable: no control plane URL configured")
	}
	waker := mcpserver.NewPollingWaker(
		mcpserver.NewAttachmentReadiness(database, resumer), slog.Default())

	oauthCfg := mcpserver.OAuthConfig{
		PublicURL:            publicURL,
		AuthorizationServers: oauthIssuers,
	}

	handler, err := mcpserver.NewHTTPHandler(mcpserver.HTTPDeps{
		Store:  connectorgrant.NewSQLStore(repo.DB.SQLDB()),
		Sender: router,
		Waker:  waker,
		OAuth:  oauthCfg,
		// Reuses the deployment's existing JWT validator, so an OAuth token is
		// trusted on exactly the same basis as a session token.
		TokenValidator: tokenValidator,
		// Bindings answer "which connector does this application act
		// through?", recorded at consent.
		Bindings:       connectorgrant.NewSQLStore(repo.DB.SQLDB()),
		ConsentBaseURL: publicURL,
	})
	if err != nil {
		logging.Error("connector MCP endpoint not mounted", "error", err)
		return
	}

	// Both the bare path and the subtree: MCP clients differ on whether they
	// append a trailing slash, and a 404 on the handshake is opaque to debug
	// from a phone.
	// The handler owns routing for both transports (streamable-HTTP at /mcp,
	// legacy SSE at /sse), so every path it serves is registered here.
	mux.Handle(mcpserver.MountPath, handler)
	mux.Handle(mcpserver.MountPath+"/", handler)
	mux.Handle(mcpserver.SSEMountPath, handler)
	mux.Handle(mcpserver.SSEMountPath+"/", handler)

	// The origin root, so a connector configured with the bare base URL
	// handshakes instead of 404ing (see mcpserver.RootMountPath).
	//
	// Safe because this mux is the API server's own origin: it is
	// hostname-routed to reliant-api-server (deploy/kcl/*/ingress.k), serves
	// Connect RPCs under /reliant.v1.*, and has no root route of its own —
	// the web app is a separate service. The `{$}` pattern matches only "/",
	// so no RPC path or unknown-path 404 changes behaviour.
	mux.Handle(mcpserver.RootMountPath, handler)
	logging.Info("connector MCP endpoint mounted", "path", mcpserver.MountPath)

	// RFC 9728 discovery, so a consumer MCP client can find the authorization
	// server from a failed request rather than being configured by hand.
	mcpserver.MountOAuthRoutes(mux, oauthCfg, slog.Default())
}

// connectorTokenValidator adapts reliant's JWT validator to the connector
// endpoint's OAuth token check.
//
// Reusing the deployment's existing validator is the point: an OAuth access
// token is then trusted on exactly the same basis as a session token, with one
// issuer, one key set, and one expiry policy. Standing up a second token
// authority beside a working one would mean two systems that can disagree
// about who a user is.
//
// Returns nil when no JWT validation is configured (e.g. AUTH_MODE=apikey), in
// which case connector credentials are the only accepted form.
func connectorTokenValidator(cfg *Config) mcpserver.OAuthTokenValidator {
	var validator auth.TokenValidator
	var err error

	switch {
	case cfg.JWTPublicKey != "":
		validator, err = auth.NewJWTValidator(cfg.JWTPublicKey)
	case cfg.JWKSURL != "":
		validator, err = auth.LoadJWKS(context.Background(), cfg.JWKSURL)
	default:
		return nil
	}
	if err != nil || validator == nil {
		logging.Warn("connector OAuth token validation unavailable", "error", err)
		return nil
	}
	return &jwtConnectorValidator{validator: validator}
}

// jwtConnectorValidator maps a validated JWT to the subject it identifies.
type jwtConnectorValidator struct {
	validator auth.TokenValidator
}

func (v *jwtConnectorValidator) ValidateToken(token string) (string, error) {
	claims, err := v.validator.ValidateToken(token)
	if err != nil {
		return "", err
	}
	return claims.Sub, nil
}

// controlPlaneBaseURL returns the control-plane origin, or "" when this
// deployment has none.
//
// The env names match internal/controlplane's client so a deployment
// configures one URL rather than one per consumer. Unlike that client there is
// no localhost default: silently pointing at a control plane that is not there
// would turn "cannot start workspaces" into a connection error on every
// suspended-workspace request.
func controlPlaneBaseURL() string {
	for _, key := range []string{
		"RELIANT_CONTROL_PLANE_URL",
		"CONTROL_PLANE_API_URL",
		"CONTROL_PLANE_BASE_URL",
	} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
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
