// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"github.com/go-chi/cors"
	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"go.temporal.io/sdk/client"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/handlers/account"
	"github.com/reliant-labs/reliant/internal/handlers/approval"
	"github.com/reliant-labs/reliant/internal/handlers/attachment"
	"github.com/reliant-labs/reliant/internal/handlers/background"
	"github.com/reliant-labs/reliant/internal/handlers/catalog"
	"github.com/reliant-labs/reliant/internal/handlers/chat"
	"github.com/reliant-labs/reliant/internal/handlers/connection"
	"github.com/reliant-labs/reliant/internal/handlers/connector"
	"github.com/reliant-labs/reliant/internal/handlers/daemon"
	"github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
	"github.com/reliant-labs/reliant/internal/handlers/file_system"
	"github.com/reliant-labs/reliant/internal/handlers/forge"
	"github.com/reliant-labs/reliant/internal/handlers/inbox"
	"github.com/reliant-labs/reliant/internal/handlers/mcp"
	"github.com/reliant-labs/reliant/internal/handlers/message"
	"github.com/reliant-labs/reliant/internal/handlers/model_endpoint"
	"github.com/reliant-labs/reliant/internal/handlers/package_commands"
	"github.com/reliant-labs/reliant/internal/handlers/plan"
	"github.com/reliant-labs/reliant/internal/handlers/preset"
	"github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/handlers/question"
	"github.com/reliant-labs/reliant/internal/handlers/repo"
	"github.com/reliant-labs/reliant/internal/handlers/run"
	"github.com/reliant-labs/reliant/internal/handlers/scenario"
	"github.com/reliant-labs/reliant/internal/handlers/settings"
	streaminghandler "github.com/reliant-labs/reliant/internal/handlers/streaming"
	"github.com/reliant-labs/reliant/internal/handlers/system"
	"github.com/reliant-labs/reliant/internal/handlers/task"
	"github.com/reliant-labs/reliant/internal/handlers/terminal"
	"github.com/reliant-labs/reliant/internal/handlers/token"
	"github.com/reliant-labs/reliant/internal/handlers/tool_call"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	workflowhandler "github.com/reliant-labs/reliant/internal/handlers/workflow"
	"github.com/reliant-labs/reliant/internal/handlers/worktree"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/mcpserver"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
	"github.com/reliant-labs/reliant/pkg/config"
	"github.com/reliant-labs/reliant/pkg/middleware"
)

// Server is the Connect/gRPC server
type Server struct {
	server      *http.Server
	mux         *http.ServeMux
	tlsCertFile string
	tlsKeyFile  string

	// ProjectService is exposed so callers can wire it to out-of-band
	// inputs (e.g. the daemon-events JetStream consumer in serverapi.Run).
	projectService *project.Service

	// chatService is exposed so serverapi.Run can hand undelivered messages
	// to it (queued delivery, the reconciler's wedge continuation).
	chatService *chat.Service

	router toolexec.DaemonRouter

	// worktreeSweeper settles archived worktrees' directories through their
	// daemons.
	worktreeSweeper *worktreesweep.Sweeper

	// streams is the listener's drain registry: every long-lived stream
	// (streaming RPCs, the terminal websocket, MCP event streams) is bound to
	// it, and Stop ends them. See Stop.
	streams *serverkit.Streams
}

// Config holds gRPC server configuration.
//
// Every collaborator validate names is required: serverapi.Run builds each
// one or fails boot, so NewServer refuses a Config without one rather than
// serve a surface with routes missing. A missing route answers 404, which the
// web client reports as version skew (web/src/api/rpcErrorPolicy.ts) — the
// wrong diagnosis for a composition bug.
type Config struct {
	Port                int
	BindAddress         string   // Bind address (default: "127.0.0.1", use "0.0.0.0" for containers)
	JWTPublicKey        string   // RSA public key for JWT validation
	JWKSURL             string   // JWKS endpoint URL for JWT validation (alternative to JWTPublicKey)
	CORSAllowedOrigins  []string // Allowed CORS origins; defaults to ["*"] if empty
	AllowedEmailDomains []string // If non-empty, only these email domains may access the system
	// DisableForgeAPI is the operator's kill switch for the forge RPC surface
	// (RELIANT_DISABLE_FORGE_API): ForgeService stays mounted and answers
	// every rpc with FailedPrecondition. See forgeAPIDisabled.
	DisableForgeAPI bool
	// ControlPlane is the control plane hosting this deployment; the zero
	// value is self-hosted. It picks the token store, where git tokens come
	// from, whether suspended workspaces can be woken, and the dial policy
	// for user-supplied model endpoints.
	ControlPlane    controlplane.Endpoint
	Database        db.Repository                      // Database repository. Required.
	ToolsFactory    *tools.ToolsFactory                // Tools factory for catalog service. Required.
	TemporalClient  client.Client                      // Temporal client for workflow operations. Required.
	StreamingHub    streaming.StreamingHub             // Streaming hub for ephemeral events
	UserUpdateHub   streaming.UpdateHub[db.UserUpdate] // Update hub for user-level events
	ChatUpdateHub   streaming.UpdateHub[db.ChatUpdate] // Update hub for chat-level events
	PauseService    *workflow.PauseService             // Pause service for unified pause/resume operations. Required.
	SharedTaskQueue string                             // Shared workflow task queue name

	// SQL is the raw handle on Database's database. Required. AccountService's
	// purge runs on it (an ordered multi-statement transaction the
	// per-entity Repository surface cannot express), and so does the
	// self-hosted token store when TokenAuthority is nil.
	SQL *sql.DB

	// ConnectorGrants stores connector grants, their audit log and the OAuth
	// client bindings. Required. ConnectorService manages them and the /mcp
	// endpoint resolves every call through them.
	ConnectorGrants connectorgrant.Store

	// DaemonRouter reaches the user's daemons (NATS-backed). Required: the
	// filesystem, terminal, background-process, package-command and forge
	// services, and the connector MCP endpoint, are nothing but a route to
	// a daemon.
	DaemonRouter toolexec.DaemonRouter

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
	// authenticate with `rlat_` connector credentials only.
	OAuthIssuers []string

	// TokenAuthority mints and validates `rlat_` access tokens. nil builds it
	// from the environment (tokenauthority.DepsFromEnv): control-plane when
	// RELIANT_CONTROL_PLANE_URL is set, else reliant's own store — exactly one.
	TokenAuthority tokenauthority.Authority

	// Connections backs ConnectionService. Required.
	Connections *connections.Service

	// CatalogSearch backs CatalogService.SearchCatalog / GetCatalogEntry:
	// the integration catalog index plus each caller's connection state.
	// Required.
	CatalogSearch *catalogindex.Service

	// OAuthRoutes mounts the browser half of the connection OAuth broker
	// (/integrations/oauth/{provider}/{start,callback}). Required.
	OAuthRoutes *connections.OAuthHTTP

	// TriggerInbound mounts the inbound-trigger receivers
	// (/hooks/{trigger_id}[/{token}], /integrations/{provider}/events) and
	// lets TriggerService write webhook and integration triggers. Required.
	TriggerInbound *webhook.Inbound

	// TriggerSenders backs TriggerService.ResolveTriggerSenders, keyed by
	// integration id ("github"). Optional: an integration this deployment
	// receives no events for has no directory, and nil (none at all) leaves
	// it answering Unavailable.
	TriggerSenders map[string]trigger.SenderDirectory
}

// validate fails naming every required collaborator cfg lacks.
func (cfg *Config) validate() error {
	required := []struct {
		name    string
		missing bool
	}{
		{"Database", cfg.Database == nil},
		{"SQL", cfg.SQL == nil},
		{"ConnectorGrants", cfg.ConnectorGrants == nil},
		{"DaemonRouter", cfg.DaemonRouter == nil},
		{"TemporalClient", cfg.TemporalClient == nil},
		{"PauseService", cfg.PauseService == nil},
		{"ToolsFactory", cfg.ToolsFactory == nil},
		{"CatalogSearch", cfg.CatalogSearch == nil},
		{"Connections", cfg.Connections == nil},
		{"OAuthRoutes", cfg.OAuthRoutes == nil},
		{"TriggerInbound", cfg.TriggerInbound == nil},
	}
	var missing []string
	for _, r := range required {
		if r.missing {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("grpc server config: required collaborators not set: %s", strings.Join(missing, ", "))
	}
	return nil
}

// NewServer creates a new Connect/gRPC server, with every service mounted.
// Returns an error if a required collaborator is missing (Config.validate) or
// the auth interceptor fails to initialize.
func NewServer(cfg *Config) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	database := cfg.Database

	// Create auth interceptor. The procedures it lets through unauthenticated
	// are the ones whose proto declares (forge.v1.method) auth_required: false;
	// forge generate projects them into middleware.UnauthenticatedProcedures.
	authInterceptor, err := interceptors.NewAuthInterceptor(cfg.JWTPublicKey, cfg.JWKSURL, middleware.UnauthenticatedProcedures)
	if err != nil {
		return nil, fmt.Errorf("auth interceptor setup failed: %w", err)
	}

	// ONE machine credential: `rlat_` access tokens, owned by the deployment's
	// token authority (control-plane when hosted, reliant's own store when
	// self-hosted — never both). The API interceptor introspects through a
	// CacheTTL-bounded cache; TokenService below mints through the same
	// authority.
	authority, authorityMode, err := resolveTokenAuthority(cfg)
	if err != nil {
		return nil, err
	}
	authInterceptor.SetAccessTokenIntrospector(accesstokenclient.NewCachedIntrospector(authority))
	domainWhitelistInterceptor := interceptors.NewDomainWhitelistInterceptor(cfg.AllowedEmailDomains)

	// Order matters: recovery (outermost) -> timeout -> auth -> domain whitelist (innermost).
	// A failed RPC reaches Sentry once, as forge's "rpc failed" line through
	// the slog bridge (internal/logging), grouped per procedure and code.
	timeouts := interceptors.NewTimeoutInterceptor().WithMethodTimeouts(forge.ForgeRPCDeadlines())
	opts := newHandlerOptions(true, timeouts.Interceptor(), authInterceptor, domainWhitelistInterceptor)
	// Innermost, so logging records the status the client received: every
	// streaming handler is bound to the listener's drain registry, and a
	// stream the drain ends finishes OK with the x-forge-drained trailer —
	// the end of a watch, which StreamUserUpdates and StreamProcessOutput
	// clients resume from their cursor. Not Unavailable, which the web client
	// reads as the network under every multiplexed read dying.
	opts = append(opts, connect.WithInterceptors(serverkit.StreamDrainInterceptor()))

	// Build a DaemonRouter for services that need transport-agnostic daemon access.
	// The api-server itself never accepts daemon bidi streams — daemons connect to
	// the daemon-gateway process (see internal/servergateway) and the api-server
	// reaches them through this router (NATS-backed in production).
	router := cfg.DaemonRouter

	// Create services
	//
	// The services already on forge's handler model (internal/handlers/<svc>,
	// research/forge-migration/PLAN.md P3b) take a logger and the typed
	// config. reliant has no typed config yet (P4), so they share an empty one.
	handlerLogger := slog.Default()
	handlerConfig := &config.Config{}
	systemService, err := system.New(system.Deps{
		Logger:        handlerLogger,
		Config:        handlerConfig,
		Repo:          database,
		Temporal:      cfg.TemporalClient,
		NATSConnected: cfg.NATSChecker,
		StreamingHub:  cfg.StreamingHub,
	})
	if err != nil {
		return nil, fmt.Errorf("system service: %w", err)
	}
	planService, err := plan.New(plan.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
	})
	if err != nil {
		return nil, fmt.Errorf("plan service: %w", err)
	}
	taskService, err := task.New(task.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
	})
	if err != nil {
		return nil, fmt.Errorf("task service: %w", err)
	}
	catalogService, err := catalog.New(catalog.Deps{
		Logger:             handlerLogger,
		Config:             handlerConfig,
		Tools:              cfg.ToolsFactory,
		TagPrefs:           database,
		LocalModels:        local.NewRepoDirectory(database),
		Search:             cfg.CatalogSearch,
		IntegrationMethods: catalog.IntegrationMethodsFrom(cfg.Connections),
	})
	if err != nil {
		return nil, fmt.Errorf("catalog service: %w", err)
	}
	// Only a deployment WITH a control plane can clone from GitHub on the
	// user's behalf; self-hosted leaves ControlPlane nil.
	var projectControlPlane project.CloneDispatcher
	if cfg.ControlPlane.Hosted() {
		projectControlPlane = controlplane.NewClient(cfg.ControlPlane.URL)
	}
	projectService, err := project.New(project.Deps{
		Logger:       handlerLogger,
		Config:       handlerConfig,
		Repo:         database,
		Router:       router,
		ControlPlane: projectControlPlane,
	})
	if err != nil {
		return nil, fmt.Errorf("project service: %w", err)
	}
	worktreeSweeper := worktreesweep.New(database, router).WithKeepFiles(worktreesweep.KeepSetting{R: database})
	worktreeService, err := worktree.New(worktree.Deps{
		Logger:   handlerLogger,
		Config:   handlerConfig,
		Repo:     database,
		Router:   router,
		Temporal: cfg.TemporalClient,
		Settler:  worktreeSweeper,
	})
	if err != nil {
		return nil, fmt.Errorf("worktree service: %w", err)
	}
	repoService, err := repo.New(repo.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
		Router: router,
	})
	if err != nil {
		return nil, fmt.Errorf("repo service: %w", err)
	}
	approvalService, err := approval.New(approval.Deps{
		Logger:  handlerLogger,
		Config:  handlerConfig,
		Repo:    database,
		Signals: cfg.PauseService,
	})
	if err != nil {
		return nil, fmt.Errorf("approval service: %w", err)
	}
	questionService, err := question.New(question.Deps{
		Logger:  handlerLogger,
		Config:  handlerConfig,
		Repo:    database,
		Signals: cfg.PauseService,
	})
	if err != nil {
		return nil, fmt.Errorf("question service: %w", err)
	}
	chatService, err := chat.New(chat.Deps{
		Logger:    handlerLogger,
		Config:    handlerConfig,
		Repo:      database,
		Temporal:  cfg.TemporalClient,
		Pause:     cfg.PauseService,
		TaskQueue: cfg.SharedTaskQueue,
		Router:    router,
	})
	if err != nil {
		return nil, fmt.Errorf("chat service: %w", err)
	}
	messageService, err := message.New(message.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
	})
	if err != nil {
		return nil, fmt.Errorf("message service: %w", err)
	}
	settingsService, err := settings.New(settings.Deps{
		Logger:             handlerLogger,
		Config:             handlerConfig,
		Repo:               database,
		ControlPlane:       controlplane.NewClient(cfg.ControlPlane.URL),
		ExchangeClaudeCode: claude.ExchangeClaudeAuthorizationCode,
	})
	if err != nil {
		return nil, fmt.Errorf("settings service: %w", err)
	}
	mcpService, err := mcp.New(mcp.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
		Router: router,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp service: %w", err)
	}
	workflowService, err := workflowhandler.New(workflowhandler.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
		Router: router,
	})
	if err != nil {
		return nil, fmt.Errorf("workflow service: %w", err)
	}
	scenarioService, err := scenario.New(scenario.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
		Router: router,
	})
	if err != nil {
		return nil, fmt.Errorf("scenario service: %w", err)
	}
	// PackageCommands is a browser-facing workspace service: its command
	// discovery runs on the daemon's filesystem, through the router (run
	// against the api-server's, it silently returned an empty command list).
	packageCommandsService, err := package_commands.New(package_commands.Deps{
		Logger:    handlerLogger,
		Config:    handlerConfig,
		Router:    router,
		Repo:      database,
		Processes: pkgmgr.NewService(),
	})
	if err != nil {
		return nil, fmt.Errorf("package_commands service: %w", err)
	}
	packageCommandsPath, packageCommandsHandler := reliantv1connect.NewPackageCommandsServiceHandler(packageCommandsService, opts...)

	streamingService, err := streaminghandler.New(streaminghandler.Deps{
		Logger:      handlerLogger,
		Config:      handlerConfig,
		Repo:        database,
		Deltas:      cfg.StreamingHub,
		UserUpdates: cfg.UserUpdateHub,
		ChatUpdates: cfg.ChatUpdateHub,
	})
	if err != nil {
		return nil, fmt.Errorf("streaming service: %w", err)
	}

	attachmentService, err := attachment.New(attachment.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
	})
	if err != nil {
		return nil, fmt.Errorf("attachment service: %w", err)
	}
	presetService, err := preset.New(preset.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
	})
	if err != nil {
		return nil, fmt.Errorf("preset service: %w", err)
	}

	daemonRegistryService, err := daemon_registry.New(daemon_registry.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
		Router: router,
	})
	if err != nil {
		return nil, fmt.Errorf("daemon_registry service: %w", err)
	}
	// Credentials stays nil: endpoint API keys have one home, the sealed
	// connection store, and no modelendpoints.EndpointCredentials over it
	// exists yet, so storing a key answers ErrCredentialStoreUnavailable.
	modelEndpointService, err := model_endpoint.New(model_endpoint.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
		Router: router,
		// The control-plane block in its config shape (cmd/reliant's
		// controlPlaneEndpoint, inverted); its URL picks the dial policy for
		// endpoint targets.
		ControlPlane: &configv1.ControlPlaneConfig{
			Url:                   cfg.ControlPlane.URL,
			InternalServiceSecret: cfg.ControlPlane.InternalServiceSecret,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("model_endpoint service: %w", err)
	}
	// TokenService: the ONE token surface (daemon credentials and API
	// tokens), a facade over the token authority.
	var gitTokens token.GitTokenSource
	if cp := cfg.ControlPlane; cp.Hosted() {
		// Hosted users' git tokens live at control-plane. tokenauthority.New
		// already refused to boot without the secret; check again so this
		// wiring never rests on that ordering.
		secret := cp.InternalServiceSecret
		if secret == "" {
			return nil, errors.New("git tokens: RELIANT_CONTROL_PLANE_URL is set but INTERNAL_SERVICE_SECRET is not")
		}
		gitTokens = gitcredentialclient.New(gitcredentialclient.Deps{
			BaseURL: cp.URL,
			Sign:    func() (string, error) { return auth.SignInternalServiceToken(secret) },
		})
	}
	tokenService, err := token.New(token.Deps{
		Logger:    handlerLogger,
		Config:    handlerConfig,
		Authority: authority,
		ControlPlane: token.TokenControlPlane{
			Issuer:      os.Getenv(cliauth.AuthorizationServerEnv),
			ClipsGrants: authorityMode == tokenauthority.ModeControlPlane,
		},
		GitTokens: gitTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("token service: %w", err)
	}
	daemonProxyService, err := daemon.New(daemon.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Router: router,
	})
	if err != nil {
		return nil, fmt.Errorf("daemon service: %w", err)
	}
	toolCallService, err := tool_call.New(tool_call.Deps{
		Logger:   handlerLogger,
		Config:   handlerConfig,
		Repo:     database,
		Temporal: cfg.TemporalClient,
		Router:   router,
	})
	if err != nil {
		return nil, fmt.Errorf("tool_call service: %w", err)
	}

	// Register services with Connect handlers
	// Connect generates HTTP handlers that work with standard net/http
	systemPath, systemHandler := reliantv1connect.NewSystemServiceHandler(systemService, opts...)
	planPath, planHandler := reliantv1connect.NewPlanServiceHandler(planService, opts...)
	taskPath, taskHandler := reliantv1connect.NewTaskServiceHandler(taskService, opts...)
	catalogPath, catalogHandler := reliantv1connect.NewCatalogServiceHandler(catalogService, opts...)
	projectPath, projectHandler := reliantv1connect.NewProjectServiceHandler(projectService, opts...)
	// The services whose requests wake an asleep machine (machinewake)
	// report the wake as Unavailable + DaemonWaking, whatever error they wrap
	// it in. Cloned so the shared opts slice is never appended into.
	machineWakeOpts := append(slices.Clone(opts), connect.WithInterceptors(machinewake.NewInterceptor()))
	worktreePath, worktreeHandler := reliantv1connect.NewWorktreeServiceHandler(worktreeService, machineWakeOpts...)
	repoPath, repoHandler := reliantv1connect.NewRepoServiceHandler(repoService, opts...)
	approvalPath, approvalHandler := reliantv1connect.NewApprovalServiceHandler(approvalService, opts...)
	questionPath, questionHandler := reliantv1connect.NewQuestionServiceHandler(questionService, opts...)
	chatPath, chatHandler := reliantv1connect.NewChatServiceHandler(chatService, opts...)
	// RunService is the engine's execution API. It delegates to chatService
	// rather than reimplementing anything, so mounting it adds surface without
	// adding behavior.
	runService, err := run.New(run.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
		Chats:  chatService,
	})
	if err != nil {
		return nil, fmt.Errorf("run service: %w", err)
	}
	runPath, runHandler := reliantv1connect.NewRunServiceHandler(runService, opts...)
	messagePath, messageHandler := reliantv1connect.NewMessageServiceHandler(messageService, opts...)
	settingsPath, settingsHandler := reliantv1connect.NewSettingsServiceHandler(settingsService, opts...)
	mcpPath, mcpHandler := reliantv1connect.NewMCPServiceHandler(mcpService, opts...)
	workflowPath, workflowHandler := reliantv1connect.NewWorkflowServiceHandler(workflowService, opts...)
	scenarioPath, scenarioHandler := reliantv1connect.NewScenarioServiceHandler(scenarioService, opts...)
	toolCallPath, toolCallHandler := reliantv1connect.NewToolCallServiceHandler(toolCallService, opts...)

	// ForgeService reads a forge project's release/env/secret state off the
	// DAEMON's filesystem, through the router.
	//
	// It is always mounted. cfg.DisableForgeAPI, the operator's kill switch,
	// puts forgeAPIDisabled innermost in its chain instead: every rpc then
	// answers FailedPrecondition, after auth, before the handler runs.
	forgeService, err := forge.New(forge.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Router: router,
		Repo:   database,
	})
	if err != nil {
		return nil, fmt.Errorf("forge service: %w", err)
	}
	forgeOpts := opts
	if cfg.DisableForgeAPI {
		logging.Info("forge API disabled in this deployment (RELIANT_DISABLE_FORGE_API): ForgeService answers FailedPrecondition")
		// Cloned so the shared opts slice is never appended into.
		forgeOpts = append(slices.Clone(opts), connect.WithInterceptors(forgeAPIDisabled{}))
	}
	forgePath, forgeHandler := reliantv1connect.NewForgeServiceHandler(forgeService, forgeOpts...)

	streamingPath, streamingHandler := reliantv1connect.NewStreamingServiceHandler(streamingService, opts...)

	attachmentPath, attachmentHandler := reliantv1connect.NewAttachmentServiceHandler(attachmentService, opts...)
	presetPath, presetHandler := reliantv1connect.NewPresetServiceHandler(presetService, opts...)

	// TriggerService converges Temporal Schedules onto the triggers table
	// (trigger.TemporalBackend), and writes webhook and integration triggers
	// against the inbound receivers; the schedule backend also polls the
	// integrations the inbound registry polls.
	in := cfg.TriggerInbound
	triggerDeps := trigger.Deps{
		Logger:    handlerLogger,
		Config:    handlerConfig,
		Repo:      database,
		Senders:   cfg.TriggerSenders,
		PublicURL: in.PublicURL,
		Intake:    in.Intake,
	}
	// Each assigned only when set: a nil pointer stored in an interface field
	// would pass the handler's nil checks.
	var polled func(integration string) bool
	if in.Registry != nil {
		triggerDeps.Catalog = in.Registry
		polled = in.Registry.IsPolled
	}
	if in.Vault != nil {
		triggerDeps.Sealer = in.Vault
	}
	triggerDeps.Access = make(map[string]trigger.IntegrationAccess, len(in.Access))
	for id, a := range in.Access {
		triggerDeps.Access[id] = a
	}
	triggerDeps.Syncer, triggerDeps.Fires = trigger.TemporalBackend(cfg.TemporalClient, database, cfg.SharedTaskQueue, polled)
	if cfg.ControlPlane.Hosted() {
		triggerDeps.ControlPlane = controlplane.NewClient(cfg.ControlPlane.URL)
	}
	triggerService, err := trigger.New(triggerDeps)
	if err != nil {
		return nil, fmt.Errorf("trigger service: %w", err)
	}
	inboxService, err := inbox.New(inbox.Deps{
		Logger:  handlerLogger,
		Config:  handlerConfig,
		Repo:    database,
		Sweeper: worktreeSweeper,
	})
	if err != nil {
		return nil, fmt.Errorf("inbox service: %w", err)
	}
	inboxPath, inboxHandler := reliantv1connect.NewInboxServiceHandler(inboxService, opts...)
	triggerPath, triggerHandler := reliantv1connect.NewTriggerServiceHandler(triggerService, opts...)

	// FileSystem, Background, and Terminal services: proxies that forward each
	// request through the daemon router (NATS) to the connected daemon, which
	// holds the files, processes and sessions.
	fileSystemService, err := file_system.New(file_system.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Router: router,
		Repo:   database,
	})
	if err != nil {
		return nil, fmt.Errorf("file_system service: %w", err)
	}
	filesystemPath, filesystemHandler := reliantv1connect.NewFileSystemServiceHandler(fileSystemService, machineWakeOpts...)

	bgProxy, err := background.New(background.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Router: router,
	})
	if err != nil {
		return nil, fmt.Errorf("background service: %w", err)
	}
	backgroundPath, backgroundHandler := reliantv1connect.NewBackgroundServiceHandler(bgProxy, opts...)

	termProxy, err := terminal.New(terminal.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Router: router,
	})
	if err != nil {
		return nil, fmt.Errorf("terminal service: %w", err)
	}
	terminalPath, terminalHandler := reliantv1connect.NewTerminalServiceHandler(termProxy, opts...)

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
	// A terminal websocket lives as long as the browser tab: long-lived, so
	// the drain ends it (with a reconnect close code) when Stop begins.
	mux.Handle("/api/v2/terminal/ws", serverkit.LongLived(
		terminal.TerminalWSHandler(router, wsValidator, terminal.WithTerminalCheckouts(database))))

	// DaemonRegistryService: browser-driven list/get/resolve/resume. The
	// ToolsDaemonService streaming endpoint is hosted by the daemon-gateway,
	// which validates the daemon's `rlat_` at connect time (see
	// internal/grpc/daemon_server.go).
	daemonRegistryPath, daemonRegistryHandler := reliantv1connect.NewDaemonRegistryServiceHandler(daemonRegistryService, opts...)
	tokenPath, tokenHandler := reliantv1connect.NewTokenServiceHandler(tokenService, opts...)

	// AccountService (account deletion) runs on the raw *sql.DB: the purge is
	// an ordered multi-statement transaction across every user-owned table,
	// which the per-entity Repository surface cannot express.
	//
	// The control-plane client is wired only when this deployment HAS a
	// control plane (cfg.ControlPlane). Self-hosted reliant has no platform
	// account to delete, and defaulting to localhost would make every such
	// deployment fail deletion against a control plane that isn't there.
	var accountControlPlane account.ControlPlane
	if cfg.ControlPlane.Hosted() {
		accountControlPlane = controlplane.NewClient(cfg.ControlPlane.URL)
	}
	accountService, err := account.New(account.Deps{
		Logger:       handlerLogger,
		Config:       handlerConfig,
		SQL:          cfg.SQL,
		ControlPlane: accountControlPlane,
	})
	if err != nil {
		return nil, fmt.Errorf("account service: %w", err)
	}
	accountPath, accountHandler := reliantv1connect.NewAccountServiceHandler(accountService, opts...)
	daemonPath, daemonHandler := reliantv1connect.NewDaemonServiceHandler(daemonProxyService, opts...)

	// ConnectorService manages grants for third-party MCP clients, in the same
	// store the /mcp endpoint (mountConnectorMCP) redeems them from.
	connectorService, err := connector.New(connector.Deps{
		Logger:    handlerLogger,
		Config:    handlerConfig,
		Grants:    cfg.ConnectorGrants,
		Tokens:    authority,
		PublicURL: cfg.PublicURL,
	})
	if err != nil {
		return nil, fmt.Errorf("connector service: %w", err)
	}
	connectorPath, connectorHandler := reliantv1connect.NewConnectorServiceHandler(connectorService, opts...)

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
	mux.Handle(runPath, runHandler)
	mux.Handle(messagePath, messageHandler)
	mux.Handle(settingsPath, settingsHandler)
	mux.Handle(mcpPath, mcpHandler)
	mux.Handle(workflowPath, workflowHandler)
	mux.Handle(scenarioPath, scenarioHandler)
	mux.Handle(filesystemPath, filesystemHandler)
	mux.Handle(packageCommandsPath, packageCommandsHandler)
	mux.Handle(toolCallPath, toolCallHandler)
	mux.Handle(forgePath, forgeHandler)

	mux.Handle(streamingPath, streamingHandler)

	mux.Handle(attachmentPath, attachmentHandler)
	// Range-capable byte serving for <video>; Connect GetAttachment is whole-blob.
	mux.Handle(attachment.AttachmentContentPathPrefix, attachment.AttachmentContentHandler(database, authInterceptor.AuthenticateHTTP))
	mux.Handle(presetPath, presetHandler)
	mux.Handle(triggerPath, triggerHandler)
	mux.Handle(inboxPath, inboxHandler)

	mux.Handle(daemonRegistryPath, daemonRegistryHandler)
	modelEndpointPath, modelEndpointHandler := reliantv1connect.NewModelEndpointServiceHandler(modelEndpointService, opts...)
	mux.Handle(modelEndpointPath, modelEndpointHandler)
	mux.Handle(tokenPath, tokenHandler)
	mux.Handle(daemonPath, daemonHandler)

	mux.Handle(accountPath, accountHandler)

	connectionService, err := connection.New(connection.Deps{
		Logger:      handlerLogger,
		Config:      handlerConfig,
		Connections: cfg.Connections,
	})
	if err != nil {
		return nil, fmt.Errorf("connection service: %w", err)
	}
	connectionPath, connectionHandler := reliantv1connect.NewConnectionServiceHandler(connectionService, opts...)
	mux.Handle(connectionPath, connectionHandler)
	cfg.OAuthRoutes.Register(func(pattern string, h http.Handler) { mux.Handle(pattern, h) })
	// Inbound trigger receivers: plain HTTP routes outside the Connect
	// interceptor chain, authenticated by the trigger's own token or the
	// provider's signature rather than a user's JWT.
	cfg.TriggerInbound.Register(func(pattern string, h http.Handler) { mux.Handle(pattern, h) })

	mux.Handle(connectorPath, connectorHandler)
	mux.Handle(backgroundPath, backgroundHandler)
	mux.Handle(terminalPath, terminalHandler)

	// The CLI's api-token management and workflow-trigger surfaces are Connect
	// RPCs now (TokenService above, ChatService.StartChat), not bespoke
	// /api/v1 JSON handlers — the CLI speaks the same authenticated Connect
	// path the web app does.

	// Connector MCP endpoint: third-party MCP clients (ChatGPT, Claude, and
	// their mobile apps) driving a cloud daemon under a connector grant.
	//
	// Authenticated by an `rlat_` connector credential rather than the user JWT the
	// Connect handlers above use, so it is mounted as a plain HTTP route
	// outside the interceptor chain. Confinement is enforced daemon-side at
	// command dispatch (internal/daemonpolicy); this route resolves the grant
	// and terminates the protocol.
	if err := mountConnectorMCP(mux, database, cfg.ConnectorGrants, router, cfg.PublicURL, cfg.OAuthIssuers,
		connectorTokenValidator(cfg), accesstokenclient.NewCachedIntrospector(authority), cfg.ControlPlane.URL); err != nil {
		return nil, fmt.Errorf("connector MCP endpoint: %w", err)
	}

	// CLI login discovery (RFC 8414): names the control plane that issues this
	// deployment's rlat_ tokens, so `reliant auth login --server <this>` finds
	// where to send the browser. Absent on a self-hosted server with no
	// control plane: it offers no browser login.
	if h := cliauth.MetadataHandler(os.Getenv(cliauth.AuthorizationServerEnv)); h != nil {
		mux.Handle(oauth2.AuthorizationServerMetadataPath, h)
	}

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

	corsHandler := newCORSHandler(cfg.CORSAllowedOrigins)

	// The listener is serverkit's: HTTP/1.1, HTTP/2 over TLS and
	// prior-knowledge h2c on one port, every request dispatched through the
	// CORS and security-headers middleware, and the drain registry installed
	// into every request's context. TLS is chosen at Start.
	streams := serverkit.NewStreams()
	srv := serverkit.NewHTTPServer(listenerConfig(cfg.BindAddress, cfg.Port), corsHandler(securityHeaders(mux)), streams)

	return &Server{
		server:          srv,
		mux:             mux,
		tlsCertFile:     cfg.TLSCertFile,
		tlsKeyFile:      cfg.TLSKeyFile,
		projectService:  projectService,
		chatService:     chatService,
		router:          router,
		worktreeSweeper: worktreeSweeper,
		streams:         streams,
	}, nil
}

// corsAllowedHeaders are the request headers a cross-origin browser may send.
//
// Every header the web client attaches to an RPC must be listed. In prod the
// web app (app.reliantlabs.io) calls this server cross-origin
// (api.reliantapi.com), so the browser preflights each RPC, and go-chi/cors
// answers a preflight that names an unlisted header with a bare 200 and no
// Access-Control-Allow-Headers. The browser then never sends the request and
// reports a CORS error for the RPC itself — so one missing name takes down
// every RPC that carries it.
//
// TestCORSAllowsEveryWebClientRequestHeader reads the web client's source and
// fails when a header it sets is missing here.
var corsAllowedHeaders = []string{
	"Accept", "Authorization", "Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms",
	"X-CSRF-Token", "traceparent", "tracestate", "sentry-trace", "baggage", "Range",
	// The last DAEMON_HEARTBEAT the web client saw (web/src/api/transport.ts),
	// which lets the auth interceptor skip the IsDaemonOnline query. The client
	// attaches it to every RPC once a cloud machine heartbeats; missing here,
	// it made every web RPC fail CORS for any user with a running machine.
	"X-Daemon-Last-Seen",
}

// corsExposedHeaders are the response headers cross-origin JavaScript may
// read. The browser hides every other non-safelisted response header, so a
// header the web client reads that is missing here reads as absent — the code
// path keyed on it silently never runs.
//
// TestCORSExposesEveryHeaderTheWebClientReads reads the web client's source and
// fails when a header it reads is missing here.
var corsExposedHeaders = []string{
	"Link", "Content-Range", "Accept-Ranges", "Content-Length",
	// The billing/quota reason and upgrade link on a Connect error. The web
	// client's upgradeInterceptor (web/src/api/upgradeInterceptor.ts) reads
	// them on errors from every transport to open the upgrade and
	// billing-email modals instead of a raw error toast.
	"X-Reliant-Reason", "X-Reliant-Upgrade-Url",
}

// newCORSHandler builds the CORS middleware that wraps the entire mux. An
// empty origin list allows any origin, without credentials (the CORS spec
// forbids credentials with a wildcard origin). MaxAge caches preflights for
// 24 hours to cut OPTIONS round trips.
func newCORSHandler(origins []string) func(http.Handler) http.Handler {
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	return cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   corsAllowedHeaders,
		ExposedHeaders:   corsExposedHeaders,
		AllowCredentials: !slices.Contains(origins, "*"),
		MaxAge:           86400,
	})
}

// ProjectService returns the project service so callers can wire it to
// out-of-band inputs (e.g. JetStream consumers in serverapi.Run).
func (s *Server) ProjectService() *project.Service {
	return s.projectService
}

// ChatService returns the chat service, for out-of-band callers that start
// runs on a chat's behalf (serverapi.Run's queued delivery).
func (s *Server) ChatService() *chat.Service {
	return s.chatService
}

// WorktreeSweeper returns the sweeper that settles archived worktrees.
func (s *Server) WorktreeSweeper() *worktreesweep.Sweeper {
	return s.worktreeSweeper
}

// DaemonRouter returns the daemon router used by this server.
func (s *Server) DaemonRouter() toolexec.DaemonRouter {
	return s.router
}

// Start binds the server's port and serves on it in the background. A port
// that cannot be bound, or a TLS key pair that cannot be loaded, fails Start.
func (s *Server) Start() error {
	return listenAndServe(s.server, s.tlsCertFile, s.tlsKeyFile, "Connect/gRPC server")
}

// IsTLS returns whether the server is configured to use TLS
func (s *Server) IsTLS() bool {
	return s.tlsCertFile != "" && s.tlsKeyFile != ""
}

// Stop gracefully stops the gRPC server (serverkit.DrainHTTP): it stops
// accepting connections, ends every long-lived stream, and waits — bounded by
// ctx — for the unary requests in flight to finish.
//
// Ending the streams is what lets it return. http.Server.Shutdown waits for
// every active request, and a long-lived stream (StreamUserUpdates holds one
// per open browser tab) never finishes on its own, so Shutdown used to wait
// out the whole SHUTDOWN_TIMEOUT and fail with context.DeadlineExceeded on
// every rollout — then the process exited and cut those streams with no
// status at all. Now the drain ends them as Shutdown begins: streaming RPCs
// end with OK, the end of a watch their clients resume from; the terminal
// websocket closes with 1012 (service restart), which the browser reconnects
// on; MCP event streams end and their clients re-open them.
//
// Callers must have flipped readiness and waited out the pre-stop delay
// first (serverkit.Drainer.Begin), so those reconnects reach a replica that
// is still serving rather than this one.
func (s *Server) Stop(ctx context.Context) error {
	logging.Info("Stopping Connect/gRPC server", "streams_open", s.streams.Active())
	err := serverkit.DrainHTTP(ctx, s.server, s.streams)
	logDrainReport("Connect/gRPC server drained", s.streams.Report())
	return err
}

// Mux returns the HTTP mux for mounting additional handlers
func (s *Server) Mux() *http.ServeMux {
	return s.mux
}

// newHandlerOptions builds the standard Connect handler options with the shared
// interceptor chain. See newInterceptors for trustRemoteTrace.
func newHandlerOptions(trustRemoteTrace bool, timeoutInterceptor connect.Interceptor, authInterceptors ...connect.Interceptor) []connect.HandlerOption {
	all := newInterceptors(trustRemoteTrace, timeoutInterceptor, authInterceptors...)
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
// Reliant-specific interceptors (slow-RPC watchdog, timeout, auth) are passed as Extras.
//
// trustRemoteTrace controls whether a caller-supplied traceparent becomes the
// parent of the server span. The public API trusts it for browser-to-API trace
// continuity. The daemon-facing server must not: daemons run on user machines
// and are untrusted, so a caller could otherwise pick the trace ID and sampled
// flag. Untrusted servers start a new root span and record the remote context
// as a span link only.
func newInterceptors(trustRemoteTrace bool, timeoutInterceptor connect.Interceptor, authInterceptors ...connect.Interceptor) []connect.Interceptor {
	extras := []connect.Interceptor{
		interceptors.NewSlowRPCWatchdogInterceptor(),
		timeoutInterceptor,
	}
	for _, ai := range authInterceptors {
		if isNilInterceptor(ai) {
			continue
		}
		extras = append(extras, ai)
	}

	var otelOpts []otelconnect.Option
	if trustRemoteTrace {
		otelOpts = append(otelOpts, otelconnect.WithTrustRemote())
	}
	otelInterceptor, err := otelconnect.NewInterceptor(otelOpts...)
	if err != nil {
		panic(fmt.Sprintf("create Connect OpenTelemetry interceptor: %v", err))
	}
	extras = append([]connect.Interceptor{otelInterceptor}, extras...)
	return observe.DefaultMiddlewares(observe.DefaultMiddlewareDeps{
		// otelconnect owns the Connect server span and propagation. Supplying a
		// Forge tracing interceptor here would create a duplicate span per RPC.
		Extras: extras,
		// A failed RPC's one record — forge's "rpc failed" — is classified by
		// the logging interceptor, and the process handler trusts a record
		// that already carries error_class, so it never applies its own
		// classifier (internal/logging) to it. Without the same one here, a
		// failure Temporal carries as Benign — nobody has to act — logged at
		// ERROR, error_class=server, and counted as a dead-end error.
		// A stream this server ended because it is shutting down (the
		// daemon-gateway's "gateway draining") needs no option here: forge's
		// logging treats svcerr.ErrServerDraining as an expected outcome,
		// logged like a success and never at ERROR.
		LogOptions: []observe.LogOption{
			observe.WithErrorClassifier(errclass.Library),
		},
	})
}

// bindEventStreams marks MCP's server-to-client event streams long-lived: the
// streamable-HTTP transport's standalone GET stream and the legacy /sse
// stream are GETs that stay open for as long as the client is connected, so
// http.Server.Shutdown would wait on them forever. The drain ends them at
// Stop, and the client re-opens the stream against a live replica. POSTs
// carry one call each and are drained like any other request.
func bindEventStreams(next http.Handler) http.Handler {
	bound := serverkit.LongLived(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			bound.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
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

// mountConnectorMCP mounts the connector MCP endpoint: grants resolves each
// call's connector grant and OAuth client binding, and router carries the
// call to the grant's daemon.
func mountConnectorMCP(
	mux *http.ServeMux,
	database db.Repository,
	grants connectorgrant.Store,
	router toolexec.DaemonRouter,
	publicURL string,
	oauthIssuers []string,
	tokenValidator mcpserver.OAuthTokenValidator,
	credentials mcpserver.CredentialIntrospector,
	controlPlaneURL string,
) error {
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
	// The URL has no localhost default (the configured one, not the client's
	// fallback): silently pointing at a control plane that is not there would
	// turn "cannot start workspaces" into a connection error on every
	// suspended-workspace request.
	if cp := mcpserver.NewControlPlaneResumer(controlPlaneURL); cp != nil {
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
		Store:       grants,
		Credentials: credentials,
		Sender:      router,
		Waker:       waker,
		OAuth:       oauthCfg,
		// Reuses the deployment's existing JWT validator, so an OAuth token is
		// trusted on exactly the same basis as a session token.
		TokenValidator: tokenValidator,
		// Bindings answer "which connector does this application act
		// through?", recorded at consent.
		Bindings:       grants,
		ConsentBaseURL: publicURL,
	})
	if err != nil {
		return err
	}

	// Both the bare path and the subtree: MCP clients differ on whether they
	// append a trailing slash, and a 404 on the handshake is opaque to debug
	// from a phone.
	// The handler owns routing for both transports (streamable-HTTP at /mcp,
	// legacy SSE at /sse), so every path it serves is registered here.
	handler = bindEventStreams(handler)
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
	return nil
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

// maxConcurrentStreams is the HTTP/2 stream cap per connection on both
// listeners: a browser multiplexes every streaming RPC of every open tab over
// one connection, and net/http's default (250) is too few for that.
const maxConcurrentStreams = 1000

// listenerConfig is the serverkit listener configuration both servers share,
// on bindAddress:port (bindAddress "" is loopback).
//
// The protocols are serverkit's: HTTP/1.1, HTTP/2 over TLS, and native
// prior-knowledge h2c, which net/http detects by peeking for the client
// preface — the RFC 7540 "Upgrade: h2c" handshake is not supported and no
// Reliant client uses it (every HTTP/2 client here is an http2.Transport with
// AllowHTTP, which is prior-knowledge only). A native h2c connection is the
// server's own, so IdleTimeout reaches it and Shutdown drains it. IdleTimeout
// only counts time with zero open streams, so a long-lived bidi stream still
// holds the connection open indefinitely.
//
// ReadTimeout is none: over HTTP/2 it is a deadline on each stream's request
// body, so it would cut every bidi stream (StreamTerminal, ConnectDaemon) at
// the same mark. There is no WriteTimeout either, for the same reason on the
// response side; serverkit's write-stall guard bounds a write to a peer that
// stopped reading instead.
func listenerConfig(bindAddress string, port int) serverkit.Config {
	if bindAddress == "" {
		bindAddress = "127.0.0.1"
	}
	return serverkit.Config{
		Addr:                      fmt.Sprintf("%s:%d", bindAddress, port),
		ReadTimeout:               -1,
		ReadHeaderTimeout:         transport.ServerReadHeaderTimeout,
		IdleTimeout:               transport.ServerIdleTimeout,
		HTTP2MaxConcurrentStreams: maxConcurrentStreams,
	}
}

// listenAndServe binds srv's address now — so a port in use fails boot rather
// than being logged from a goroutine — and serves srv on it in the
// background: over TLS when certFile and keyFile are set (h2 negotiated by
// ALPN), otherwise cleartext.
func listenAndServe(srv *http.Server, certFile, keyFile, name string) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("failed to bind %s: %w", name, err)
	}
	serve := func() error { return srv.Serve(ln) }
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("failed to load TLS cert: %w", err)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		serve = func() error { return srv.ServeTLS(ln, "", "") }
		logging.Info("Starting "+name+" with TLS (HTTPS/HTTP2)", "address", ln.Addr().String(), "cert", certFile)
	} else {
		logging.Info("Starting "+name+" (h2c)", "address", ln.Addr().String())
	}
	go func() {
		if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.Error(name+" failed", "error", err)
		}
	}()
	return nil
}

// logDrainReport writes one line saying what a listener's drain did to its
// long-lived streams.
func logDrainReport(msg string, r serverkit.DrainReport) {
	logging.Info(msg,
		"streams_ended", r.Ended, "streams_claimed", r.Claimed, "streams_hard_ended", r.HardEnded,
		"hijacked_conns_closed", r.ConnsClosed, "streams_still_running", r.StillRunning)
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

// resolveTokenAuthority returns the deployment's ONE token authority: the
// configured one, else control-plane or the local store as the environment
// dictates (the self-hosted store keeps its tokens in cfg.SQL). A
// misconfiguration (control-plane URL without its secret) fails server start
// loudly rather than serving a surface that can mint nothing.
func resolveTokenAuthority(cfg *Config) (tokenauthority.Authority, tokenauthority.Mode, error) {
	if cfg.TokenAuthority != nil {
		// An injected authority states no mode, so nothing that depends on
		// control-plane-side clipping is enabled for it.
		return cfg.TokenAuthority, "", nil
	}
	authority, mode, err := tokenauthority.New(tokenauthority.DepsFor(cfg.ControlPlane, cfg.SQL))
	if err != nil {
		return nil, "", fmt.Errorf("token authority: %w", err)
	}
	logging.Info("access-token authority selected", "mode", mode)
	return authority, mode, nil
}
