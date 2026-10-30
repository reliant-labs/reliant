// Copyright (c) 2025 Reliant Labs

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/serverkit"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/handlers/chat"
	"github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/credwire"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
	"github.com/reliant-labs/reliant/internal/integrations/ghusers"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/queueddelivery"
	"github.com/reliant-labs/reliant/internal/readguard"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/reliant-labs/reliant/internal/videojobs"
	"github.com/reliant-labs/reliant/internal/workersetup"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/reconciliation"
	scenariorunner "github.com/reliant-labs/reliant/internal/workflow/scenario/runner"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The api-server's providers (the api root, roots_api.go).
//
// The api-server is stateless: it serves every reliant.v1 service a browser,
// the CLI or an `rlat_` token calls, and runs the loops that converge what
// those services wrote (triggers, worktrees, queued messages, stuck runs). It
// executes no tools and holds no daemon stream: it reaches daemons through
// its own router, over NATS, and the daemon-gateway holds the streams.
//
// A node wired differently here than in the worker has a type of its own
// (APIDaemonRouter, APIToolsFactory, APICatalogSearch, APIDebugServer), and
// every node only this root may build is //forge:root-only api: the graph's
// identity is the type, so the worker can never be handed the api-server's
// variant, nor start a loop that runs only here.

// APISettings is the api-server's own config (ApiServerConfig) in the shapes
// its nodes take, read once. The api root lists it as its LAST field, so the
// JWT key file it reads is read before anything is dialled.
type APISettings struct {
	// Auth is how callers authenticate: the auth mode, the shared API key or
	// the JWT key (PEM — given directly or read from its file — else JWKS
	// URL), and the email_verified switch.
	Auth auth.Settings
	// AuthorizationServer is the control plane that issues this
	// deployment's rlat_ tokens (RELIANT_AUTHORIZATION_SERVER): the issuer
	// TokenService names, and what CLI login discovery (RFC 8414)
	// advertises. Empty on a self-hosted server with no control plane.
	AuthorizationServer string
	// OAuthProvider is the provider the desktop sign-in
	// (SystemService.StartOAuthSignIn) logs in through.
	OAuthProvider auth.Provider
	// WorktreeAutoArchive is what the worktree sweep does with finished
	// worktrees (RELIANT_WORKTREE_AUTO_ARCHIVE); see worktreesweep.
	WorktreeAutoArchive worktreesweep.AutoArchiveMode
	// PayloadGCHorizon is how long an unreferenced Temporal payload blob is
	// kept (RELIANT_PAYLOAD_BLOB_GC_HORIZON).
	PayloadGCHorizon time.Duration
}

// APISettingsDeps is the api-server's command config.
type APISettingsDeps struct {
	API *config.ApiServerConfig
}

// NewAPISettings reads the api-server's config. A value it cannot serve with
// never gets here: api_server_config.proto's rules refuse it as the config
// loads, before the process binds a port or applies a migration.
//
//forge:root-only api
func NewAPISettings(d APISettingsDeps) (*APISettings, error) {
	api := d.API
	// JWT public key: explicit value > file.
	jwtPublicKey := api.GetJwtPublicKey()
	if file := api.GetJwtPublicKeyFile(); jwtPublicKey == "" && file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to read JWT_PUBLIC_KEY_FILE %s: %w", file, err)
		}
		jwtPublicKey = string(data)
	}
	s := &APISettings{
		Auth: auth.Settings{
			Mode:                      api.GetAuthMode(),
			APIKey:                    api.GetAuthApiKey(),
			PublicKeyPEM:              jwtPublicKey,
			JWKSURL:                   api.GetJwksUrl(),
			RequireEmailVerifiedClaim: api.GetRequireEmailVerifiedClaim(),
		},
		AuthorizationServer: strings.TrimSpace(api.GetAuthorizationServer()),
		OAuthProvider:       auth.Provider{URL: api.GetAuthProviderUrl(), Key: api.GetAuthProviderKey()},
		WorktreeAutoArchive: worktreesweep.ParseAutoArchiveMode(api.GetWorktreeAutoArchive()),
		PayloadGCHorizon:    api.GetPayloadBlobGcHorizon().AsDuration(),
	}
	logging.Info("Starting Reliant API server (stateless)",
		"grpc_port", api.GetGrpcPort(),
		"auth_mode", s.Auth.ModeName(),
	)
	return s, nil
}

// UserAuthDeps is how callers authenticate, and the authority `rlat_` access
// tokens are resolved against.
type UserAuthDeps struct {
	Settings *APISettings
	Tokens   tokenauthority.Authority
}

// NewUserAuth authenticates the user audience: a JWT or the shared API key,
// and `rlat_` access tokens through the deployment's ONE token authority
// (control-plane when hosted, reliant's own store when self-hosted — never
// both) behind a CacheTTL-bounded cache. It admits the user audience, which
// every service the API mounts declares by declaring none: `forge lint
// --graph` and the listener's boot check prove the API listener serves
// nothing else.
//
//forge:audience user
//forge:root-only api
func NewUserAuth(d UserAuthDeps) (*grpcserver.UserAuth, error) {
	return grpcserver.NewUserAuth(d.Settings.Auth, d.Tokens)
}

// UserChainDeps is the user chain's authenticator and the email domains it
// admits.
type UserChainDeps struct {
	Auth                *grpcserver.UserAuth
	AllowedEmailDomains []string
	Logger              *slog.Logger
}

// NewUserChain is the API listener's interceptor chain (grpcserver.NewUserChain).
//
//forge:root-only api
func NewUserChain(d UserChainDeps) (*grpcserver.UserChain, error) {
	return grpcserver.NewUserChain(grpcserver.UserChainDeps{
		Auth:                d.Auth,
		AllowedEmailDomains: d.AllowedEmailDomains,
		Logger:              d.Logger,
	})
}

// HTTPRoutesDeps is what the API's plain-HTTP routes serve and
// authenticate with.
type HTTPRoutesDeps struct {
	Settings     *APISettings
	Auth         *grpcserver.UserAuth
	DB           *db.Repo
	Router       *APIDaemonRouter
	Connections  *APIConnections
	Inbound      *webhook.Inbound
	Grants       connectorgrant.Store
	Tokens       tokenauthority.Authority
	Integrations *IntegrationSettings
	OAuthIssuers []string
	ControlPlane *configv1.ControlPlaneConfig
}

// NewHTTPRoutes is the API's plain-HTTP routes (grpcserver.APIHTTPRoutes):
// the terminal websocket, attachment bytes, the OAuth broker's callbacks, the
// inbound-trigger receivers, the connector MCP endpoint (PUBLIC_URL is where
// a user points a third-party MCP client; MCP_OAUTH_ISSUERS the authorization
// servers whose tokens it accepts), CLI login discovery and the JSON /health
// the web client reads the auth mode from.
//
//forge:root-only api
func NewHTTPRoutes(d HTTPRoutesDeps) (*grpcserver.APIHTTPRoutes, error) {
	cp := ControlPlaneEndpoint(d.ControlPlane)
	return grpcserver.NewAPIHTTPRoutes(grpcserver.APIHTTPRoutesDeps{
		Auth:                d.Auth,
		Repo:                d.DB,
		Router:              d.Router.NATSDaemonRouter,
		OAuth:               d.Connections.OAuth,
		Inbound:             d.Inbound,
		Grants:              d.Grants,
		Tokens:              d.Tokens,
		PublicURL:           d.Integrations.PublicURL,
		OAuthIssuers:        d.OAuthIssuers,
		AuthorizationServer: d.Settings.AuthorizationServer,
		ControlPlaneURL:     cp.URL,
		HostedHTTPRedeem:    grpcserver.NewHostedHTTPRedeemHandler(d.DB, cp.InternalServiceSecret),
	})
}

// APIListenerDeps is where the API listens and what it serves there.
type APIListenerDeps struct {
	API    *config.ApiServerConfig
	TLS    *TLSFiles
	Chain  *grpcserver.UserChain
	Routes *UserRoutes
	HTTP   *grpcserver.APIHTTPRoutes
	Logger *slog.Logger
}

// NewAPIListener is the address browsers, the CLI and `rlat_` tokens call
// (GRPC_PORT on BIND_ADDRESS): the user-audience route set through the user
// chain, and the plain-HTTP routes beside it. Its drain ends every long-lived
// stream as it begins and waits for the unary calls in flight
// (grpcserver.NewAPIListener).
//
//forge:root-only api
func NewAPIListener(d APIListenerDeps) (*grpcserver.APIListener, error) {
	return grpcserver.NewAPIListener(grpcserver.APIListenerDeps{
		Port:               int(d.API.GetGrpcPort()),
		BindAddress:        d.API.GetBindAddress(),
		TLSCertFile:        d.TLS.CertFile,
		TLSKeyFile:         d.TLS.KeyFile,
		CORSAllowedOrigins: d.API.GetCorsAllowedOrigins(),
		Chain:              d.Chain,
		Routes: serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
			services := d.Routes.Register(mux, opts...)
			d.HTTP.Register(mux)
			return services
		}),
		Logger: d.Logger,
	})
}

// APIDebugServer is the api-server's localhost-only diagnostics listener on
// PPROF_PORT (DebugServer), the worker's twin with the api's own port.
type APIDebugServer struct{ *DebugServer }

// APIDebugServerDeps is the diagnostics port (PPROF_PORT).
type APIDebugServerDeps struct {
	Port int32
}

// NewAPIDebugServer serves pprof, the DB write-queue gauges and metrics on
// 127.0.0.1:PPROF_PORT. PPROF_PORT=0 serves none: the node is absent.
//
//forge:root-only api
func NewAPIDebugServer(d APIDebugServerDeps) (*APIDebugServer, error) {
	s, err := NewDebugServer(DebugServerDeps(d))
	if err != nil || s == nil {
		return nil, err
	}
	return &APIDebugServer{s}, nil
}

// APIDaemonRouter is the api-server's daemon router: the browser-facing
// workspace services, the connector MCP endpoint and the loops reach the
// user's daemons through it, over NATS. Unlike the worker's it never acts
// without a user: it has no delegated-credential fallback.
type APIDaemonRouter struct{ *toolexec.NATSDaemonRouter }

// APIDaemonRouterDeps is the bus the router routes over, the database the
// daemon records come from, and the control plane that wakes machines.
type APIDaemonRouterDeps struct {
	NATS         *NATSConn
	DB           *db.Repo
	ControlPlane *configv1.ControlPlaneConfig
}

// NewAPIDaemonRouter builds the api-server's router (daemonRouterOptions).
//
//forge:root-only api
func NewAPIDaemonRouter(d APIDaemonRouterDeps) (*APIDaemonRouter, error) {
	router := toolexec.NewNATSDaemonRouter(d.NATS.Conn, daemonRouterOptions(d.DB, d.ControlPlane)...)
	logging.Info("Using NATS daemon router — daemon services run in separate daemon-gateway process")
	return &APIDaemonRouter{router}, nil
}

// Close closes the router, after every node that routes through it.
func (r *APIDaemonRouter) Close(context.Context) error { return r.NATSDaemonRouter.Close() }

// daemonRouterOptions is the api-server's daemon-router wiring.
//
// Daemon records are this process's own: the router resolves them — including
// the "provisioning" / "suspended" lifecycle that makes ErrDaemonPending — from
// repo, the same tables DaemonRegistryService serves
// (docs/design/one-daemon-list.md). It never asks the control plane for them;
// control-plane deleted its reliant.v1.DaemonRegistryService adapter so that
// exactly one host answers that path.
//
// The control plane is wired only for what it alone can do: resume a
// suspended managed machine, via controlplane.v1.DaemonService/ResumeDaemon,
// authenticated as the signed-in user. cp names no url when no control plane
// is configured, and then a suspended daemon is reported pending, never woken.
func daemonRouterOptions(repo toolexec.DaemonRecords, cp *configv1.ControlPlaneConfig) []toolexec.NATSRouterOption {
	opts := []toolexec.NATSRouterOption{toolexec.WithDatabase(repo)}
	if ControlPlaneEndpoint(cp).Hosted() {
		opts = append(opts, toolexec.WithDaemonResumer(controlplane.NewInstrumented(controlplane.Deps{ControlPlane: cp})))
	}
	return opts
}

// APIToolsFactoryDeps is what the catalog's tool metadata reads.
type APIToolsFactoryDeps struct {
	DB       *db.Repo
	Network  *configv1.NetworkConfig
	Temporal *TemporalClient
}

// NewAPIToolsFactory is the tools factory CatalogService lists tools from:
// catalog metadata only — the api-server executes no tools.
//
//forge:root-only api
func NewAPIToolsFactory(d APIToolsFactoryDeps) (*tools.ToolsFactory, error) {
	repo, temporalClient := d.DB, d.Temporal.Client
	return tools.NewToolsFactory(&tools.ToolsOptions{
		Repo:               repo,
		AllowPrivateEgress: AllowPrivateEgress(d.Network),
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
	}), nil
}

// APIConnections is what the api-server serves connections with.
type APIConnections struct {
	// Service backs ConnectionService.
	Service *connections.Service
	// OAuth is the OAuth broker's browser callback.
	OAuth *connections.OAuthHTTP
	// Tokens opens a connection's stored credential for its owner. The
	// inbound receiver verifies Twilio deliveries with it (each account
	// signs with its own Auth Token), and the input-option service resolves
	// a caller's connection through it.
	Tokens *connections.TokenSource
}

// APIConnectionsDeps is where connections and their secrets live, which
// OAuth clients this deployment has, and the origins a browser OAuth flow is
// relayed back to.
type APIConnectionsDeps struct {
	DB           *db.Repo
	Vault        vault.Service
	Integrations *IntegrationSettings
	AppOrigins   []string
	ControlPlane *configv1.ControlPlaneConfig
}

// NewAPIConnections builds the connection service, the token source and the
// OAuth broker's HTTP callback. A provider whose client credentials are unset
// is listed as unavailable; it never fails boot.
//
// AppOrigins is the deployment's CORS allow-list: the origins the web app is
// served from, which are the only ones (besides PUBLIC_URL's and loopback) a
// browser OAuth flow is relayed back to.
//
//forge:root-only api
func NewAPIConnections(d APIConnectionsDeps) (*APIConnections, error) {
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), d.Integrations.Webhooks.OAuthClients)
	if err != nil {
		return nil, fmt.Errorf("connections: %w", err)
	}
	store := d.DB.Connections()
	tokens := connections.NewTokenSource(store, d.Vault, providers, nil)
	broker := connections.NewBroker(store, d.Vault, providers, nil, d.Integrations.PublicURL).WithAppOrigins(d.AppOrigins)
	svc := connections.NewService(store, d.Vault, providers, tokens, broker, nil).
		WithDelegated(delegatedAvailable(ControlPlaneEndpoint(d.ControlPlane).Hosted()))
	for _, in := range svc.ListIntegrations() {
		for _, m := range in.Methods {
			if !m.Available {
				logging.Info("connection method unavailable", "integration", in.ID, "method", m.Kind, "reason", m.Reason)
			}
		}
	}
	return &APIConnections{Service: svc, OAuth: connections.NewOAuthHTTP(broker), Tokens: tokens}, nil
}

// APICatalogSearch is the integration catalog search CatalogService answers
// SearchCatalog / GetCatalogEntry from.
type APICatalogSearch struct{ *catalogindex.Service }

// APICatalogSearchDeps is the connections it reports on, and whether a
// control plane hosts this deployment.
type APICatalogSearchDeps struct {
	DB           *db.Repo
	ControlPlane *configv1.ControlPlaneConfig
}

// NewAPICatalogSearch builds the search: the embedded catalog's index (built
// once, shared) plus each caller's connections, with delegated brokers judged
// by the same rule ListIntegrations uses.
//
//forge:root-only api
func NewAPICatalogSearch(d APICatalogSearchDeps) (*APICatalogSearch, error) {
	idx, err := catalogindex.Builtin()
	if err != nil {
		return nil, fmt.Errorf("integration catalog index: %w", err)
	}
	delegated := delegatedAvailable(ControlPlaneEndpoint(d.ControlPlane).Hosted())
	return &APICatalogSearch{catalogindex.NewService(idx, d.DB.Connections(), catalogindex.DelegatedAvailable(delegated))}, nil
}

// delegatedAvailable reports which delegated brokers the worker registers.
// The api-server holds no brokers (credentials resolve on the worker), so it
// mirrors the worker's rule: the control-plane GitHub broker exists exactly
// when a control plane is configured (NewWorkerIntegrationCredentials).
func delegatedAvailable(hosted bool) connections.DelegatedAvailable {
	return func(brokerID string) (bool, string) {
		if brokerID == ghdelegated.BrokerID {
			if hosted {
				return true, ""
			}
			return false, "no control plane is configured (RELIANT_CONTROL_PLANE_URL), so GitHub cannot be delegated to it"
		}
		return false, "delegated broker " + brokerID + " is not registered on this deployment"
	}
}

// InputOptionsDeps is the database, the connection secrets a caller's
// connection is opened with, and the control plane credentials are delegated
// to.
type InputOptionsDeps struct {
	DB           *db.Repo
	Connections  *APIConnections
	ControlPlane *configv1.ControlPlaneConfig
}

// NewInputOptions builds the service behind CatalogService.ListInputOptions
// and enable-time reachability checks: an integration type's list action run
// as the signed-in user, through the same credential rules a run's actions
// follow (credwire), with the shipped catalog's types.
//
// The API server resolves the user's connection itself rather than asking the
// worker: a picker is a read the user is waiting on, and the resolver only
// needs the database and the vault key this process already holds.
//
//forge:root-only api
func NewInputOptions(d InputOptionsDeps) (inputoptions.Service, error) {
	idx, err := catalogindex.Builtin()
	if err != nil {
		return nil, fmt.Errorf("input options: %w", err)
	}
	repo := d.DB
	creds, err := credwire.New(connections.NewResolver(repo, repo.Connections(), d.Connections.Tokens), d.ControlPlane)
	if err != nil {
		return nil, err
	}
	return inputoptions.NewInstrumented(inputoptions.Deps{
		Types:       idx,
		Runner:      httpaction.NewRunner(netguard.New()),
		Credentials: creds,
	})
}

// optionsChecker answers trigger.OptionsChecker from the option service, so
// enabling an automation asks the integration the same question its picker did.
//
// Only a definite answer refuses an enable. A value the integration cannot
// reach for this owner is Unreachable; one the acting identity cannot write to
// is a warning. Anything that stops the question being answered (the provider
// down or rate limited, no usable connection) is an error, which the trigger
// service turns into a warning: a check that cannot be made is not evidence
// about the value.
type optionsChecker struct{ svc inputoptions.Service }

func (c optionsChecker) CheckValues(ctx context.Context, check trigger.ValueCheck) (trigger.ValueCheckResult, error) {
	res, err := c.svc.Resolve(ctx, inputoptions.ResolveRequest{
		UserID: check.UserID, Type: check.Type, ConnectionID: check.ConnectionID, Scope: check.Scope,
		Values: check.Values, ActingIdentity: string(check.ActingIdentity),
	})
	if err != nil {
		var upstream *inputoptions.UpstreamError
		switch {
		case errors.As(err, &upstream):
			return trigger.ValueCheckResult{}, errors.New(upstream.Message)
		case errors.Is(err, inputoptions.ErrUnknownType), errors.Is(err, inputoptions.ErrNoOptions):
			// A type with nothing to check against has nothing to refuse.
			return trigger.ValueCheckResult{}, nil
		}
		return trigger.ValueCheckResult{}, errors.New(inputoptions.Message(err))
	}
	if res.State == inputoptions.StateNeedsConnection {
		return trigger.ValueCheckResult{}, errors.New("no usable connection to check against")
	}
	out := trigger.ValueCheckResult{Unreachable: res.Unreachable}
	for _, o := range res.Options {
		for _, w := range o.Warnings {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s: %s", o.Label, strings.TrimSpace(w)))
		}
	}
	return out, nil
}

// ConnectorGrantsDeps is the database connector grants live in.
type ConnectorGrantsDeps struct {
	DB *db.Repo
}

// NewConnectorGrants stores connector grants, their audit log and the OAuth
// client bindings: ConnectorService manages them and the /mcp endpoint
// resolves every call through them.
//
//forge:root-only api
func NewConnectorGrants(d ConnectorGrantsDeps) (connectorgrant.Store, error) {
	return connectorgrant.NewSQLStore(connectorgrant.Deps{DB: d.DB.ORM()}), nil
}

// GitHubAccessDeps is where an owner's GitHub token comes from: the control
// plane when hosted, the owner's saved connection otherwise.
type GitHubAccessDeps struct {
	DB           *db.Repo
	Vault        vault.Service
	ControlPlane *configv1.ControlPlaneConfig
	Integrations *IntegrationSettings
}

// NewGitHubAccess builds the refresher that keeps GitHub trigger owners'
// repository access fresh — GitHub events are access-gated — or nothing
// (nil) when this deployment receives no GitHub webhooks
// (RELIANT_GITHUB_WEBHOOK_SECRET unset), since nothing routes then.
//
// The user's token comes from control-plane when one is configured (hosted:
// the one GitHub App's user tokens live there), and from the user's saved
// GitHub connection otherwise (self-hosted, reliant's own App). A
// control-plane URL without INTERNAL_SERVICE_SECRET is a boot error, never a
// silent fall-back: hosted users have no saved connection, so falling back
// would refuse every GitHub trigger as "not connected".
//
//forge:root-only api
func NewGitHubAccess(d GitHubAccessDeps) (*ghaccess.Refresher, error) {
	return wireGitHubAccess(d.DB, d.Vault, d.ControlPlane, d.Integrations.Webhooks)
}

// gitHubAccess adapts *ghaccess.Refresher to trigger.IntegrationAccess.
type gitHubAccess struct{ *ghaccess.Refresher }

func (gitHubAccess) IsPermanent(err error) bool { return ghaccess.IsPermanent(err) }

// TriggerSenders is the people lookup behind a trigger's "Only from", keyed by
// integration id ("github"): who a user id or a login is, as the caller.
type TriggerSenders map[string]trigger.SenderDirectory

// TriggerSendersDeps is where a caller's GitHub token comes from, as for
// GitHubAccessDeps.
type TriggerSendersDeps struct {
	DB           *db.Repo
	Vault        vault.Service
	ControlPlane *configv1.ControlPlaneConfig
	Integrations *IntegrationSettings
}

// NewTriggerSenders builds the GitHub people lookup, or none under the same
// condition as NewGitHubAccess: a deployment that receives no GitHub webhooks
// has no GitHub triggers to restrict.
//
//forge:root-only api
func NewTriggerSenders(d TriggerSendersDeps) (TriggerSenders, error) {
	return wireGitHubSenders(d.DB, d.Vault, d.ControlPlane, d.Integrations.Webhooks)
}

// gitHubSenders adapts ghusers.Service to trigger.SenderDirectory. It
// asks GitHub with the same token source the access refresher uses: the
// caller's own GitHub token, delegated by control-plane when hosted.
type gitHubSenders struct{ dir ghusers.Service }

func (g gitHubSenders) Resolve(ctx context.Context, userID string, handles, ids []string) ([]trigger.ResolvedSender, error) {
	found, err := g.dir.Resolve(ctx, userID, handles, ids)
	if err != nil {
		return nil, err
	}
	out := make([]trigger.ResolvedSender, 0, len(found))
	for _, f := range found {
		out = append(out, trigger.ResolvedSender{Query: f.Query, ID: f.User.ID, DisplayName: f.User.Login})
	}
	return out, nil
}

func (gitHubSenders) IsPermanent(err error) bool {
	return ghaccess.IsPermanent(err) || errors.Is(err, ghusers.ErrTokenRejected)
}

func (gitHubSenders) IsInvalid(err error) bool { return errors.Is(err, ghusers.ErrTooManyQueries) }

// wireGitHubSenders is NewTriggerSenders over its parts.
func wireGitHubSenders(repo *db.Repo, keys vault.Service, cp *configv1.ControlPlaneConfig, hooks webhook.RegistryConfig) (TriggerSenders, error) {
	if strings.TrimSpace(hooks.GitHubWebhookSecret) == "" {
		return nil, nil
	}
	tokens, err := gitHubTokenSource(repo, keys, cp, hooks.OAuthClients)
	if err != nil {
		return nil, err
	}
	return TriggerSenders{
		ghusers.IntegrationID: gitHubSenders{dir: ghusers.NewInstrumented(ghusers.Deps{Tokens: tokens})},
	}, nil
}

// wireGitHubAccess is NewGitHubAccess over its parts.
func wireGitHubAccess(repo *db.Repo, keys vault.Service, cp *configv1.ControlPlaneConfig, hooks webhook.RegistryConfig) (*ghaccess.Refresher, error) {
	if strings.TrimSpace(hooks.GitHubWebhookSecret) == "" {
		return nil, nil
	}
	tokens, err := gitHubTokenSource(repo, keys, cp, hooks.OAuthClients)
	if err != nil {
		return nil, err
	}
	return ghaccess.New(repo, tokens, ghaccess.Options{
		Logger: slog.Default().With("component", "ghaccess"),
	}), nil
}

func gitHubTokenSource(repo *db.Repo, keys vault.Service, cfg *configv1.ControlPlaneConfig, clients connections.OAuthClients) (ghaccess.TokenSource, error) {
	if cp := ControlPlaneEndpoint(cfg); cp.Hosted() {
		secret := cp.InternalServiceSecret
		if secret == "" {
			return nil, errors.New("github triggers: RELIANT_CONTROL_PLANE_URL is set but INTERNAL_SERVICE_SECRET is not; " +
				"hosted GitHub tokens come from control-plane and cannot be fetched unauthenticated")
		}
		return ghaccess.NewDelegatedTokens(gitcredentialclient.NewInstrumented(gitcredentialclient.Deps{
			ControlPlane: cfg,
			Sign:         func() (string, error) { return auth.SignInternalServiceToken(secret) },
		})), nil
	}
	if repo == nil {
		return nil, errors.New("github triggers: no database for saved GitHub connections")
	}
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), clients)
	if err != nil {
		return nil, fmt.Errorf("github triggers: %w", err)
	}
	store := repo.Connections()
	return ghaccess.NewSavedTokens(store, connections.NewTokenSource(store, keys, providers, nil)), nil
}

// GitHubAccessLoop refreshes each GitHub trigger owner's repository access
// while a trigger exists. Every replica runs it; leases share the work.
type GitHubAccessLoop struct{ refresher *ghaccess.Refresher }

// GitHubAccessLoopDeps is the refresher the loop runs.
type GitHubAccessLoopDeps struct {
	// nil without RELIANT_GITHUB_WEBHOOK_SECRET.
	// forge:optional-dep
	Access *ghaccess.Refresher
}

// NewGitHubAccessLoop is the refresh loop, or nothing (nil) when this
// deployment receives no GitHub events.
//
//forge:root-only api
func NewGitHubAccessLoop(d GitHubAccessLoopDeps) (*GitHubAccessLoop, error) {
	if d.Access == nil {
		return nil, nil
	}
	return &GitHubAccessLoop{refresher: d.Access}, nil
}

// Run refreshes until ctx ends.
func (l *GitHubAccessLoop) Run(ctx context.Context) error {
	l.refresher.Run(ctx)
	return nil
}

// TriggerAlertingDeps is the control plane that provisions alert receivers,
// and the URL its Alertmanager delivers to.
type TriggerAlertingDeps struct {
	DB           *db.Repo
	ControlPlane *configv1.ControlPlaneConfig
	Integrations *IntegrationSettings
}

// NewTriggerAlerting builds the provisioner of `source: alerting` webhook
// triggers' alert receivers, or nothing (nil) without a control plane: those
// triggers are then plain webhooks. Webhook triggers declared `source:
// alerting` are delivered to by the control plane's alerting: TriggerService
// provisions their receivers and the trigger reconciler sweeps them. The calls
// are internal-service signed: the sweep runs with no caller, and a resumed
// trigger's owner may have no live session.
//
//forge:root-only api
func NewTriggerAlerting(d TriggerAlertingDeps) (*triggers.Alerting, error) {
	return wireTriggerAlerting(d.DB, d.ControlPlane, d.Integrations.PublicURL), nil
}

// wireTriggerAlerting is NewTriggerAlerting over its parts.
func wireTriggerAlerting(repo *db.Repo, cfg *configv1.ControlPlaneConfig, publicURL string) *triggers.Alerting {
	cp := ControlPlaneEndpoint(cfg)
	if !cp.Hosted() {
		return nil
	}
	secret := cp.InternalServiceSecret
	if secret == "" {
		logging.Warn("on-call automations are plain webhooks here: RELIANT_CONTROL_PLANE_URL is set but INTERNAL_SERVICE_SECRET is not")
		return nil
	}
	if strings.TrimSpace(publicURL) == "" {
		// The control plane's Alertmanager needs an absolute URL to post to;
		// a bare /hooks path would be refused at every enable.
		logging.Warn("on-call automations are plain webhooks here: PUBLIC_URL is not set, so Alertmanager has no URL to deliver to")
		return nil
	}
	receivers := controlplane.NewInstrumented(controlplane.Deps{
		ControlPlane: cfg,
		Sign:         func() (string, error) { return auth.SignInternalServiceToken(secret) },
	})
	return triggers.NewAlerting(receivers, repo, publicURL)
}

// TriggerInboundDeps is what the inbound receivers record events in, start
// fires through, and verify deliveries with.
type TriggerInboundDeps struct {
	DB           *db.Repo
	Temporal     *TemporalClient
	Pollers      *webhook.Registry
	Vault        vault.Service
	Integrations *IntegrationSettings
	Connections  *APIConnections
	// nil without RELIANT_GITHUB_WEBHOOK_SECRET.
	// forge:optional-dep
	GitHubAccess *ghaccess.Refresher
}

// NewTriggerInbound builds the inbound-trigger receivers: they record an
// event and start its fire on the worker; launching never happens here. Who
// caused an event is resolved against the write ledger before any filter
// reads it. GitHub events are access-gated: each owner's repository access is
// refreshed when a trigger is activated (and by GitHubAccessLoop while one
// exists).
//
//forge:root-only api
func NewTriggerInbound(d TriggerInboundDeps) (*webhook.Inbound, error) {
	repo := d.DB
	in := webhook.NewInbound(repo,
		triggers.NewIntake(repo, d.Temporal.Client, v2workflow.SharedTaskQueue).
			WithWorkflows(triggers.LaunchWorkflows{Repo: repo}).
			WithPoolCatalogs(machinepool.Catalogs{R: repo}).
			WithEnrichers(triggers.NewOriginEnricher(repo.IntegrationWrites(), repo, d.Integrations.GitHubAppSlug)),
		d.Pollers, d.Vault, d.Integrations.PublicURL).
		WithConnectionSecrets(d.Connections.Tokens)
	if d.GitHubAccess != nil {
		in.Access = map[string]webhook.AccessRefresher{ghaccess.IntegrationID: gitHubAccess{d.GitHubAccess}}
	}
	return in, nil
}

// ResetGuardDeps is the pause service whose resumes count against the guard.
type ResetGuardDeps struct {
	Pause *v2workflow.PauseService
}

// NewResetGuard is the shared reset-attempt guard: it bounds reset-and-replay
// attempts per workflow so a deterministically-failing run is not reset
// forever. The user-driven resume path (the pause service, on which it is
// installed here) and the reconciler's automatic stuck-task recovery share
// it, so both count against one per-workflow bound. The worker's pause
// service has none: it resets nothing the reconciler also resets.
//
//forge:root-only api
func NewResetGuard(d ResetGuardDeps) (*v2workflow.ResetAttemptGuard, error) {
	guard := v2workflow.NewResetAttemptGuard(v2workflow.DefaultMaxResetAttempts)
	d.Pause.SetResetGuard(guard)
	return guard, nil
}

// QueuedDelivererDeps is where queued messages are kept, the machines they
// wait for, and what delivers and wakes them.
type QueuedDelivererDeps struct {
	DB       *db.Repo
	Router   *APIDaemonRouter
	Chat     *chat.Service
	Temporal *TemporalClient
}

// NewQueuedDeliverer delivers messages sent while a chat's machine was down:
// it wakes the runs waiting for it when one of the user's machines connects,
// and delivers the ones whose run already ended. The reconciler sweeps for
// connects it misses and continues the message of a run it ends as wedged.
//
//forge:root-only api
func NewQueuedDeliverer(d QueuedDelivererDeps) (queueddelivery.Service, error) {
	return queueddelivery.NewInstrumented(queueddelivery.Deps{
		Store:     d.DB,
		Machines:  d.Router.NATSDaemonRouter,
		Deliverer: d.Chat,
		Signaler:  d.Temporal.Client,
	}), nil
}

// QueuedDelivery consumes machine connects for the queued deliverer.
type QueuedDelivery struct {
	deliverer queueddelivery.Service
	nats      *NATSConn
}

// QueuedDeliveryDeps is the deliverer and the bus connects arrive on.
type QueuedDeliveryDeps struct {
	Deliverer queueddelivery.Service
	NATS      *NATSConn
}

// NewQueuedDelivery is the queued-delivery consumer.
//
//forge:root-only api
func NewQueuedDelivery(d QueuedDeliveryDeps) (*QueuedDelivery, error) {
	return &QueuedDelivery{deliverer: d.Deliverer, nats: d.NATS}, nil
}

// Run consumes until ctx ends. A consumer that exits early is logged, not
// fatal: the reconciler's sweep still delivers.
func (q *QueuedDelivery) Run(ctx context.Context) error {
	if err := q.deliverer.Run(ctx, q.nats.Conn); err != nil && ctx.Err() == nil {
		logging.WarnContext(ctx, "queued-delivery consumer exited with error; the reconciler's sweep still delivers", "error", err)
	}
	return nil
}

// DaemonEventsConsumer heals each user's project directories the moment
// their workspace daemon comes online, from daemon.v1.events.connected on the
// DAEMON_EVENTS JetStream stream. The api-server never sees daemon
// connections itself — daemons connect to the daemon-gateway, which publishes
// the events.
type DaemonEventsConsumer struct {
	projects *project.Service
	nats     *NATSConn
}

// DaemonEventsConsumerDeps is the project service and the bus.
type DaemonEventsConsumerDeps struct {
	Projects *project.Service
	NATS     *NATSConn
}

// NewDaemonEventsConsumer is the consumer.
//
//forge:root-only api
func NewDaemonEventsConsumer(d DaemonEventsConsumerDeps) (*DaemonEventsConsumer, error) {
	return &DaemonEventsConsumer{projects: d.Projects, nats: d.NATS}, nil
}

// Run consumes until ctx ends; an early exit is logged, not fatal.
func (c *DaemonEventsConsumer) Run(ctx context.Context) error {
	if err := c.projects.StartDaemonEventConsumer(ctx, c.nats.Conn); err != nil && ctx.Err() == nil {
		logging.WarnContext(ctx, "daemon-events consumer exited with error", "error", err)
	}
	return nil
}

// WorktreeSweepDeps is the database, the router the daemons are asked
// through, and what the sweep does with finished worktrees.
type WorktreeSweepDeps struct {
	ControlPlane *configv1.ControlPlaneConfig
	DB           *db.Repo
	Router       *APIDaemonRouter
	Settings     *APISettings
}

// NewWorktreeSweep settles archived worktrees' directories through their
// daemons: WorktreeService and InboxService settle one at a time through it,
// and the WorktreeSweeper loop sweeps them all.
//
// RELIANT_WORKTREE_AUTO_ARCHIVE: "dry-run" (default) observes candidates but
// does not enroll dirty/unpushed work in Git recovery; "on" admits the Git-only
// preservation/finalization pipeline after seven days of complete inactivity
// evidence. Every capture still requires its own eligibility/secret gates and
// independent remote proof. "off" disables automatic enrollment.
//
//forge:root-only api
func NewWorktreeSweep(d WorktreeSweepDeps) (*worktreesweep.Sweeper, error) {
	database := d.DB
	credentials, err := gitTokens(d.ControlPlane)
	if err != nil {
		return nil, err
	}
	return worktreesweep.New(database, d.Router.NATSDaemonRouter).
		// The existing explicit rollout setting gates Git recovery too. The
		// gate does not bypass any per-workspace or remote verification check.
		WithGitRecovery(database, recoveryCredentials{client: credentials}, d.Settings.WorktreeAutoArchive == worktreesweep.AutoArchiveOn).
		WithKeepFiles(worktreesweep.KeepSetting{R: database}).
		WithAutoArchive(d.Settings.WorktreeAutoArchive, database,
			func(ctx context.Context, userID, projectID, worktreeID string) {
				if err := database.EmitUserRefetch(ctx, userID, db.RefetchWorktreeChanges, db.RefetchOpts{ProjectID: &projectID, WorktreeID: &worktreeID}); err != nil {
					logging.WarnContext(ctx, "worktree sweep: could not emit worktree refetch", "worktreeID", worktreeID, "error", err)
				}
			}), nil
}

// worktreeSweepInterval is how often the server asks each daemon to settle its
// archived worktrees. Archiving already attempts removal immediately, so this
// only catches worktrees the daemon was offline or busy for.
const worktreeSweepInterval = 10 * time.Minute

// WorktreeSweeper settles archived worktrees' directories on their daemons
// every worktreeSweepInterval: removes what is provably safe, records what is
// held, and locks live worktrees as reliant-owned. Every replica runs it; a
// daemon's answers are idempotent.
type WorktreeSweeper struct{ sweep *worktreesweep.Sweeper }

// WorktreeSweeperDeps is the sweep the loop runs.
type WorktreeSweeperDeps struct {
	Sweep *worktreesweep.Sweeper
}

// NewWorktreeSweeper is the loop.
//
//forge:root-only api
func NewWorktreeSweeper(d WorktreeSweeperDeps) (*WorktreeSweeper, error) {
	return &WorktreeSweeper{sweep: d.Sweep}, nil
}

// Run sweeps until ctx ends.
func (w *WorktreeSweeper) Run(ctx context.Context) error {
	w.sweep.Run(ctx, worktreeSweepInterval)
	return nil
}

// ScheduleSyncerDeps is the Temporal schedules, the triggers table, and the
// integrations polled on a schedule.
type ScheduleSyncerDeps struct {
	Temporal *TemporalClient
	DB       *db.Repo
	Pollers  *webhook.Registry
}

// NewScheduleSyncer converges a trigger's Temporal Schedule onto its row.
//
//forge:root-only api
func NewScheduleSyncer(d ScheduleSyncerDeps) (*triggers.Syncer, error) {
	return triggers.NewSyncer(d.Temporal.Client.ScheduleClient(), d.DB, v2workflow.SharedTaskQueue).
		WithPolledIntegrations(d.Pollers.IsPolled), nil
}

// TriggerScheduleSync converges every trigger's Temporal Schedule onto its
// row once, at startup, and drops schedules whose row is gone. The DB is the
// truth, so this repairs drift left by a write that landed while Temporal was
// unreachable — and an orphan schedule keeps firing for a trigger nobody can
// see or stop. Once, with backoff, beside the listener: it races a cold
// Temporal, and refusing to serve until schedules converge would turn an
// ordering problem into an outage.
type TriggerScheduleSync struct{ syncer *triggers.Syncer }

// TriggerScheduleSyncDeps is the syncer.
type TriggerScheduleSyncDeps struct {
	Syncer *triggers.Syncer
}

// NewTriggerScheduleSync is the startup sync.
//
//forge:root-only api
func NewTriggerScheduleSync(d TriggerScheduleSyncDeps) (*TriggerScheduleSync, error) {
	return &TriggerScheduleSync{syncer: d.Syncer}, nil
}

// Run syncs once.
func (s *TriggerScheduleSync) Run(ctx context.Context) error {
	triggers.SyncAllOnStartup(ctx, s.syncer)
	return nil
}

// TriggerReconciler keeps activations of workflow-declared triggers projected
// from their workflows: an edited cron reconverges its schedule, an edited
// source re-routes. Fires read the declaration themselves, so this bounds
// only routing lag.
type TriggerReconciler struct{ reconciler *triggers.Reconciler }

// TriggerReconcilerDeps is the triggers table, the syncer, and the alert
// receivers it sweeps.
type TriggerReconcilerDeps struct {
	DB     *db.Repo
	Syncer *triggers.Syncer
	// nil without a control plane.
	// forge:optional-dep
	Alerting *triggers.Alerting
}

// NewTriggerReconciler is the reconciler loop.
//
//forge:root-only api
func NewTriggerReconciler(d TriggerReconcilerDeps) (*TriggerReconciler, error) {
	repo := d.DB
	return &TriggerReconciler{triggers.NewReconciler(repo, triggers.LaunchWorkflows{Repo: repo}, d.Syncer).WithAlerting(d.Alerting)}, nil
}

// Run reconciles until ctx ends.
func (r *TriggerReconciler) Run(ctx context.Context) error {
	r.reconciler.Run(ctx)
	return nil
}

// TriggerRedriver restarts the fire of any inbound trigger event left
// pending — the receiver recorded it and its start was lost. The receivers
// live here, so the redrive does too.
type TriggerRedriver struct{ redriver *triggers.Redriver }

// TriggerRedriverDeps is the events table and the Temporal client fires
// start on.
type TriggerRedriverDeps struct {
	DB       *db.Repo
	Temporal *TemporalClient
}

// NewTriggerRedriver is the redrive loop.
//
//forge:root-only api
func NewTriggerRedriver(d TriggerRedriverDeps) (*TriggerRedriver, error) {
	return &TriggerRedriver{triggers.NewRedriver(d.DB, d.Temporal.Client, v2workflow.SharedTaskQueue)}, nil
}

// Run redrives until ctx ends.
func (r *TriggerRedriver) Run(ctx context.Context) error {
	r.redriver.Run(ctx)
	return nil
}

// backgroundProcessLeaseWindow is how fresh a daemon_attachment lease must be
// for the daemon to count as connected. Six missed 15s heartbeats, the same
// window the daemon router uses to decide a daemon is routable.
const backgroundProcessLeaseWindow = 90 * time.Second

// backgroundProcessDaemons adapts the daemon router and the attachment table
// to reconciliation.BackgroundProcessDaemons: the reconciler asks a daemon
// about its processes through the router, and learns which daemons are
// connected from the attachment leases the gateway renews on every heartbeat.
type backgroundProcessDaemons struct {
	router toolexec.DaemonRouter
	repo   attachedDaemons
}

// attachedDaemons is the attachment-lease read backgroundProcessDaemons makes.
type attachedDaemons interface {
	ListAttachedDaemonIDsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]string, error)
}

func (d backgroundProcessDaemons) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return d.router.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
}

func (d backgroundProcessDaemons) ConnectedDaemonIDs(ctx context.Context, userID string) ([]string, error) {
	return d.repo.ListAttachedDaemonIDsForUser(ctx, userID, backgroundProcessLeaseWindow)
}

// WorkflowReconciler is background workflow reconciliation: stuck-task
// recovery, wedged runs, queued messages whose connect the consumer missed.
type WorkflowReconciler struct{ reconciler *reconciliation.Reconciler }

// WorkflowReconcilerDeps is everything a pass reads and repairs through.
type WorkflowReconcilerDeps struct {
	DB         *db.Repo
	Temporal   *TemporalClient
	Namespace  string
	ResetGuard *v2workflow.ResetAttemptGuard
	Queued     queueddelivery.Service
	Router     *APIDaemonRouter
	// Activity is where a held run's still-running background work is
	// published as machine activity, so a suspend does not kill it.
	Activity *daemonstate.Publisher
}

// NewWorkflowReconciler builds the reconciler. Its namespace must match the
// Temporal client's so reset (stuck-task recovery) requests land in the right
// namespace; the task queue defaults to the shared worker queue. Backgrounded
// tool calls end when their process does, and only the daemon running the
// process can say so: the reconciler asks it on every pass.
//
//forge:root-only api
func NewWorkflowReconciler(d WorkflowReconcilerDeps) (*WorkflowReconciler, error) {
	cfg := reconciliation.DefaultConfig()
	cfg.Namespace = d.Namespace
	r := reconciliation.NewReconciler(d.DB, d.Temporal.Client, cfg)
	r.SetResetGuard(d.ResetGuard)
	r.SetQueuedDelivery(d.Queued)
	r.SetBackgroundProcessDaemons(backgroundProcessDaemons{router: d.Router.NATSDaemonRouter, repo: d.DB})
	if d.Activity != nil {
		r.SetHeldRunActivity(d.Activity)
	}
	return &WorkflowReconciler{r}, nil
}

// Run reconciles in the background until ctx ends.
func (w *WorkflowReconciler) Run(ctx context.Context) error {
	w.reconciler.StartBackgroundReconciliation(ctx)
	<-ctx.Done()
	return nil
}

// Close waits for the reconciler's pass to stop.
func (w *WorkflowReconciler) Close(context.Context) error {
	w.reconciler.Stop()
	return nil
}

// ReliantKeySweep re-mints, hourly, every user's Reliant key that the control
// plane no longer honours.
type ReliantKeySweep struct{ healer *drivers.ReliantKeyHealer }

// ReliantKeySweepDeps is the process runtime that installed the healer.
type ReliantKeySweepDeps struct {
	Runtime *ProcessRuntime
}

// NewReliantKeySweep is the sweep, or nothing (nil) without a control plane:
// a self-hosted deployment has no Reliant key to heal.
//
//forge:root-only api
func NewReliantKeySweep(d ReliantKeySweepDeps) (*ReliantKeySweep, error) {
	if d.Runtime.KeyHealer == nil {
		return nil, nil
	}
	return &ReliantKeySweep{healer: d.Runtime.KeyHealer}, nil
}

// Run sweeps until ctx ends.
func (k *ReliantKeySweep) Run(ctx context.Context) error {
	drivers.RunReliantKeySweeps(ctx, k.healer, time.Hour)
	return nil
}

// PayloadGC is the claim-check store's GC: the api-server owns the schema,
// so it owns the unreferenced-payload sweep too.
type PayloadGC struct {
	store   *claimcheck.PostgresStore
	horizon time.Duration
}

// PayloadGCDeps is the store and how long an unreferenced blob is kept.
type PayloadGCDeps struct {
	Store    *claimcheck.PostgresStore
	Settings *APISettings
}

// NewPayloadGC is the GC loop.
//
//forge:root-only api
func NewPayloadGC(d PayloadGCDeps) (*PayloadGC, error) {
	return &PayloadGC{store: d.Store, horizon: d.Settings.PayloadGCHorizon}, nil
}

// Run collects until ctx ends.
func (p *PayloadGC) Run(ctx context.Context) error {
	p.store.RunGC(ctx, p.horizon)
	return nil
}

// apiMountOptions is each user-audience service's mount options on top of the
// user chain. The services whose requests wake an asleep machine
// (machinewake: the worktree and filesystem services) report the wake as
// Unavailable + DaemonWaking, whatever error they wrap it in. ForgeService is
// always mounted — an unmounted one answers 404, which the web files as
// version skew — and when the operator turned the forge API off
// (RELIANT_DISABLE_FORGE_API) it answers every rpc FailedPrecondition, after
// auth, before the handler runs.
//
// The list reads the user-update stream makes a client refetch (the chat list,
// the archived list, a chat's approvals) go through one readguard: one user's
// identical concurrent reads share a read, and at most readguard.DefaultPerUser
// distinct ones run at once, so a client that storms them (desktop builds
// before #1865) cannot take the database pool from everyone else.
func apiMountOptions(disableForgeAPI bool) MountOptions {
	machineWake := mountkit.WithHandlerOptions(connect.WithInterceptors(machinewake.NewInterceptor()))
	listReads := mountkit.WithHandlerOptions(connect.WithInterceptors(readguard.NewInstrumented(readguard.Deps{
		Procedures: []readguard.Procedure{
			readguard.Read[reliantv1.ListChatsResponse](reliantv1connect.ChatServiceListChatsProcedure),
			readguard.Read[reliantv1.ListArchivedChatsResponse](reliantv1connect.ChatServiceListArchivedChatsProcedure),
			readguard.Read[reliantv1.ListApprovalsByChatResponse](reliantv1connect.ApprovalServiceListApprovalsByChatProcedure),
		},
	})))
	opts := MountOptions{
		Worktree:   []mountkit.Option{machineWake},
		FileSystem: []mountkit.Option{machineWake},
		Chat:       []mountkit.Option{listReads},
		Approval:   []mountkit.Option{listReads},
	}
	if disableForgeAPI {
		logging.Info("forge API disabled in this deployment (RELIANT_DISABLE_FORGE_API): ForgeService answers FailedPrecondition")
		opts.Forge = []mountkit.Option{grpcserver.ForgeAPIDisabled()}
	}
	return opts
}

// triggerAccess is the integration access TriggerService checks a trigger
// owner's against: the inbound receivers' refreshers (GitHub's, when this
// deployment receives GitHub events).
func triggerAccess(in *webhook.Inbound) map[string]trigger.IntegrationAccess {
	access := make(map[string]trigger.IntegrationAccess, len(in.Access))
	for id, a := range in.Access {
		access[id] = a
	}
	return access
}

// alertingProvisioner is a as TriggerService takes it: nil — not a typed nil
// in the interface, which would pass the handler's nil checks — without a
// control plane.
func alertingProvisioner(a *triggers.Alerting) trigger.AlertingProvisioner {
	if a == nil {
		return nil
	}
	return a
}

// hostedControlPlane is the control-plane client of a deployment a control
// plane hosts, for the handlers that act on it only then (cloning from
// GitHub, deleting the platform account, minting automation tokens). It is
// nil — never a typed nil in the handler's interface — when self-hosted, and
// never defaults to localhost: a deployment would then fail against a control
// plane that is not there.
func hostedControlPlane(cfg *configv1.ControlPlaneConfig) controlplane.Service {
	cp := ControlPlaneEndpoint(cfg)
	if !cp.Hosted() {
		return nil
	}
	return controlplane.NewInstrumented(controlplane.Deps{ControlPlane: cfg})
}

// controlPlaneClient is the control-plane client SettingsService mints LLM
// keys through, authenticated as the signed-in user.
func controlPlaneClient(cfg *configv1.ControlPlaneConfig) controlplane.Service {
	return controlplane.NewInstrumented(controlplane.Deps{ControlPlane: cfg})
}

// gitTokens is where TokenService gets a hosted user's git tokens: the
// control plane, as the internal service. nil self-hosted.
// recoveryCredentials fails closed on self-hosted deployments without the
// existing credential authority, without preventing ordinary worktree RPCs.
type recoveryCredentials struct{ client gitcredentialclient.Service }

func (c recoveryCredentials) UserAccessToken(ctx context.Context, userID, provider string) (gitcredentialclient.Token, error) {
	if c.client == nil {
		return gitcredentialclient.Token{}, gitcredentialclient.ErrNotConnected
	}
	return c.client.UserAccessToken(ctx, userID, provider)
}

func gitTokens(cfg *configv1.ControlPlaneConfig) (gitcredentialclient.Service, error) {
	cp := ControlPlaneEndpoint(cfg)
	if !cp.Hosted() {
		return nil, nil
	}
	// Hosted users' git tokens live at control-plane. NewTokenAuthority
	// already refused to boot without the secret; check again so this
	// wiring never rests on that ordering.
	secret := cp.InternalServiceSecret
	if secret == "" {
		return nil, errors.New("git tokens: RELIANT_CONTROL_PLANE_URL is set but INTERNAL_SERVICE_SECRET is not")
	}
	return gitcredentialclient.NewInstrumented(gitcredentialclient.Deps{
		ControlPlane: cfg,
		Sign:         func() (string, error) { return auth.SignInternalServiceToken(secret) },
	}), nil
}
