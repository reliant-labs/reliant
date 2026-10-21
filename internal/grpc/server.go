// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/go-chi/cors"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/reliant-labs/forge/pkg/orm"
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
	"github.com/reliant-labs/reliant/internal/handlers/provider_account"
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
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/drivers/copilot"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/keyvalidate"
	"github.com/reliant-labs/reliant/internal/llm/providerusage"
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

// API is what the API listener serves: every reliant.v1 service a browser,
// the CLI or an `rlat_` token calls, and the plain-HTTP routes beside them
// (the terminal websocket, attachment bytes, the connector MCP endpoint, the
// OAuth and inbound-trigger callbacks, CLI login discovery, /health). It is
// the listener's serverkit.Routes: Register mounts each service with the
// listener's chain. Build it with NewAPI.
type API struct {
	// services are the Connect services, in mount order.
	services []apiService
	// http mounts the plain-HTTP routes. They are outside the chain: each
	// authenticates itself (a user token, a connector credential, a
	// trigger's own token or a provider signature) or is public.
	http []func(mux *http.ServeMux)

	// auth authenticates users. The chain's Auth slot runs it for every
	// Connect call (NewUserChain), and the attachment-bytes route calls its
	// AuthenticateHTTP directly.
	auth *interceptors.AuthInterceptor

	// projectService is exposed so callers can wire it to out-of-band
	// inputs (e.g. the daemon-events JetStream consumer in serverapi.Run).
	projectService *project.Service

	// chatService is exposed so serverapi.Run can hand undelivered messages
	// to it (queued delivery, the reconciler's wedge continuation).
	chatService *chat.Service

	// worktreeSweeper settles archived worktrees' directories through their
	// daemons.
	worktreeSweeper *worktreesweep.Sweeper
}

// apiService is one Connect service the API mounts: its proto service name
// (which the listener checks against its chain's audience), the service, and
// this mount's own options over the chain's.
type apiService struct {
	name  string
	svc   mountkit.Registrar
	mount []mountkit.Option
}

// Config holds the API's collaborators and settings.
//
// Every collaborator validate names is required: serverapi.Run builds each
// one or fails boot, so NewAPI refuses a Config without one rather than
// serve a surface with routes missing. A missing route answers 404, which the
// web client reports as version skew (web/src/api/rpcErrorPolicy.ts) — the
// wrong diagnosis for a composition bug.
type Config struct {
	// Auth is how callers authenticate: the auth mode, the shared API key or
	// the JWT key (PEM, else JWKS URL), and the email_verified switch.
	Auth auth.Settings
	// AuthorizationServer is the control plane that issues this deployment's
	// rlat_ tokens (RELIANT_AUTHORIZATION_SERVER): the issuer TokenService
	// names, and what CLI login discovery (RFC 8414) advertises. Empty on a
	// self-hosted server with no control plane.
	AuthorizationServer string
	// OAuthProvider is the provider the desktop sign-in
	// (SystemService.StartOAuthSignIn) logs in through.
	OAuthProvider auth.Provider
	// MaxUploadSize is the largest attachment accepted, in bytes; zero takes
	// AttachmentService's default.
	MaxUploadSize int64
	// WorktreeAutoArchive is what the worktree sweep does with finished
	// worktrees (RELIANT_WORKTREE_AUTO_ARCHIVE); see worktreesweep.
	WorktreeAutoArchive worktreesweep.AutoArchiveMode
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

	// ORM is the forge ORM client over Database's pool. The self-hosted
	// token store runs on it when TokenAuthority is nil.
	ORM orm.Context

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

	// InputOptions backs CatalogService.ListInputOptions. Optional: without it
	// the rpc answers Unimplemented.
	InputOptions inputoptions.Service

	// TriggerOptions checks, when an automation is enabled, that the values
	// picked for its integration inputs can be reached. Optional: without it
	// enabling checks nothing.
	TriggerOptions trigger.OptionsChecker

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
		return fmt.Errorf("api config: required collaborators not set: %s", strings.Join(missing, ", "))
	}
	return nil
}

// NewAPI builds every service and route the API serves; nothing is mounted
// until a listener registers it (Register). Returns an error if a required
// collaborator is missing (Config.validate) or a service or route fails to
// build.
func NewAPI(cfg *Config) (*API, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	database := cfg.Database

	// Create auth interceptor. The procedures it lets through unauthenticated
	// are the ones whose proto declares (forge.v1.method) auth_required: false;
	// forge generate projects them into middleware.UnauthenticatedProcedures.
	authInterceptor, err := interceptors.NewAuthInterceptor(cfg.Auth, middleware.UnauthenticatedProcedures)
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
		OAuthProvider: cfg.OAuthProvider,
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
	// Assigned only when set: a nil interface stored in a Deps field would
	// pass the handler's nil checks.
	var catalogInputOptions catalog.InputOptions
	if cfg.InputOptions != nil {
		catalogInputOptions = cfg.InputOptions
	}
	catalogService, err := catalog.New(catalog.Deps{
		Logger:             handlerLogger,
		Config:             handlerConfig,
		Tools:              cfg.ToolsFactory,
		TagPrefs:           database,
		LocalModels:        local.NewRepoDirectory(database),
		Search:             cfg.CatalogSearch,
		InputOptions:       catalogInputOptions,
		IntegrationMethods: catalog.IntegrationMethodsFrom(cfg.Connections),
	})
	if err != nil {
		return nil, fmt.Errorf("catalog service: %w", err)
	}
	// Only a deployment WITH a control plane can clone from GitHub on the
	// user's behalf; self-hosted leaves ControlPlane nil.
	var projectControlPlane project.CloneDispatcher
	if cfg.ControlPlane.Hosted() {
		projectControlPlane = controlplane.NewInstrumented(controlplane.Deps{BaseURL: cfg.ControlPlane.URL})
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
	// Config.WorktreeAutoArchive: "dry-run" (default) logs the active
	// worktrees its daemon reports finished (clean, merged or pushed, idle over
	// 24h, not in use) and archives nothing; read the finished= count in the
	// "[WorktreeSweep] auto-archive" line, then set "on" to archive them so the
	// ordinary removal reaches them; "off" never asks.
	worktreeSweeper := worktreesweep.New(database, router).
		WithKeepFiles(worktreesweep.KeepSetting{R: database}).
		WithAutoArchive(cfg.WorktreeAutoArchive, database,
			func(ctx context.Context, userID, projectID, worktreeID string) {
				if err := database.EmitUserRefetch(ctx, userID, db.RefetchWorktreeChanges, db.RefetchOpts{ProjectID: &projectID, WorktreeID: &worktreeID}); err != nil {
					logging.WarnContext(ctx, "worktree sweep: could not emit worktree refetch", "worktreeID", worktreeID, "error", err)
				}
			})
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
		Logger:                  handlerLogger,
		Config:                  handlerConfig,
		Repo:                    database,
		ControlPlane:            controlplane.NewInstrumented(controlplane.Deps{BaseURL: cfg.ControlPlane.URL}),
		ExchangeClaudeCode:      claude.ExchangeClaudeAuthorizationCode,
		ExchangeCodexCode:       codex.ExchangeCodexAuthorizationCode,
		ExchangeAntigravityCode: antigravity.ExchangeAntigravityAuthorizationCode,
		PollCopilotDevice:       copilot.PollDeviceAuthOnce,
		ValidateKey:             keyvalidate.Validate,
		ClaudeProfile: func(ctx context.Context, accessToken string) (providerusage.ClaudeAccountProfile, error) {
			return providerusage.ClaudeProfile(ctx, nil, accessToken)
		},
		GitHubIdentity: settings.FetchGitHubUser,
	})
	if err != nil {
		return nil, fmt.Errorf("settings service: %w", err)
	}
	providerAccountService, err := provider_account.New(provider_account.Deps{
		Logger:      handlerLogger,
		Config:      handlerConfig,
		Store:       database,
		ValidateKey: keyvalidate.Validate,
		// The same refreshing token source the worker's poller uses, so a refresh never races a live request.
		UsageRefresher: &accountlimits.Fetcher{Store: database, Tokens: drivers.RefreshingTokens{Store: database}},
	})
	if err != nil {
		return nil, fmt.Errorf("provider account service: %w", err)
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
		Logger:      handlerLogger,
		Config:      handlerConfig,
		Repo:        database,
		MaxFileSize: cfg.MaxUploadSize,
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
		gitTokens = gitcredentialclient.NewInstrumented(gitcredentialclient.Deps{
			BaseURL: cp.URL,
			Sign:    func() (string, error) { return auth.SignInternalServiceToken(secret) },
		})
	}
	tokenService, err := token.New(token.Deps{
		Logger:    handlerLogger,
		Config:    handlerConfig,
		Authority: authority,
		ControlPlane: token.TokenControlPlane{
			Issuer:      cfg.AuthorizationServer,
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

	// ForgeService reads a forge project's release/env/secret state off the
	// DAEMON's filesystem, through the router.
	//
	// It is always mounted. cfg.DisableForgeAPI, the operator's kill switch,
	// puts forgeAPIDisabled innermost in its chain instead (this mount's own
	// handler options run inside the listener's chain): every rpc then
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
	var forgeMount []mountkit.Option
	if cfg.DisableForgeAPI {
		logging.Info("forge API disabled in this deployment (RELIANT_DISABLE_FORGE_API): ForgeService answers FailedPrecondition")
		forgeMount = append(forgeMount, mountkit.WithHandlerOptions(connect.WithInterceptors(forgeAPIDisabled{})))
	}

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
	if cfg.TriggerOptions != nil {
		triggerDeps.Options = cfg.TriggerOptions
	}
	triggerDeps.Access = make(map[string]trigger.IntegrationAccess, len(in.Access))
	for id, a := range in.Access {
		triggerDeps.Access[id] = a
	}
	triggerDeps.Syncer, triggerDeps.Fires = trigger.TemporalBackend(cfg.TemporalClient, database, cfg.SharedTaskQueue, polled)
	if cfg.ControlPlane.Hosted() {
		triggerDeps.ControlPlane = controlplane.NewInstrumented(controlplane.Deps{BaseURL: cfg.ControlPlane.URL})
	}
	triggerService, err := trigger.New(triggerDeps)
	if err != nil {
		return nil, fmt.Errorf("trigger service: %w", err)
	}
	workflowService, err := workflowhandler.New(workflowhandler.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Repo:   database,
		Router: router,
		// The trigger service stops the schedules of automations deleted with a workflow.
		Automations: triggerService,
	})
	if err != nil {
		return nil, fmt.Errorf("workflow service: %w", err)
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

	bgProxy, err := background.New(background.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Router: router,
	})
	if err != nil {
		return nil, fmt.Errorf("background service: %w", err)
	}

	termProxy, err := terminal.New(terminal.Deps{
		Logger: handlerLogger,
		Config: handlerConfig,
		Router: router,
	})
	if err != nil {
		return nil, fmt.Errorf("terminal service: %w", err)
	}

	// WebSocket terminal handler (browser bidi terminal I/O)
	var wsValidator auth.TokenValidator
	switch cfg.Auth.ModeName() {
	case auth.ModeAPIKey:
		if key := cfg.Auth.APIKey; key != "" {
			wsValidator, _ = auth.NewAPIKeyValidator(key)
		}
	default: // supabase
		if cfg.Auth.PublicKeyPEM != "" {
			wsValidator, _ = auth.NewJWTValidator(cfg.Auth.PublicKeyPEM, cfg.Auth.JWTOptions()...)
		} else if cfg.Auth.JWKSURL != "" {
			wsValidator, _ = auth.LoadJWKS(context.Background(), cfg.Auth.JWKSURL, cfg.Auth.JWTOptions()...)
		}
	}
	// A terminal websocket lives as long as the browser tab: long-lived, so
	// the listener's drain ends it (with a reconnect close code) when it
	// begins.
	terminalWS := serverkit.LongLived(
		terminal.TerminalWSHandler(router, wsValidator, terminal.WithTerminalCheckouts(database)))

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
		accountControlPlane = controlplane.NewInstrumented(controlplane.Deps{BaseURL: cfg.ControlPlane.URL})
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

	// ConnectorService manages grants for third-party MCP clients, in the same
	// store the /mcp endpoint (connectorMCPRoutes) redeems them from.
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
	connectionService, err := connection.New(connection.Deps{
		Logger:      handlerLogger,
		Config:      handlerConfig,
		Connections: cfg.Connections,
	})
	if err != nil {
		return nil, fmt.Errorf("connection service: %w", err)
	}

	// The services whose requests wake an asleep machine (machinewake)
	// report the wake as Unavailable + DaemonWaking, whatever error they wrap
	// it in.
	machineWake := mountkit.WithHandlerOptions(connect.WithInterceptors(machinewake.NewInterceptor()))

	services := []apiService{
		{name: reliantv1connect.SystemServiceName, svc: systemService},
		{name: reliantv1connect.PlanServiceName, svc: planService},
		{name: reliantv1connect.TaskServiceName, svc: taskService},
		{name: reliantv1connect.CatalogServiceName, svc: catalogService},
		{name: reliantv1connect.ProjectServiceName, svc: projectService},
		{name: reliantv1connect.WorktreeServiceName, svc: worktreeService, mount: []mountkit.Option{machineWake}},
		{name: reliantv1connect.RepoServiceName, svc: repoService},
		{name: reliantv1connect.ApprovalServiceName, svc: approvalService},
		{name: reliantv1connect.QuestionServiceName, svc: questionService},
		{name: reliantv1connect.ChatServiceName, svc: chatService},
		// RunService is the engine's execution API. It delegates to
		// chatService rather than reimplementing anything, so mounting it
		// adds surface without adding behavior.
		{name: reliantv1connect.RunServiceName, svc: runService},
		{name: reliantv1connect.MessageServiceName, svc: messageService},
		{name: reliantv1connect.SettingsServiceName, svc: settingsService},
		{name: reliantv1connect.ProviderAccountServiceName, svc: providerAccountService},
		{name: reliantv1connect.MCPServiceName, svc: mcpService},
		{name: reliantv1connect.WorkflowServiceName, svc: workflowService},
		{name: reliantv1connect.ScenarioServiceName, svc: scenarioService},
		{name: reliantv1connect.FileSystemServiceName, svc: fileSystemService, mount: []mountkit.Option{machineWake}},
		{name: reliantv1connect.PackageCommandsServiceName, svc: packageCommandsService},
		{name: reliantv1connect.ToolCallServiceName, svc: toolCallService},
		{name: reliantv1connect.ForgeServiceName, svc: forgeService, mount: forgeMount},
		{name: reliantv1connect.StreamingServiceName, svc: streamingService},
		{name: reliantv1connect.AttachmentServiceName, svc: attachmentService},
		{name: reliantv1connect.PresetServiceName, svc: presetService},
		{name: reliantv1connect.TriggerServiceName, svc: triggerService},
		{name: reliantv1connect.InboxServiceName, svc: inboxService},
		// DaemonRegistryService: browser-driven list/get/resolve/resume. The
		// ToolsDaemonService streaming endpoint is hosted by the
		// daemon-gateway's DaemonListener, which authenticates the daemon's
		// `rlat_` (daemon_listener.go).
		{name: reliantv1connect.DaemonRegistryServiceName, svc: daemonRegistryService},
		{name: reliantv1connect.ModelEndpointServiceName, svc: modelEndpointService},
		{name: reliantv1connect.TokenServiceName, svc: tokenService},
		{name: reliantv1connect.DaemonServiceName, svc: daemonProxyService},
		{name: reliantv1connect.AccountServiceName, svc: accountService},
		{name: reliantv1connect.ConnectionServiceName, svc: connectionService},
		{name: reliantv1connect.ConnectorServiceName, svc: connectorService},
		{name: reliantv1connect.BackgroundServiceName, svc: bgProxy},
		{name: reliantv1connect.TerminalServiceName, svc: termProxy},
	}

	// The plain-HTTP routes. The CLI's api-token management and
	// workflow-trigger surfaces are Connect RPCs (TokenService,
	// ChatService.StartChat), not bespoke /api/v1 JSON handlers — the CLI
	// speaks the same authenticated Connect path the web app does.
	var routes []func(mux *http.ServeMux)
	handle := func(pattern string, h http.Handler) {
		routes = append(routes, func(mux *http.ServeMux) { mux.Handle(pattern, h) })
	}
	handle("/api/v2/terminal/ws", terminalWS)
	// Range-capable byte serving for <video>; Connect GetAttachment is whole-blob.
	handle(attachment.AttachmentContentPathPrefix, attachment.AttachmentContentHandler(database, authInterceptor.AuthenticateHTTP))
	cfg.OAuthRoutes.Register(handle)
	// Inbound trigger receivers: authenticated by the trigger's own token or
	// the provider's signature rather than a user's JWT.
	cfg.TriggerInbound.Register(handle)

	// Connector MCP endpoint: third-party MCP clients (ChatGPT, Claude, and
	// their mobile apps) driving a cloud daemon under a connector grant.
	//
	// Authenticated by an `rlat_` connector credential rather than the user
	// JWT the Connect handlers use, so it is a plain HTTP route outside the
	// chain. Confinement is enforced daemon-side at command dispatch
	// (internal/daemonpolicy); this route resolves the grant and terminates
	// the protocol.
	mountMCP, err := connectorMCPRoutes(database, cfg.ConnectorGrants, router, cfg.PublicURL, cfg.OAuthIssuers,
		connectorTokenValidator(cfg), accesstokenclient.NewCachedIntrospector(authority), cfg.ControlPlane.URL)
	if err != nil {
		return nil, fmt.Errorf("connector MCP endpoint: %w", err)
	}
	routes = append(routes, mountMCP)

	// CLI login discovery (RFC 8414): names the control plane that issues this
	// deployment's rlat_ tokens, so `reliant auth login --server <this>` finds
	// where to send the browser. Absent on a self-hosted server with no
	// control plane: it offers no browser login.
	if h := cliauth.MetadataHandler(cfg.AuthorizationServer); h != nil {
		handle(oauth2.AuthorizationServerMetadataPath, h)
	}

	// JSON health endpoint on the API listener so the frontend can discover
	// auth_mode without needing to reach the dedicated health port.
	authMode := cfg.Auth.ModeName()
	handle("/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":    "ok",
			"service":   "grpc-server",
			"auth_mode": authMode,
		})
	}))

	return &API{
		services:        services,
		http:            routes,
		auth:            authInterceptor,
		projectService:  projectService,
		chatService:     chatService,
		worktreeSweeper: worktreeSweeper,
	}, nil
}

// Register mounts every service on mux, each Connect handler built with opts
// (the listener's chain) followed by its own mount options, then the
// plain-HTTP routes; it returns the proto service names it mounted. It is
// serverkit.Routes, and a listener calls it once.
func (a *API) Register(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
	names := make([]string, 0, len(a.services))
	for _, s := range a.services {
		mountkit.RegisterService(mux, s.svc, opts, s.mount...)
		names = append(names, s.name)
	}
	for _, mount := range a.http {
		mount(mux)
	}
	return names
}

// Auth is the API's user authenticator, for the chain that serves it
// (UserChainDeps.Auth).
func (a *API) Auth() *interceptors.AuthInterceptor {
	return a.auth
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
func (a *API) ProjectService() *project.Service {
	return a.projectService
}

// ChatService returns the chat service, for out-of-band callers that start
// runs on a chat's behalf (serverapi.Run's queued delivery).
func (a *API) ChatService() *chat.Service {
	return a.chatService
}

// WorktreeSweeper returns the sweeper that settles archived worktrees.
func (a *API) WorktreeSweeper() *worktreesweep.Sweeper {
	return a.worktreeSweeper
}

// bindEventStreams marks MCP's server-to-client event streams long-lived: the
// streamable-HTTP transport's standalone GET stream and the legacy /sse
// stream are GETs that stay open for as long as the client is connected, so
// http.Server.Shutdown would wait on them forever. The drain ends them when
// it begins, and the client re-opens the stream against a live replica. POSTs
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

// connectorMCPRoutes builds the connector MCP endpoint and returns what mounts
// it: grants resolves each call's connector grant and OAuth client binding,
// and router carries the call to the grant's daemon.
func connectorMCPRoutes(
	database db.Repository,
	grants connectorgrant.Store,
	router toolexec.DaemonRouter,
	publicURL string,
	oauthIssuers []string,
	tokenValidator mcpserver.OAuthTokenValidator,
	credentials mcpserver.CredentialIntrospector,
	controlPlaneURL string,
) (func(mux *http.ServeMux), error) {
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
		return nil, err
	}
	handler = bindEventStreams(handler)

	return func(mux *http.ServeMux) {
		// Both the bare path and the subtree: MCP clients differ on whether
		// they append a trailing slash, and a 404 on the handshake is opaque
		// to debug from a phone.
		// The handler owns routing for both transports (streamable-HTTP at
		// /mcp, legacy SSE at /sse), so every path it serves is registered
		// here.
		mux.Handle(mcpserver.MountPath, handler)
		mux.Handle(mcpserver.MountPath+"/", handler)
		mux.Handle(mcpserver.SSEMountPath, handler)
		mux.Handle(mcpserver.SSEMountPath+"/", handler)

		// The origin root, so a connector configured with the bare base URL
		// handshakes instead of 404ing (see mcpserver.RootMountPath).
		//
		// Safe because this mux is the API server's own origin: it is
		// hostname-routed to reliant-api-server (deploy/kcl/*/ingress.k),
		// serves Connect RPCs under /reliant.v1.*, and has no root route of
		// its own — the web app is a separate service. The `{$}` pattern
		// matches only "/", so no RPC path or unknown-path 404 changes
		// behaviour.
		mux.Handle(mcpserver.RootMountPath, handler)
		logging.Info("connector MCP endpoint mounted", "path", mcpserver.MountPath)

		// RFC 9728 discovery, so a consumer MCP client can find the
		// authorization server from a failed request rather than being
		// configured by hand.
		mcpserver.MountOAuthRoutes(mux, oauthCfg, slog.Default())
	}, nil
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
	case cfg.Auth.PublicKeyPEM != "":
		validator, err = auth.NewJWTValidator(cfg.Auth.PublicKeyPEM, cfg.Auth.JWTOptions()...)
	case cfg.Auth.JWKSURL != "":
		validator, err = auth.LoadJWKS(context.Background(), cfg.Auth.JWKSURL, cfg.Auth.JWTOptions()...)
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
// dictates (the self-hosted store keeps its tokens through cfg.ORM). A
// misconfiguration (control-plane URL without its secret) fails server start
// loudly rather than serving a surface that can mint nothing.
func resolveTokenAuthority(cfg *Config) (tokenauthority.Authority, tokenauthority.Mode, error) {
	if cfg.TokenAuthority != nil {
		// An injected authority states no mode, so nothing that depends on
		// control-plane-side clipping is enabled for it.
		return cfg.TokenAuthority, "", nil
	}
	authority, mode, err := tokenauthority.New(tokenauthority.DepsFor(cfg.ControlPlane, cfg.ORM))
	if err != nil {
		return nil, "", fmt.Errorf("token authority: %w", err)
	}
	logging.Info("access-token authority selected", "mode", mode)
	return authority, mode, nil
}
