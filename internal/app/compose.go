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
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/internal/agentruns"
	"github.com/reliant-labs/reliant/internal/auth"
	reliantconfig "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemonevents"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/handlers/catalog"
	"github.com/reliant-labs/reliant/internal/handlers/daemon"
	"github.com/reliant-labs/reliant/internal/handlers/system"
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/servergateway"
	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggers/workflowevent"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/reliant-labs/reliant/internal/workersetup"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
)

func (g *Graph) newDBRepo() (*DBRepo, error) {
	return NewDBRepo(DBRepoDeps{
		Database: g.DatabaseConfig,
		DataDir:  g.ServerProcessConfig.GetDataDir(),
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
		Repo:         g.DBRepo(),
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
		Repo:            g.DBRepo().Repo,
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
		Repo:        g.DBRepo(),
		ToolsDaemon: g.ToolsDaemon(),
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
		Repo:        g.DBRepo(),
		ToolsDaemon: g.ToolsDaemon(),
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
		Tools:              nil, // optional — no provider
		TagPrefs:           nil, // optional — no provider
		LocalModels:        nil, // optional — no provider
		Search:             nil, // optional — no provider
		InputOptions:       nil, // optional — no provider
		IntegrationMethods: nil, // optional — no provider
	})
}

// newDaemon constructs the daemon handler (internal/handlers/daemon).
func (g *Graph) newDaemon() (*daemon.Service, error) {
	return daemon.New(daemon.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Router: nil, // optional — no provider
	})
}

// newSystem constructs the system handler (internal/handlers/system).
func (g *Graph) newSystem() (*system.Service, error) {
	return system.New(system.Deps{
		Logger:        g.Log,
		Config:        g.Cfg,
		Repo:          nil,             // optional — no provider
		Temporal:      nil,             // optional — no provider
		NATSConnected: nil,             // optional — no provider
		StreamingHub:  nil,             // optional — no provider
		OAuthProvider: auth.Provider{}, // optional — no provider: the zero value
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
		Settings:      g.ProcessSettings(),
		Repo:          g.DBRepo(),
		ControlPlane:  g.ServerProcessConfig.GetControlPlane(),
		Analytics:     g.ServerProcessConfig.GetAnalytics(),
		Sentry:        g.ServerProcessConfig.GetSentry(),
		Observability: g.ServerProcessConfig.GetObservability(),
	})
}

func (g *Graph) newPayloadStore() (*claimcheck.PostgresStore, error) {
	return NewPayloadStore(PayloadStoreDeps{
		Repo: g.DBRepo(),
	})
}

func (g *Graph) newTemporalClient() (*TemporalClient, error) {
	return NewTemporalClient(TemporalClientDeps{
		Ctx:          g.Ctx,
		Temporal:     g.ServerProcessConfig.GetTemporal(),
		PayloadStore: g.PayloadStore(),
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
		Repo:  g.DBRepo(),
		Users: g.UserUpdateHub(),
		Chats: g.ChatUpdateHub(),
	})
}

func (g *Graph) newVault() (vault.Service, error) {
	return NewVault(VaultDeps{
		Ctx:          g.Ctx,
		Repo:         g.DBRepo(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DataDir:      g.ServerProcessConfig.GetDataDir(),
		Integrations: g.IntegrationSettings(),
	})
}

func (g *Graph) newPauseService() (*v2workflow.PauseService, error) {
	return NewPauseService(PauseServiceDeps{
		Temporal: g.TemporalClient(),
		Repo:     g.DBRepo(),
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
		Repo:         g.DBRepo(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
	})
}

func (g *Graph) newWorkerIntegrationCredentials() (*WorkerIntegrationCredentials, error) {
	return NewWorkerIntegrationCredentials(WorkerIntegrationCredentialsDeps{
		Repo:         g.DBRepo(),
		Vault:        g.Vault(),
		Integrations: g.IntegrationSettings(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
	})
}

func (g *Graph) newWorkerCatalogSearch() (*WorkerCatalogSearch, error) {
	return NewWorkerCatalogSearch(WorkerCatalogSearchDeps{
		Repo:        g.DBRepo(),
		Credentials: g.WorkerIntegrationCredentials(),
	})
}

func (g *Graph) newWriteLedger() (writeledger.Service, error) {
	return NewWriteLedger(WriteLedgerDeps{
		Repo: g.DBRepo(),
	})
}

func (g *Graph) newStoredConfigProvider() (*reliantconfig.StoredConfigProvider, error) {
	return NewStoredConfigProvider(StoredConfigProviderDeps{
		Repo: g.DBRepo(),
	})
}

func (g *Graph) newRunLifecycle() (*runs.Service, error) {
	return NewRunLifecycle(RunLifecycleDeps{
		Repo:     g.DBRepo(),
		Temporal: g.TemporalClient(),
		Pause:    g.PauseService(),
	})
}

func (g *Graph) newRunLauncher() (*launch.Launcher, error) {
	return NewRunLauncher(RunLauncherDeps{
		Repo:     g.DBRepo(),
		Temporal: g.TemporalClient(),
		Runs:     g.RunLifecycle(),
	})
}

func (g *Graph) newWorkerChat() (*WorkerChat, error) {
	return NewWorkerChat(WorkerChatDeps{
		Logger:   g.Log,
		Config:   g.Cfg,
		Repo:     g.DBRepo(),
		Temporal: g.TemporalClient(),
		Pause:    g.PauseService(),
		Router:   g.WorkerDaemonRouter(),
	})
}

func (g *Graph) newWorkerRun() (*WorkerRun, error) {
	return NewWorkerRun(WorkerRunDeps{
		Logger: g.Log,
		Config: g.Cfg,
		Repo:   g.DBRepo(),
		Chats:  g.WorkerChat(),
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
		Repo:         g.DBRepo(),
		Temporal:     g.TemporalClient(),
		Vault:        g.Vault(),
		Pollers:      g.TriggerPollers(),
		Integrations: g.IntegrationSettings(),
	})
}

func (g *Graph) newWorkerToolsFactory() (*WorkerToolsFactory, error) {
	return NewWorkerToolsFactory(WorkerToolsFactoryDeps{
		Repo:          g.DBRepo(),
		ControlPlane:  g.ServerProcessConfig.GetControlPlane(),
		Temporal:      g.TemporalClient(),
		AgentRuns:     g.AgentRuns(),
		Triggers:      g.WorkerTrigger(),
		Credentials:   g.WorkerIntegrationCredentials(),
		WriteLedger:   g.WriteLedger(),
		CatalogSearch: g.WorkerCatalogSearch(),
		Router:        g.WorkerDaemonRouter(),
	})
}

func (g *Graph) newRemoteExecutor() (*toolexec.RemoteExecutor, error) {
	return NewRemoteExecutor(RemoteExecutorDeps{
		Router: g.WorkerDaemonRouter(),
		Tools:  g.WorkerToolsFactory(),
	})
}

func (g *Graph) newAccountRecorder() (*AccountRecorder, error) {
	return NewAccountRecorder(AccountRecorderDeps{
		Repo: g.DBRepo(),
	})
}

func (g *Graph) newTemporalWorker() (*workersetup.Worker, error) {
	return NewTemporalWorker(TemporalWorkerDeps{
		Repo:           g.DBRepo(),
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
		Recorder:       g.AccountRecorder(),
	})
}

func (g *Graph) newWorkflowEventRelay() (*workflowevent.Relay, error) {
	return NewWorkflowEventRelay(WorkflowEventRelayDeps{
		Repo:     g.DBRepo(),
		Temporal: g.TemporalClient(),
	})
}

func (g *Graph) newAccountUsagePoller() (*accountlimits.Poller, error) {
	return NewAccountUsagePoller(AccountUsagePollerDeps{
		Repo: g.DBRepo(),
	})
}

func (g *Graph) newWriteLedgerPrune() (*WriteLedgerPrune, error) {
	return NewWriteLedgerPrune(WriteLedgerPruneDeps{
		Ledger: g.WriteLedger(),
		Repo:   g.DBRepo(),
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
