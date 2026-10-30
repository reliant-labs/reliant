// forge-scaffold (USER-OWNED, RECONCILED): forge writes this file once and then
// keeps only its projection in sync. It is the composition graph's wiring: one
// constructor method per node, holding the value expression for every field of
// that node's Deps. Those expressions are yours — change what fills a field
// here, and forge never touches it again.
//
// What forge reconciles on every `forge generate`:
//   - a node with no new<X> method (a service, worker, package or provider you
//     just added) gets one, each Deps field resolved BY TYPE;
//   - a Deps field a node gained gets its key added; a key whose field the
//     Deps struct no longer has is dropped;
//   - a key two nodes could fill is a generate error until you write it here.
//
// The memoized accessors these feed (g.<X>()), the roots and their lifecycle
// are generated in graph_gen.go. `forge project disown internal/app/compose.go`
// stops the reconciliation.

package app

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/internal/agentruns"
	reliantconfig "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/daemonevents"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/egresslimit"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
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
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	workflowhandler "github.com/reliant-labs/reliant/internal/handlers/workflow"
	"github.com/reliant-labs/reliant/internal/handlers/worktree"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/launch"
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
	"github.com/reliant-labs/reliant/internal/pkgmgr"
	"github.com/reliant-labs/reliant/internal/queueddelivery"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/servergateway"
	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/workflowevent"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/reliant-labs/reliant/internal/workersetup"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

func (g *Graph) newDBRepo() (*DBRepo, error) {
	return NewDBRepo(DBRepoDeps{
		Database: g.DatabaseConfig,
		DataDir:  g.ServerProcessConfig.GetDataDir(),
	})
}

// newDatabase is the value every persistence role below is filled with
// (TestPersistenceRolesAreTheDatabase); DBRepo is its lifecycle.
func (g *Graph) newDatabase() (*db.Repo, error) {
	return NewDatabase(DatabaseDeps{
		Pool: g.DBRepo(),
	})
}

func (g *Graph) newNATSConn() (*NATSConn, error) {
	return NewNATSConn(NATSConnDeps{
		Ctx:  g.Ctx,
		NATS: g.ServerProcessConfig.GetNats(),
	})
}

func (g *Graph) newJetStream() (jetstream.JetStream, error) {
	return NewJetStream(JetStreamDeps{
		NATS: g.NATSConn(),
	})
}

func (g *Graph) newUserUpdateHub() (*UserUpdateHub, error) {
	return NewUserUpdateHub(UserUpdateHubDeps{
		NATS: g.NATSConn(),
	})
}

func (g *Graph) newTokenAuthority() (tokenauthority.Authority, error) {
	return NewTokenAuthority(TokenAuthorityDeps{
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DB:           g.Database(),
	})
}

func (g *Graph) newTLSFiles() (*TLSFiles, error) {
	return NewTLSFiles(TLSFilesDeps{
		TLS:     g.ServerProcessConfig.GetTls(),
		DataDir: g.ServerProcessConfig.GetDataDir(),
	})
}

func (g *Graph) newPprof() (*serverkit.Pprof, error) {
	return NewPprof(PprofDeps{
		ServerCfg: g.ServerCfg,
		Logger:    g.Log,
	})
}

func (g *Graph) newToolsDaemon() (*tools_daemon.Service, error) {
	return tools_daemon.New(tools_daemon.Deps{
		Logger:          g.Log,
		Config:          g.Cfg,
		Repo:            g.Database(),
		UserUpdateHub:   g.UserUpdateHub(),
		StatePublisher:  g.DaemonStatePublisher(),
		ResultPublisher: g.ToolResultPublisher(),
	})
}

func (g *Graph) newDaemonAuth() (*grpcserver.DaemonAuth, error) {
	return NewDaemonAuth(DaemonAuthDeps{
		Tokens: g.TokenAuthority(),
	})
}

func (g *Graph) newDaemonChain() (*grpcserver.DaemonChain, error) {
	return NewDaemonChain(DaemonChainDeps{
		Auth:   g.DaemonAuth(),
		Logger: g.Log,
	})
}

func (g *Graph) newDaemonListener() (*grpcserver.DaemonListener, error) {
	return NewDaemonListener(DaemonListenerDeps{
		Gateway: g.GatewayConfig,
		TLS:     g.TLSFiles(),
		Chain:   g.DaemonChain(),
		Routes:  g.DaemonRoutes(),
		Logger:  g.Log,
	})
}

func (g *Graph) newDaemonStatePublisher() (*daemonstate.Publisher, error) {
	return NewDaemonStatePublisher(DaemonStatePublisherDeps{
		NATS: g.NATSConn(),
	})
}

func (g *Graph) newToolResultPublisher() (*toolexec.ToolResultPublisher, error) {
	return NewToolResultPublisher(ToolResultPublisherDeps{
		NATS: g.NATSConn(),
	})
}

func (g *Graph) newToolsDaemonSweeper() (*ToolsDaemonSweeper, error) {
	return NewToolsDaemonSweeper(ToolsDaemonSweeperDeps{
		ToolsDaemon: g.ToolsDaemon(),
	})
}

func (g *Graph) newStateDerivation() (*StateDerivation, error) {
	return NewStateDerivation(StateDerivationDeps{
		NATS:        g.NATSConn(),
		ToolsDaemon: g.ToolsDaemon(),
		DB:          g.Database(),
	})
}

func (g *Graph) newToolBridge() (*ToolBridge, error) {
	return NewToolBridge(ToolBridgeDeps{
		NATS:        g.NATSConn(),
		JetStream:   g.JetStream(),
		ToolsDaemon: g.ToolsDaemon(),
	})
}

func (g *Graph) newDaemonEventPublisher() (*daemonevents.Publisher, error) {
	return NewDaemonEventPublisher(DaemonEventPublisherDeps{
		Ctx:       g.Ctx,
		NATS:      g.NATSConn(),
		JetStream: g.JetStream(),
	})
}

func (g *Graph) newConnectionListeners() (*ConnectionListeners, error) {
	return NewConnectionListeners(ConnectionListenersDeps{
		NATS:        g.NATSConn(),
		ToolsDaemon: g.ToolsDaemon(),
		Bridge:      g.ToolBridge(),
		Events:      g.DaemonEventPublisher(),
	})
}

func (g *Graph) newActivitySubscriber() (*ActivitySubscriber, error) {
	return NewActivitySubscriber(ActivitySubscriberDeps{
		NATS:        g.NATSConn(),
		ToolsDaemon: g.ToolsDaemon(),
	})
}

func (g *Graph) newDaemonConnector() (*servergateway.DaemonConnector, error) {
	return NewDaemonConnector(DaemonConnectorDeps{
		ToolsDaemon: g.ToolsDaemon(),
		Events:      g.DaemonEventPublisher(),
	})
}

func (g *Graph) newManagedReconciler() (*ManagedReconciler, error) {
	return NewManagedReconciler(ManagedReconcilerDeps{
		JetStream: g.JetStream(),
		Connector: g.DaemonConnector(),
	})
}

// newFlowHealth serves /flow-health on the root's probe surface, beside
// /health and /ready.
func (g *Graph) newFlowHealth() (*FlowHealth, error) {
	fh, err := NewFlowHealth(FlowHealthDeps{
		ToolsDaemon: g.ToolsDaemon(),
		DB:          g.Database(),
	})
	if err != nil {
		return nil, err
	}
	if g.Probes != nil {
		if err := g.Probes.Handle("/flow-health", fh.Handler); err != nil {
			return nil, fmt.Errorf("flow health: %w", err)
		}
	}
	return fh, nil
}

// newCatalog constructs the catalog handler (internal/handlers/catalog).
func (g *Graph) newCatalog() (*catalog.Service, error) {
	return catalog.New(catalog.Deps{
		Logger:             g.Log,
		Config:             g.Cfg,
		Tools:              g.APIToolsFactory(),
		TagPrefs:           g.Database(),
		LocalModels:        local.NewRepoDirectory(g.Database()),
		Search:             g.APICatalogSearch(),
		InputOptions:       g.InputOptions(),
		IntegrationMethods: catalog.IntegrationMethodsFrom(g.APIConnections().Service),
	})
}

// newDaemon constructs the daemon handler (internal/handlers/daemon).
func (g *Graph) newDaemon() (*daemon.Service, error) {
	return daemon.New(daemon.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Router: g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newSystem constructs the system handler (internal/handlers/system).
func (g *Graph) newSystem() (*system.Service, error) {
	return system.New(system.Deps{
		Logger:        g.Log,
		Config:        g.Cfg,
		Repo:          g.Database(),
		Temporal:      g.TemporalClient().Client,
		NATSConnected: g.NATSConn().Conn.IsConnected,
		StreamingHub:  g.StreamingHub().Hub,
		OAuthProvider: g.APISettings().OAuthProvider,
	})
}

func (g *Graph) newDebugServer() (*DebugServer, error) {
	return NewDebugServer(DebugServerDeps{
		Port: g.WorkerConfig.GetPprofPort(),
	})
}

func (g *Graph) newIntegrationSettings() (*IntegrationSettings, error) {
	return NewIntegrationSettings(IntegrationSettingsDeps{
		Integrations: g.ServerProcessConfig.GetIntegrations(),
	})
}

func (g *Graph) newProcessSettings() (*ProcessSettings, error) {
	return NewProcessSettings(ProcessSettingsDeps{
		DataDir:      g.ServerProcessConfig.GetDataDir(),
		LLM:          g.ServerProcessConfig.GetLlm(),
		Preview:      g.ServerProcessConfig.GetPreview(),
		Integrations: g.IntegrationSettings(),
	})
}

func (g *Graph) newProcessRuntime() (*ProcessRuntime, error) {
	return NewProcessRuntime(ProcessRuntimeDeps{
		Settings:     g.ProcessSettings(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		Network:      g.ServerProcessConfig.GetNetwork(),
		Analytics:    g.ServerProcessConfig.GetAnalytics(),
		DB:           g.Database(),
	})
}

func (g *Graph) newPayloadStore() (*claimcheck.PostgresStore, error) {
	return NewPayloadStore(PayloadStoreDeps{
		DB: g.Database(),
	})
}

func (g *Graph) newTemporalClient() (*TemporalClient, error) {
	return NewTemporalClient(TemporalClientDeps{
		Ctx:          g.Ctx,
		Temporal:     g.ServerProcessConfig.GetTemporal(),
		PayloadStore: g.PayloadStore(),
		NATS:         g.NATSConn(),
	})
}

func (g *Graph) newStreamingHub() (*StreamingHub, error) {
	return NewStreamingHub(StreamingHubDeps{
		NATS:   g.NATSConn(),
		Server: g.ServerProcessConfig.GetNats(),
		Driver: g.ServerProcessConfig.GetStreamingDriver(),
	})
}

func (g *Graph) newChatUpdateHub() (*ChatUpdateHub, error) {
	return NewChatUpdateHub(ChatUpdateHubDeps{
		NATS: g.NATSConn(),
	})
}

func (g *Graph) newRepoUpdateFeed() (*RepoUpdateFeed, error) {
	return NewRepoUpdateFeed(RepoUpdateFeedDeps{
		Users: g.UserUpdateHub(),
		Chats: g.ChatUpdateHub(),
		DB:    g.Database(),
	})
}

func (g *Graph) newVault() (vault.Service, error) {
	return NewVault(VaultDeps{
		Ctx:          g.Ctx,
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DataDir:      g.ServerProcessConfig.GetDataDir(),
		Integrations: g.IntegrationSettings(),
		DB:           g.Database(),
	})
}

func (g *Graph) newPauseService() (*v2workflow.PauseService, error) {
	return NewPauseService(PauseServiceDeps{
		Temporal: g.TemporalClient(),
		DB:       g.Database(),
	})
}

func (g *Graph) newTriggerPollers() (*webhook.Registry, error) {
	return NewTriggerPollers(TriggerPollersDeps{
		Integrations: g.IntegrationSettings(),
	})
}

func (g *Graph) newWorkerDaemonRouter() (*WorkerDaemonRouter, error) {
	return NewWorkerDaemonRouter(WorkerDaemonRouterDeps{
		NATS:         g.NATSConn(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DB:           g.Database(),
	})
}

func (g *Graph) newWorkerIntegrationCredentials() (*WorkerIntegrationCredentials, error) {
	return NewWorkerIntegrationCredentials(WorkerIntegrationCredentialsDeps{
		Vault:        g.Vault(),
		Integrations: g.IntegrationSettings(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DB:           g.Database(),
	})
}

func (g *Graph) newWorkerCatalogSearch() (*WorkerCatalogSearch, error) {
	return NewWorkerCatalogSearch(WorkerCatalogSearchDeps{
		Credentials: g.WorkerIntegrationCredentials(),
		DB:          g.Database(),
	})
}

func (g *Graph) newWriteLedger() (writeledger.Service, error) {
	return NewWriteLedger(WriteLedgerDeps{
		DB: g.Database(),
	})
}

func (g *Graph) newStoredConfigProvider() (*reliantconfig.StoredConfigProvider, error) {
	return NewStoredConfigProvider(StoredConfigProviderDeps{
		DB: g.Database(),
	})
}

func (g *Graph) newRunLifecycle() (*runs.Service, error) {
	return NewRunLifecycle(RunLifecycleDeps{
		Temporal: g.TemporalClient(),
		Pause:    g.PauseService(),
		DB:       g.Database(),
	})
}

func (g *Graph) newRunLauncher() (*launch.Launcher, error) {
	return NewRunLauncher(RunLauncherDeps{
		Temporal: g.TemporalClient(),
		Runs:     g.RunLifecycle(),
		DB:       g.Database(),
		Router:   g.WorkerDaemonRouter(),
	})
}

func (g *Graph) newWorkerChat() (*WorkerChat, error) {
	return NewWorkerChat(WorkerChatDeps{
		Logger:   g.Log,
		Config:   g.Cfg,
		Temporal: g.TemporalClient(),
		Pause:    g.PauseService(),
		Router:   g.WorkerDaemonRouter(),
		DB:       g.Database(),
	})
}

func (g *Graph) newWorkerRun() (*WorkerRun, error) {
	return NewWorkerRun(WorkerRunDeps{
		Logger: g.Log,
		Config: g.Cfg,
		Chats:  g.WorkerChat(),
		DB:     g.Database(),
	})
}

func (g *Graph) newAgentRuns() (*agentruns.Runs, error) {
	return NewAgentRuns(AgentRunsDeps{
		Launcher: g.RunLauncher(),
		Runs:     g.WorkerRun(),
	})
}

func (g *Graph) newWorkerTrigger() (*WorkerTrigger, error) {
	return NewWorkerTrigger(WorkerTriggerDeps{
		Logger:       g.Log,
		Config:       g.Cfg,
		Temporal:     g.TemporalClient(),
		Vault:        g.Vault(),
		Pollers:      g.TriggerPollers(),
		Integrations: g.IntegrationSettings(),
		DB:           g.Database(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
	})
}

func (g *Graph) newWorkerToolsFactory() (*WorkerToolsFactory, error) {
	return NewWorkerToolsFactory(WorkerToolsFactoryDeps{
		Network:       g.ServerProcessConfig.GetNetwork(),
		Temporal:      g.TemporalClient(),
		AgentRuns:     g.AgentRuns(),
		Triggers:      g.WorkerTrigger(),
		Credentials:   g.WorkerIntegrationCredentials(),
		WriteLedger:   g.WriteLedger(),
		CatalogSearch: g.WorkerCatalogSearch(),
		Router:        g.WorkerDaemonRouter(),
		DB:            g.Database(),
	})
}

func (g *Graph) newRemoteExecutor() (*toolexec.RemoteExecutor, error) {
	return NewRemoteExecutor(RemoteExecutorDeps{
		Router: g.WorkerDaemonRouter(),
		Tools:  g.WorkerToolsFactory(),
		// Read here, not in the provider, so the worker root's config reach
		// names them (pkg/config Roots).
		Egress: egresslimit.Limits{
			Replicas:          int(g.WorkerConfig.GetEgressReplicas()),
			UserPerMinute:     int(g.WorkerConfig.GetEgressUserPerMinute()),
			UserBurst:         int(g.WorkerConfig.GetEgressUserBurst()),
			UserMaxConcurrent: int(g.WorkerConfig.GetEgressUserMaxConcurrent()),
			HostMaxConcurrent: int(g.WorkerConfig.GetEgressHostMaxConcurrent()),
			TurnMaxParallel:   int(g.WorkerConfig.GetEgressTurnMaxParallel()),
		},
	})
}

func (g *Graph) newAccountRecorder() (*AccountRecorder, error) {
	return NewAccountRecorder(AccountRecorderDeps{
		DB: g.Database(),
	})
}

func (g *Graph) newTemporalWorker() (*workersetup.Worker, error) {
	return NewTemporalWorker(TemporalWorkerDeps{
		Temporal:       g.TemporalClient(),
		Streaming:      g.StreamingHub(),
		Tools:          g.WorkerToolsFactory(),
		Executor:       g.RemoteExecutor(),
		Router:         g.WorkerDaemonRouter(),
		ConfigProvider: g.StoredConfigProvider(),
		Launcher:       g.RunLauncher(),
		Credentials:    g.WorkerIntegrationCredentials(),
		WriteLedger:    g.WriteLedger(),
		Pollers:        g.TriggerPollers(),
		ControlPlane:   g.ServerProcessConfig.GetControlPlane(),
		Network:        g.ServerProcessConfig.GetNetwork(),
		Recorder:       g.AccountRecorder(),
		DB:             g.Database(),
		// Read here, not in workersetup, so the worker root's config reach
		// names them (pkg/config Roots).
		WorkflowTaskSlots:   g.WorkerConfig.GetMaxConcurrentWorkflowTasks(),
		WorkflowTaskPollers: g.WorkerConfig.GetWorkflowTaskPollers(),
		Worker:              g.WorkerConfig,
		NATS:                g.NATSConn(),
	})
}

func (g *Graph) newWorkflowEventRelay() (*workflowevent.Relay, error) {
	return NewWorkflowEventRelay(WorkflowEventRelayDeps{
		Temporal: g.TemporalClient(),
		DB:       g.Database(),
	})
}

func (g *Graph) newAccountUsagePoller() (*accountlimits.Poller, error) {
	return NewAccountUsagePoller(AccountUsagePollerDeps{
		DB:       g.Database(),
		Temporal: g.TemporalClient(),
	})
}

func (g *Graph) newWriteLedgerPrune() (*WriteLedgerPrune, error) {
	return NewWriteLedgerPrune(WriteLedgerPruneDeps{
		Ledger: g.WriteLedger(),
		DB:     g.Database(),
	})
}

// newDaemonRoutes constructs the daemon-audience route set (routes_gen.go).
func (g *Graph) newDaemonRoutes() (*DaemonRoutes, error) {
	return NewDaemonRoutes(DaemonRoutesDeps{
		ToolsDaemon: g.ToolsDaemon(),
		Logger:      g.Log,
		Options:     MountOptions{}, // per-service mountkit options on top of the listener's chain — yours
	})
}

// The api root (roots_api.go): its providers (providers_api.go), then its 35
// user-audience handlers, each Deps value what the api-server has always
// handed it. A Router is always the api's own (g.APIDaemonRouter()), spelled
// out: the worker's router is a type of its own, and never the api's.

func (g *Graph) newAPISettings() (*APISettings, error) {
	return NewAPISettings(APISettingsDeps{
		API: g.ApiServerConfig,
	})
}

func (g *Graph) newUserAuth() (*grpcserver.UserAuth, error) {
	return NewUserAuth(UserAuthDeps{
		Settings: g.APISettings(),
		Tokens:   g.TokenAuthority(),
	})
}

func (g *Graph) newUserChain() (*grpcserver.UserChain, error) {
	return NewUserChain(UserChainDeps{
		Auth:                g.UserAuth(),
		AllowedEmailDomains: g.ApiServerConfig.GetAllowedEmailDomains(),
		Logger:              g.Log,
	})
}

func (g *Graph) newHTTPRoutes() (*grpcserver.APIHTTPRoutes, error) {
	return NewHTTPRoutes(HTTPRoutesDeps{
		Settings:     g.APISettings(),
		Auth:         g.UserAuth(),
		Router:       g.APIDaemonRouter(),
		Connections:  g.APIConnections(),
		Inbound:      g.TriggerInbound(),
		Grants:       g.ConnectorGrants(),
		Tokens:       g.TokenAuthority(),
		Integrations: g.IntegrationSettings(),
		OAuthIssuers: g.ApiServerConfig.GetMcpOauthIssuers(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DB:           g.Database(),
	})
}

func (g *Graph) newAPIListener() (*grpcserver.APIListener, error) {
	return NewAPIListener(APIListenerDeps{
		API:    g.ApiServerConfig,
		TLS:    g.TLSFiles(),
		Chain:  g.UserChain(),
		Routes: g.UserRoutes(),
		HTTP:   g.HTTPRoutes(),
		Logger: g.Log,
	})
}

func (g *Graph) newAPIDebugServer() (*APIDebugServer, error) {
	return NewAPIDebugServer(APIDebugServerDeps{
		Port: g.ApiServerConfig.GetPprofPort(),
	})
}

func (g *Graph) newAPIDaemonRouter() (*APIDaemonRouter, error) {
	return NewAPIDaemonRouter(APIDaemonRouterDeps{
		NATS:         g.NATSConn(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DB:           g.Database(),
	})
}

func (g *Graph) newAPIToolsFactory() (*tools.ToolsFactory, error) {
	return NewAPIToolsFactory(APIToolsFactoryDeps{
		Network:  g.ServerProcessConfig.GetNetwork(),
		Temporal: g.TemporalClient(),
		DB:       g.Database(),
	})
}

func (g *Graph) newAPIConnections() (*APIConnections, error) {
	return NewAPIConnections(APIConnectionsDeps{
		Vault:        g.Vault(),
		Integrations: g.IntegrationSettings(),
		AppOrigins:   g.ApiServerConfig.GetCorsAllowedOrigins(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DB:           g.Database(),
	})
}

func (g *Graph) newAPICatalogSearch() (*APICatalogSearch, error) {
	return NewAPICatalogSearch(APICatalogSearchDeps{
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DB:           g.Database(),
	})
}

func (g *Graph) newInputOptions() (inputoptions.Service, error) {
	return NewInputOptions(InputOptionsDeps{
		Connections:  g.APIConnections(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DB:           g.Database(),
	})
}

func (g *Graph) newConnectorGrants() (connectorgrant.Store, error) {
	return NewConnectorGrants(ConnectorGrantsDeps{
		DB: g.Database(),
	})
}

func (g *Graph) newGitHubAccess() (*ghaccess.Refresher, error) {
	return NewGitHubAccess(GitHubAccessDeps{
		Vault:        g.Vault(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		Integrations: g.IntegrationSettings(),
		DB:           g.Database(),
	})
}

func (g *Graph) newTriggerSenders() (TriggerSenders, error) {
	return NewTriggerSenders(TriggerSendersDeps{
		Vault:        g.Vault(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		Integrations: g.IntegrationSettings(),
		DB:           g.Database(),
	})
}

func (g *Graph) newGitHubAccessLoop() (*GitHubAccessLoop, error) {
	return NewGitHubAccessLoop(GitHubAccessLoopDeps{
		Access: g.GitHubAccess(),
	})
}

func (g *Graph) newTriggerAlerting() (*triggers.Alerting, error) {
	return NewTriggerAlerting(TriggerAlertingDeps{
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		Integrations: g.IntegrationSettings(),
		DB:           g.Database(),
	})
}

func (g *Graph) newTriggerInbound() (*webhook.Inbound, error) {
	return NewTriggerInbound(TriggerInboundDeps{
		Temporal:     g.TemporalClient(),
		Pollers:      g.TriggerPollers(),
		Vault:        g.Vault(),
		Integrations: g.IntegrationSettings(),
		Connections:  g.APIConnections(),
		GitHubAccess: g.GitHubAccess(),
		DB:           g.Database(),
	})
}

func (g *Graph) newResetGuard() (*v2workflow.ResetAttemptGuard, error) {
	return NewResetGuard(ResetGuardDeps{
		Pause: g.PauseService(),
	})
}

func (g *Graph) newQueuedDeliverer() (queueddelivery.Service, error) {
	return NewQueuedDeliverer(QueuedDelivererDeps{
		Router:   g.APIDaemonRouter(),
		Chat:     g.Chat(),
		Temporal: g.TemporalClient(),
		DB:       g.Database(),
	})
}

func (g *Graph) newQueuedDelivery() (*QueuedDelivery, error) {
	return NewQueuedDelivery(QueuedDeliveryDeps{
		Deliverer: g.QueuedDeliverer(),
		NATS:      g.NATSConn(),
	})
}

func (g *Graph) newDaemonEventsConsumer() (*DaemonEventsConsumer, error) {
	return NewDaemonEventsConsumer(DaemonEventsConsumerDeps{
		Projects: g.Project(),
		NATS:     g.NATSConn(),
	})
}

func (g *Graph) newWorktreeSweep() (*worktreesweep.Sweeper, error) {
	return NewWorktreeSweep(WorktreeSweepDeps{
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		Router:       g.APIDaemonRouter(),
		Settings:     g.APISettings(),
		DB:           g.Database(),
	})
}

func (g *Graph) newWorktreeSweeper() (*WorktreeSweeper, error) {
	return NewWorktreeSweeper(WorktreeSweeperDeps{
		Sweep: g.WorktreeSweep(),
	})
}

func (g *Graph) newScheduleSyncer() (*triggers.Syncer, error) {
	return NewScheduleSyncer(ScheduleSyncerDeps{
		Temporal: g.TemporalClient(),
		Pollers:  g.TriggerPollers(),
		DB:       g.Database(),
	})
}

func (g *Graph) newTriggerScheduleSync() (*TriggerScheduleSync, error) {
	return NewTriggerScheduleSync(TriggerScheduleSyncDeps{
		Syncer: g.ScheduleSyncer(),
	})
}

func (g *Graph) newTriggerReconciler() (*TriggerReconciler, error) {
	return NewTriggerReconciler(TriggerReconcilerDeps{
		Syncer:   g.ScheduleSyncer(),
		Alerting: g.TriggerAlerting(),
		DB:       g.Database(),
	})
}

func (g *Graph) newTriggerRedriver() (*TriggerRedriver, error) {
	return NewTriggerRedriver(TriggerRedriverDeps{
		Temporal: g.TemporalClient(),
		DB:       g.Database(),
	})
}

func (g *Graph) newWorkflowReconciler() (*WorkflowReconciler, error) {
	return NewWorkflowReconciler(WorkflowReconcilerDeps{
		Temporal:   g.TemporalClient(),
		Namespace:  g.ServerProcessConfig.GetTemporal().GetNamespace(),
		ResetGuard: g.ResetGuard(),
		Queued:     g.QueuedDeliverer(),
		Router:     g.APIDaemonRouter(),
		DB:         g.Database(),
		Activity:   g.DaemonStatePublisher(),
	})
}

func (g *Graph) newReliantKeySweep() (*ReliantKeySweep, error) {
	return NewReliantKeySweep(ReliantKeySweepDeps{
		Runtime: g.ProcessRuntime(),
	})
}

func (g *Graph) newPayloadGC() (*PayloadGC, error) {
	return NewPayloadGC(PayloadGCDeps{
		Store:    g.PayloadStore(),
		Settings: g.APISettings(),
	})
}

// newUserRoutes constructs the user-audience route set (routes_gen.go): every
// service the API listener mounts, with each one's mount options.
func (g *Graph) newUserRoutes() (*UserRoutes, error) {
	return NewUserRoutes(UserRoutesDeps{
		Account:         g.Account(),
		Approval:        g.Approval(),
		Attachment:      g.Attachment(),
		Background:      g.Background(),
		Connection:      g.Connection(),
		Worktree:        g.Worktree(),
		Chat:            g.Chat(),
		Run:             g.Run(),
		Trigger:         g.Trigger(),
		Catalog:         g.Catalog(),
		Connector:       g.Connector(),
		Daemon:          g.Daemon(),
		DaemonRegistry:  g.DaemonRegistry(),
		FileSystem:      g.FileSystem(),
		Forge:           g.Forge(),
		Inbox:           g.Inbox(),
		MCP:             g.MCP(),
		Message:         g.Message(),
		ModelEndpoint:   g.ModelEndpoint(),
		PackageCommands: g.PackageCommands(),
		Task:            g.Task(),
		Plan:            g.Plan(),
		Preset:          g.Preset(),
		Project:         g.Project(),
		ProviderAccount: g.ProviderAccount(),
		Question:        g.Question(),
		Repo:            g.Repo(),
		Scenario:        g.Scenario(),
		Settings:        g.Settings(),
		Streaming:       g.Streaming(),
		System:          g.System(),
		Terminal:        g.Terminal(),
		Token:           g.Token(),
		ToolCall:        g.ToolCall(),
		Workflow:        g.Workflow(),
		Logger:          g.Log,
		Options:         apiMountOptions(g.ApiServerConfig.GetDisableForgeApi()),
	})
}

// newAccount constructs the account handler (internal/handlers/account). Its
// purge runs on the raw *sql.DB: an ordered multi-statement transaction across
// every user-owned table, which the per-entity Repository surface cannot
// express. The control plane is wired only when this deployment HAS one:
// self-hosted reliant has no platform account to delete.
func (g *Graph) newAccount() (*account.Service, error) {
	return account.New(account.Deps{
		Logger:       g.Log,
		Config:       g.Cfg,
		SQL:          g.Database().DB.SQLDB(),
		ControlPlane: hostedControlPlane(g.ServerProcessConfig.GetControlPlane()),
	})
}

// newApproval constructs the approval handler (internal/handlers/approval).
func (g *Graph) newApproval() (*approval.Service, error) {
	return approval.New(approval.Deps{
		Logger:  g.Log,
		Config:  g.Cfg,
		Repo:    g.Database(),
		Signals: g.PauseService(),
	})
}

// newAttachment constructs the attachment handler (internal/handlers/attachment).
func (g *Graph) newAttachment() (*attachment.Service, error) {
	return attachment.New(attachment.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Repo:   g.Database(),
		API:    g.ApiServerConfig,
	})
}

// newBackground constructs the background handler
// (internal/handlers/background): a proxy that forwards each request through
// the daemon router to the connected daemon, which holds the processes.
func (g *Graph) newBackground() (*background.Service, error) {
	return background.New(background.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Router: g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newConnection constructs the connection handler (internal/handlers/connection).
func (g *Graph) newConnection() (*connection.Service, error) {
	return connection.New(connection.Deps{
		Logger:      g.Log,
		Config:      g.Cfg,
		Connections: g.APIConnections().Service,
	})
}

// newWorktree constructs the worktree handler (internal/handlers/worktree).
func (g *Graph) newWorktree() (*worktree.Service, error) {
	credentials, err := gitTokens(g.ServerProcessConfig.GetControlPlane())
	if err != nil {
		return nil, err
	}
	return worktree.New(worktree.Deps{
		GitCredentials: recoveryCredentials{client: credentials},
		Logger:         g.Log,
		Config:         g.Cfg,
		Repo:           g.Database(),
		Router:         g.APIDaemonRouter().NATSDaemonRouter,
		Temporal:       g.TemporalClient().Client,
		Settler:        g.WorktreeSweep(),
	})
}

// newChat constructs the chat handler (internal/handlers/chat).
func (g *Graph) newChat() (*chat.Service, error) {
	return chat.New(chat.Deps{
		Logger:      g.Log,
		Config:      g.Cfg,
		Repo:        g.Database(),
		Temporal:    g.TemporalClient().Client,
		Pause:       g.PauseService(),
		Router:      g.APIDaemonRouter().NATSDaemonRouter,
		LocalModels: local.NewRepoDirectory(g.Database()),
	})
}

// newRun constructs the run handler (internal/handlers/run): the engine's
// execution API. It delegates to the chat handler rather than reimplementing
// anything, so mounting it adds surface without adding behavior.
func (g *Graph) newRun() (*run.Service, error) {
	return run.New(run.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Repo:   g.Database(),
		Chats:  g.Chat(),
	})
}

// newTrigger constructs the trigger handler (internal/handlers/trigger). It
// converges Temporal Schedules onto the triggers table
// (trigger.TemporalBackend, which also polls the integrations the inbound
// registry polls), and writes webhook and integration triggers against the
// inbound receivers.
func (g *Graph) newTrigger() (*trigger.Service, error) {
	in := g.TriggerInbound()
	syncer, fires := trigger.TemporalBackend(g.TemporalClient().Client, g.Database(), v2workflow.SharedTaskQueue, in.Registry.IsPolled)
	return trigger.New(trigger.Deps{
		Logger:       g.Log,
		Config:       g.Cfg,
		Repo:         g.Database(),
		Syncer:       syncer,
		Fires:        fires,
		ControlPlane: hostedControlPlane(g.ServerProcessConfig.GetControlPlane()),
		Integrations: g.IntegrationSettings().Config,
		Sealer:       in.Vault,
		Catalog:      in.Registry,
		Intake:       in.Intake,
		Access:       triggerAccess(in),
		Senders:      g.TriggerSenders(),
		Options:      optionsChecker{svc: g.InputOptions()},
		Alerting:     alertingProvisioner(g.TriggerAlerting()),
		Drafter:      automationDrafter(g.TemporalClient().Client, v2workflow.SharedTaskQueue),
	})
}

// newConnector constructs the connector handler (internal/handlers/connector):
// grants for third-party MCP clients, in the same store the /mcp endpoint
// redeems them from.
func (g *Graph) newConnector() (*connector.Service, error) {
	return connector.New(connector.Deps{
		Logger:       g.Log,
		Config:       g.Cfg,
		Grants:       g.ConnectorGrants(),
		Tokens:       g.TokenAuthority(),
		Integrations: g.IntegrationSettings().Config,
	})
}

// newDaemonRegistry constructs the daemon_registry handler
// (internal/handlers/daemon_registry): browser-driven list/get/resolve/resume.
// The ToolsDaemonService stream is the daemon-gateway's.
func (g *Graph) newDaemonRegistry() (*daemon_registry.Service, error) {
	return daemon_registry.New(daemon_registry.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Repo:   g.Database(),
		Router: g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newFileSystem constructs the file_system handler
// (internal/handlers/file_system): a proxy to the connected daemon, which
// holds the files.
func (g *Graph) newFileSystem() (*file_system.Service, error) {
	return file_system.New(file_system.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Router: g.APIDaemonRouter().NATSDaemonRouter,
		Repo:   g.Database(),
	})
}

// newForge constructs the forge handler (internal/handlers/forge): a forge
// project's release/env/secret state, read off the DAEMON's filesystem
// through the router. Always mounted; RELIANT_DISABLE_FORGE_API is its mount
// option (apiMountOptions).
func (g *Graph) newForge() (*forge.Service, error) {
	return forge.New(forge.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Router: g.APIDaemonRouter().NATSDaemonRouter,
		Repo:   g.Database(),
	})
}

// newInbox constructs the inbox handler (internal/handlers/inbox).
func (g *Graph) newInbox() (*inbox.Service, error) {
	return inbox.New(inbox.Deps{
		Logger:  g.Log,
		Config:  g.Cfg,
		Repo:    g.Database(),
		Sweeper: g.WorktreeSweep(),
	})
}

// newMCP constructs the mcp handler (internal/handlers/mcp).
func (g *Graph) newMCP() (*mcp.Service, error) {
	return mcp.New(mcp.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Repo:   g.Database(),
		Router: g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newMessage constructs the message handler (internal/handlers/message).
func (g *Graph) newMessage() (*message.Service, error) {
	return message.New(message.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Repo:   g.Database(),
	})
}

// newModelEndpoint constructs the model_endpoint handler
// (internal/handlers/model_endpoint). Credentials stays nil: endpoint API keys
// have one home, the sealed connection store, and no
// modelendpoints.EndpointCredentials over it exists yet, so storing a key
// answers ErrCredentialStoreUnavailable.
func (g *Graph) newModelEndpoint() (*model_endpoint.Service, error) {
	return model_endpoint.New(model_endpoint.Deps{
		Logger:      g.Log,
		Config:      g.Cfg,
		Repo:        g.Database(),
		Router:      g.APIDaemonRouter().NATSDaemonRouter,
		Credentials: nil,
		Network:     g.ServerProcessConfig.GetNetwork(),
	})
}

// newPackageCommands constructs the package_commands handler
// (internal/handlers/package_commands): a browser-facing workspace service
// whose command discovery runs on the daemon's filesystem, through the router
// (run against the api-server's, it silently returned an empty command list).
func (g *Graph) newPackageCommands() (*package_commands.Service, error) {
	return package_commands.New(package_commands.Deps{
		Logger:    g.Log,
		Config:    g.Cfg,
		Router:    g.APIDaemonRouter().NATSDaemonRouter,
		Repo:      g.Database(),
		Processes: pkgmgr.NewService(),
	})
}

// newTask constructs the task handler (internal/handlers/task).
func (g *Graph) newTask() (*task.Service, error) {
	return task.New(task.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Repo:   g.Database(),
	})
}

// newPlan constructs the plan handler (internal/handlers/plan).
func (g *Graph) newPlan() (*plan.Service, error) {
	return plan.New(plan.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Repo:   g.Database(),
	})
}

// newPreset constructs the preset handler (internal/handlers/preset).
func (g *Graph) newPreset() (*preset.Service, error) {
	return preset.New(preset.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Repo:   g.Database(),
	})
}

// newProject constructs the project handler (internal/handlers/project). Only
// a deployment WITH a control plane can clone from GitHub on the user's
// behalf; self-hosted leaves ControlPlane nil.
func (g *Graph) newProject() (*project.Service, error) {
	return project.New(project.Deps{
		Logger:       g.Log,
		Config:       g.Cfg,
		Repo:         g.Database(),
		Router:       g.APIDaemonRouter().NATSDaemonRouter,
		ControlPlane: hostedControlPlane(g.ServerProcessConfig.GetControlPlane()),
	})
}

// newProviderAccount constructs the provider_account handler
// (internal/handlers/provider_account). Its usage refresher is the same
// refreshing token source the worker's poller uses, so a refresh never races
// a live request.
func (g *Graph) newProviderAccount() (*provider_account.Service, error) {
	return provider_account.New(provider_account.Deps{
		Logger:      g.Log,
		Config:      g.Cfg,
		Store:       g.Database(),
		ValidateKey: keyvalidate.Validate,
		UsageRefresher: &accountlimits.Fetcher{
			Store:  g.Database(),
			Tokens: drivers.RefreshingTokens{Store: g.Database()},
			// A refresh that lifts a bench wakes the user's blocked chats.
			OnCapacity: func(ctx context.Context, userID, _ string) {
				g.Chat().WakeUserChats(ctx, userID, "a usage refresh found a limited account serving again")
			},
		},
		ChatWaker: g.Chat(),
	})
}

// newQuestion constructs the question handler (internal/handlers/question).
func (g *Graph) newQuestion() (*question.Service, error) {
	return question.New(question.Deps{
		Logger:   g.Log,
		Config:   g.Cfg,
		Repo:     g.Database(),
		Signals:  g.PauseService(),
		Messages: g.Chat(),
	})
}

// newRepo constructs the repo handler (internal/handlers/repo).
func (g *Graph) newRepo() (*repo.Service, error) {
	return repo.New(repo.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Repo:   g.Database(),
		Router: g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newScenario constructs the scenario handler (internal/handlers/scenario).
func (g *Graph) newScenario() (*scenario.Service, error) {
	return scenario.New(scenario.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Repo:   g.Database(),
		Router: g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newSettings constructs the settings handler (internal/handlers/settings).
func (g *Graph) newSettings() (*settings.Service, error) {
	return settings.New(settings.Deps{
		Logger:                  g.Log,
		Config:                  g.Cfg,
		Repo:                    g.Database(),
		ControlPlane:            controlPlaneClient(g.ServerProcessConfig.GetControlPlane()),
		ExchangeClaudeCode:      claude.ExchangeClaudeAuthorizationCode,
		ExchangeCodexCode:       codex.ExchangeCodexAuthorizationCode,
		ExchangeAntigravityCode: antigravity.ExchangeAntigravityAuthorizationCode,
		ValidateKey:             keyvalidate.Validate,
		PollCopilotDevice:       copilot.PollDeviceAuthOnce,
		ClaudeProfile: func(ctx context.Context, accessToken string) (providerusage.ClaudeAccountProfile, error) {
			return providerusage.ClaudeProfile(ctx, nil, accessToken)
		},
		GitHubIdentity: settings.FetchGitHubUser,
		ChatWaker:      g.Chat(),
	})
}

// newStreaming constructs the streaming handler (internal/handlers/streaming).
func (g *Graph) newStreaming() (*streaminghandler.Service, error) {
	return streaminghandler.New(streaminghandler.Deps{
		Logger:      g.Log,
		Config:      g.Cfg,
		Repo:        g.Database(),
		Deltas:      g.StreamingHub().Hub,
		UserUpdates: g.UserUpdateHub(),
		ChatUpdates: g.ChatUpdateHub(),
	})
}

// newTerminal constructs the terminal handler (internal/handlers/terminal): a
// proxy to the connected daemon, which holds the sessions.
func (g *Graph) newTerminal() (*terminal.Service, error) {
	return terminal.New(terminal.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Router: g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newToken constructs the token handler (internal/handlers/token): the ONE
// token surface (daemon credentials and API tokens), a facade over the token
// authority. A control-plane authority clips grants itself.
func (g *Graph) newToken() (*token.Service, error) {
	cp := g.ServerProcessConfig.GetControlPlane()
	git, err := gitTokens(cp)
	if err != nil {
		return nil, err
	}
	return token.New(token.Deps{
		Logger:    g.Log,
		Config:    g.Cfg,
		Authority: g.TokenAuthority(),
		ControlPlane: token.TokenControlPlane{
			Issuer: g.APISettings().AuthorizationServer,
		},
		GitTokens:    git,
		GitBotTokens: git,
	})
}

// newToolCall constructs the tool_call handler (internal/handlers/tool_call).
func (g *Graph) newToolCall() (*tool_call.Service, error) {
	return tool_call.New(tool_call.Deps{
		Logger:   g.Log,
		Config:   g.Cfg,
		Repo:     g.Database(),
		Temporal: g.TemporalClient().Client,
		Router:   g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newWorkflow constructs the workflow handler (internal/handlers/workflow).
// The trigger handler stops the schedules of automations deleted with a
// workflow.
func (g *Graph) newWorkflow() (*workflowhandler.Service, error) {
	return workflowhandler.New(workflowhandler.Deps{
		Logger:      g.Log,
		Config:      g.Cfg,
		Repo:        g.Database(),
		Router:      g.APIDaemonRouter().NATSDaemonRouter,
		Automations: g.Trigger(),
	})
}
