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
	"time"

	"github.com/reliant-labs/reliant/internal/chatprlinks"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/credentials/automationtokens"
	"github.com/reliant-labs/reliant/internal/credentials/connectionvault"
	"github.com/reliant-labs/reliant/internal/credentials/orgaccounts"
	"github.com/reliant-labs/reliant/internal/credentials/provideraccounts"
	"github.com/reliant-labs/reliant/internal/daemonmirror"
	"github.com/reliant-labs/reliant/internal/questions"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/serverkit"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/accountpurge"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/agentruns"
	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/chatupdates"
	reliantconfig "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemonevents"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/egresslimit"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/handlers/account"
	"github.com/reliant-labs/reliant/internal/handlers/approval"
	"github.com/reliant-labs/reliant/internal/handlers/attachment"
	"github.com/reliant-labs/reliant/internal/handlers/automation_takeover"
	"github.com/reliant-labs/reliant/internal/handlers/background"
	"github.com/reliant-labs/reliant/internal/handlers/catalog"
	"github.com/reliant-labs/reliant/internal/handlers/chat"
	"github.com/reliant-labs/reliant/internal/handlers/chat_trace"
	"github.com/reliant-labs/reliant/internal/handlers/connection"
	"github.com/reliant-labs/reliant/internal/handlers/connector"
	"github.com/reliant-labs/reliant/internal/handlers/coverage_source"
	"github.com/reliant-labs/reliant/internal/handlers/daemon"
	"github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
	"github.com/reliant-labs/reliant/internal/handlers/file_system"
	"github.com/reliant-labs/reliant/internal/handlers/forge"
	svcGtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
	"github.com/reliant-labs/reliant/internal/handlers/inbox"
	"github.com/reliant-labs/reliant/internal/handlers/mcp"
	"github.com/reliant-labs/reliant/internal/handlers/message"
	"github.com/reliant-labs/reliant/internal/handlers/model_endpoint"
	"github.com/reliant-labs/reliant/internal/handlers/org_policy_impact"
	"github.com/reliant-labs/reliant/internal/handlers/org_provider_account"
	"github.com/reliant-labs/reliant/internal/handlers/org_usage"
	"github.com/reliant-labs/reliant/internal/handlers/package_commands"
	"github.com/reliant-labs/reliant/internal/handlers/pipeline"
	"github.com/reliant-labs/reliant/internal/handlers/plan"
	"github.com/reliant-labs/reliant/internal/handlers/preset"
	"github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/handlers/provider_account"
	"github.com/reliant-labs/reliant/internal/handlers/pull_request_record"
	"github.com/reliant-labs/reliant/internal/handlers/question"
	"github.com/reliant-labs/reliant/internal/handlers/repo"
	"github.com/reliant-labs/reliant/internal/handlers/run"
	"github.com/reliant-labs/reliant/internal/handlers/scenario"
	"github.com/reliant-labs/reliant/internal/handlers/settings"
	"github.com/reliant-labs/reliant/internal/handlers/sharing"
	streaminghandler "github.com/reliant-labs/reliant/internal/handlers/streaming"
	"github.com/reliant-labs/reliant/internal/handlers/system"
	"github.com/reliant-labs/reliant/internal/handlers/task"
	"github.com/reliant-labs/reliant/internal/handlers/terminal"
	"github.com/reliant-labs/reliant/internal/handlers/token"
	"github.com/reliant-labs/reliant/internal/handlers/tool_call"
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	"github.com/reliant-labs/reliant/internal/handlers/usage"
	workflowhandler "github.com/reliant-labs/reliant/internal/handlers/workflow"
	"github.com/reliant-labs/reliant/internal/handlers/worktree"
	"github.com/reliant-labs/reliant/internal/handlers/worktree_reclaim"
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
	"github.com/reliant-labs/reliant/internal/llmusage"
	"github.com/reliant-labs/reliant/internal/orgmove"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/privacy"
	"github.com/reliant-labs/reliant/internal/projectaccess"
	"github.com/reliant-labs/reliant/internal/projectpurge"
	"github.com/reliant-labs/reliant/internal/providerbackoff"
	"github.com/reliant-labs/reliant/internal/queueddelivery"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/seatmatch"
	"github.com/reliant-labs/reliant/internal/servergateway"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggerowner"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/recorddue"
	"github.com/reliant-labs/reliant/internal/triggers/workflowevent"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/reliant-labs/reliant/internal/workersetup"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflowcheck"
	"github.com/reliant-labs/reliant/internal/workflowdrafts"
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

func (g *Graph) newOrgResolver() (*orgs.Resolver, error) {
	return NewOrgResolver(OrgResolverDeps{
		Authority: g.TokenAuthority(),
	})
}

func (g *Graph) newOrgPolicyWatcher() (*OrgPolicyWatcher, error) {
	return NewOrgPolicyWatcher(OrgPolicyWatcherDeps{
		Policies: g.OrgPolicyResolver(),
		DB:       g.Database(),
		Backoff:  g.Providerbackoff(),
		Temporal: g.TemporalClient(),
	})
}

func (g *Graph) newOrgPolicyResolver() (*orgpolicy.Resolver, error) {
	return NewOrgPolicyResolver(OrgPolicyResolverDeps{
		Authority:   g.TokenAuthority(),
		Enforcement: g.ServerProcessConfig.GetOrgPolicyEnforcement(),
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
		Refetch:         g.chatUpdates(),
		Logger:          g.Log,
		Config:          g.Cfg,
		Repo:            g.Database(),
		Projects:        store.NewProjectExtStore(g.Database().ORM()),
		Installs:        store.NewProjectDaemonExtStore(g.Database().ORM()),
		Attachments:     store.NewDaemonAttachmentExtStore(g.Database().ORM()),
		LocalModels:     store.NewDaemonExtStore(g.Database().ORM()),
		Checkouts:       store.NewProjectDaemonDetachmentExtStore(g.Database().ORM()),
		Pools:           store.NewMachinePoolExtStore(g.Database().ORM()),
		UserUpdateHub:   g.UserUpdateHub(),
		StatePublisher:  g.DaemonStatePublisher(),
		ResultPublisher: g.ToolResultPublisher(),
		MachineStats:    machineStatsSelection(g.GatewayConfig),
		ClientVersions:  g.ServerProcessConfig.GetClientVersions(),
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
		// Every request's context carries membership (authz).
		Membership: g.OrgResolver(),
		// ... and the org policy resolver beside it (orgpolicy).
		Policies: g.OrgPolicyResolver(),
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
		Mirror:      g.Daemonmirror(),
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

func (g *Graph) newDisconnectSubscriber() (*DisconnectSubscriber, error) {
	return NewDisconnectSubscriber(DisconnectSubscriberDeps{
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
		LocalModels:        local.NewRepoDirectory(g.Database(), store.NewModelEndpointExtStore(g.Database().ORM())),
		Search:             g.APICatalogSearch(),
		InputOptions:       g.InputOptions(),
		IntegrationMethods: catalog.IntegrationMethodsFrom(g.APIConnections().Service),
		Orgs:               g.OrgResolver(),
		Gate:               nil, // optional — no provider
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
		Logger:         g.Log,
		Config:         g.Cfg,
		Repo:           g.Database(),
		Temporal:       g.TemporalClient().Client,
		NATSConnected:  g.NATSConn().Conn.IsConnected,
		StreamingHub:   g.StreamingHub().Hub,
		OAuthProvider:  g.APISettings().OAuthProvider,
		ClientVersions: g.ServerProcessConfig.GetClientVersions(),
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
		LLM:          g.ServerProcessConfig.GetLlm(),
		Preview:      g.ServerProcessConfig.GetPreview(),
		Integrations: g.IntegrationSettings(),
	})
}

func (g *Graph) newProcessRuntime() (*ProcessRuntime, error) {
	return NewProcessRuntime(ProcessRuntimeDeps{
		Settings:         g.ProcessSettings(),
		ControlPlane:     g.ServerProcessConfig.GetControlPlane(),
		Network:          g.ServerProcessConfig.GetNetwork(),
		Analytics:        g.ServerProcessConfig.GetAnalytics(),
		DB:               g.Database(),
		ProviderAccounts: g.CredentialsProvideraccounts(),
		Privacy:          g.Privacy(),
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

// chatUpdates is the one writer of the chat and user update streams: it
// appends a refetch and publishes it to the hubs after its transaction commits
// (internal/chatupdates).
func (g *Graph) chatUpdates() chatupdates.Service {
	return chatupdates.NewFromORM(g.Database().ORM(), g.Database(), hubPublisher{users: g.UserUpdateHub(), chats: g.ChatUpdateHub()})
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
		Updates:  g.chatUpdates(),
	})
}

func (g *Graph) newTriggerPollers() (*webhook.Registry, error) {
	return NewTriggerPollers(TriggerPollersDeps{
		Integrations: g.IntegrationSettings(),
	})
}

func (g *Graph) newWorkerDaemonRouter() (*WorkerDaemonRouter, error) {
	return NewWorkerDaemonRouter(WorkerDaemonRouterDeps{
		NATS:             g.NATSConn(),
		ControlPlane:     g.ServerProcessConfig.GetControlPlane(),
		DB:               g.Database(),
		AutomationTokens: g.CredentialsAutomationtokens(),
	})
}

func (g *Graph) newWorkerIntegrationCredentials() (*WorkerIntegrationCredentials, error) {
	return NewWorkerIntegrationCredentials(WorkerIntegrationCredentialsDeps{
		ConnectionVault: g.CredentialsConnectionvault(),
		Vault:           g.Vault(),
		Integrations:    g.IntegrationSettings(),
		ControlPlane:    g.ServerProcessConfig.GetControlPlane(),
		DB:              g.Database(),
	})
}

func (g *Graph) newWorkerCatalogSearch() (*WorkerCatalogSearch, error) {
	return NewWorkerCatalogSearch(WorkerCatalogSearchDeps{
		ConnectionVault: g.CredentialsConnectionvault(),
		Credentials:     g.WorkerIntegrationCredentials(),
		DB:              g.Database(),
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
		Backoff:  g.Providerbackoff(),
	})
}

func (g *Graph) newRunLauncher() (*launch.Launcher, error) {
	return NewRunLauncher(RunLauncherDeps{
		Temporal: g.TemporalClient(),
		Runs:     g.RunLifecycle(),
		DB:       g.Database(),
		Router:   g.WorkerDaemonRouter(),
		Projects: g.Projectaccess(),
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
		Updates:  g.chatUpdates(),
		Backoff:  g.Providerbackoff(),
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
		ConnectionVault: g.CredentialsConnectionvault(),
		Logger:          g.Log,
		Config:          g.Cfg,
		Temporal:        g.TemporalClient(),
		Vault:           g.Vault(),
		Pollers:         g.TriggerPollers(),
		Integrations:    g.IntegrationSettings(),
		DB:              g.Database(),
		ControlPlane:    g.ServerProcessConfig.GetControlPlane(),
		AutomationKeys:  g.CredentialsAutomationtokens(),
		Owners:          g.Triggerowner(),
		Gate:            nil, // optional — no provider
	})
}

func (g *Graph) newWorkerToolsFactory() (*WorkerToolsFactory, error) {
	return NewWorkerToolsFactory(WorkerToolsFactoryDeps{
		Updates:           g.chatUpdates(),
		ConnectionVault:   g.CredentialsConnectionvault(),
		Network:           g.ServerProcessConfig.GetNetwork(),
		Temporal:          g.TemporalClient(),
		AgentRuns:         g.AgentRuns(),
		Triggers:          g.WorkerTrigger(),
		Credentials:       g.WorkerIntegrationCredentials(),
		WriteLedger:       g.IntegrationsWriteledger(),
		CatalogSearch:     g.WorkerCatalogSearch(),
		Router:            g.WorkerDaemonRouter(),
		DB:                g.Database(),
		Membership:        g.OrgResolver(),
		Logger:            g.Log,
		Config:            g.Cfg,
		Backoff:           g.Providerbackoff(),
		ChangeController:  g.ChangeController(),
		MachineController: g.MachineController(),
		WorkflowDrafts:    g.Workflowdrafts(),
	})
}

func (g *Graph) newMachineController() (tools.MachineController, error) {
	return NewMachineController(MachineControllerDeps{
		DB:     g.Database(),
		Router: g.WorkerDaemonRouter(),
		Mirror: g.Daemonmirror(),
		Logger: g.Log,
		Config: g.Cfg,
	})
}

func (g *Graph) newOrgMoves() (orgmove.Service, error) {
	return NewOrgMoves(OrgMovesDeps{
		DB:           g.Database(),
		Router:       g.WorkerDaemonRouter(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		Triggers:     g.WorkerTrigger(),
		Membership:   g.OrgResolver(),
		Updates:      g.chatUpdates(),
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
		Accounts: g.CredentialsProvideraccounts(),
	})
}

func (g *Graph) newTemporalWorker() (*workersetup.Worker, error) {
	return NewTemporalWorker(TemporalWorkerDeps{
		Checks:           g.RecordOnlyChecks(),
		ConnectionVault:  g.CredentialsConnectionvault(),
		Updates:          g.chatUpdates(),
		ProviderAccounts: g.CredentialsProvideraccounts(),
		OrgAccounts:      g.CredentialsOrgaccounts(),
		Temporal:         g.TemporalClient(),
		Streaming:        g.StreamingHub(),
		Tools:            g.WorkerToolsFactory(),
		Executor:         g.RemoteExecutor(),
		Router:           g.WorkerDaemonRouter(),
		ConfigProvider:   g.StoredConfigProvider(),
		Launcher:         g.RunLauncher(),
		Runs:             g.RunLifecycle(),
		Credentials:      g.WorkerIntegrationCredentials(),
		WriteLedger:      g.IntegrationsWriteledger(),
		Pollers:          g.TriggerPollers(),
		ControlPlane:     g.ServerProcessConfig.GetControlPlane(),
		Network:          g.ServerProcessConfig.GetNetwork(),
		Pipeline:         g.ServerProcessConfig.GetPipeline(),
		Recorder:         g.AccountRecorder(),
		DB:               g.Database(),
		// Read here, not in workersetup, so the worker root's config reach
		// names them (pkg/config Roots).
		WorkflowTaskSlots:   g.WorkerConfig.GetMaxConcurrentWorkflowTasks(),
		WorkflowTaskPollers: g.WorkerConfig.GetWorkflowTaskPollers(),
		Worker:              g.WorkerConfig,
		NATS:                g.NATSConn(),
		// Every activity's context carries membership (authz).
		Membership: g.OrgResolver(),
		// ... and the org policy resolver beside it (orgpolicy).
		Policies: g.OrgPolicyResolver(),
		OrgMoves: g.OrgMoves(),
	})
}

func (g *Graph) newWorkflowEventRelay() (*workflowevent.Relay, error) {
	return NewWorkflowEventRelay(WorkflowEventRelayDeps{
		Temporal: g.TemporalClient(),
		DB:       g.Database(),
	})
}

func (g *Graph) newTriggerDeliveryRelay() (*TriggerDeliveryRelay, error) {
	return NewTriggerDeliveryRelay(TriggerDeliveryRelayDeps{
		ConnectionVault: g.CredentialsConnectionvault(),
		DB:              g.Database(), Temporal: g.TemporalClient(), Integrations: g.IntegrationSettings(),
		Membership: g.OrgResolver(), Policies: g.OrgPolicyResolver(),
		Pipeline:     g.ServerProcessConfig.GetPipeline(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
	})
}

func (g *Graph) newRecordDueScanner() (recorddue.Service, error) {
	return NewRecordDueScanner(RecordDueScannerDeps{
		DB: g.Database(), Temporal: g.TemporalClient(), Pipeline: g.ServerProcessConfig.GetPipeline(),
		Membership: g.OrgResolver(), Policies: g.OrgPolicyResolver(),
	})
}

func (g *Graph) newMergeQueue() (*MergeQueue, error) {
	return NewMergeQueue(MergeQueueDeps{
		DB:     g.Database(),
		Router: g.WorkerDaemonRouter(),
		// Read here, not in the provider, so the worker root's config reach
		// names them (pkg/config Roots).
		Pipeline:     g.ServerProcessConfig.GetPipeline(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DataDir:      g.ServerProcessConfig.GetDataDir(),
	})
}

func (g *Graph) newAccountUsagePoller() (*accountlimits.Poller, error) {
	return NewAccountUsagePoller(AccountUsagePollerDeps{
		DB:       g.Database(),
		Accounts: g.CredentialsProvideraccounts(),
		Temporal: g.TemporalClient(),
		Backoff:  g.Providerbackoff(),
	})
}

func (g *Graph) newWriteLedgerPrune() (*WriteLedgerPrune, error) {
	return NewWriteLedgerPrune(WriteLedgerPruneDeps{
		Ledger: g.IntegrationsWriteledger(),
		DB:     g.Database(),
	})
}

func (g *Graph) newProjectPurge() (*ProjectPurge, error) {
	return NewProjectPurge(ProjectPurgeDeps{Purges: g.Projectpurge()})
}

func (g *Graph) newLedgerRetention() (*LedgerRetention, error) {
	return NewLedgerRetention(LedgerRetentionDeps{
		DB:                     g.Database(),
		ChatUpdateRetention:    g.WorkerConfig.GetChatUpdateRetention().AsDuration(),
		StepExecutionRetention: g.WorkerConfig.GetStepExecutionRetention().AsDuration(),
		ObservationRetention:   g.WorkerConfig.GetNodeObservationRetention().AsDuration(),
		StartDelay:             g.WorkerConfig.GetLedgerRetentionStartDelay().AsDuration(),
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
		Sessions: g.SignInSessions(),
	})
}

func (g *Graph) newSignInSessions() (*SignInSessions, error) {
	return NewSignInSessions(SignInSessionsDeps{
		API:    g.ApiServerConfig,
		DB:     g.Database(),
		Logger: g.Log,
	})
}

func (g *Graph) newControlPlaneAccounts() (*ControlPlaneAccounts, error) {
	return NewControlPlaneAccounts(ControlPlaneAccountsDeps{
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
	})
}

func (g *Graph) newUserChain() (*grpcserver.UserChain, error) {
	return NewUserChain(UserChainDeps{
		Auth:                g.UserAuth(),
		AllowedEmailDomains: g.ApiServerConfig.GetAllowedEmailDomains(),
		Logger:              g.Log,
		ClientVersions:      g.ServerProcessConfig.GetClientVersions(),
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
		OAuthScopes:  g.ApiServerConfig.GetMcpOauthScopes(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
		DB:           g.Database(),
		Membership:   g.OrgResolver(),
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
		// Every request's context carries membership (authz).
		Membership: g.OrgResolver(),
		// ... and the org policy resolver beside it (orgpolicy).
		Policies:       g.OrgPolicyResolver(),
		ClientVersions: g.ServerProcessConfig.GetClientVersions(),
		// Native sign-in (/auth/*), only with a configured identity.
		SignIn:       g.SignInSessions(),
		Accounts:     g.ControlPlaneAccounts(),
		Auth:         g.UserAuth(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
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
		ConnectionVault: g.CredentialsConnectionvault(),
		Vault:           g.Vault(),
		Integrations:    g.IntegrationSettings(),
		AppOrigins:      g.ApiServerConfig.GetCorsAllowedOrigins(),
		ControlPlane:    g.ServerProcessConfig.GetControlPlane(),
		DB:              g.Database(),
		// Off until every replica and the rollback target read org
		// connections (org-sharing.md §9, D13).
		OrgConnections: APIOrgConnections(g.ApiServerConfig),
	})
}

func (g *Graph) newAPICatalogSearch() (*APICatalogSearch, error) {
	return NewAPICatalogSearch(APICatalogSearchDeps{
		ConnectionVault: g.CredentialsConnectionvault(),
		ControlPlane:    g.ServerProcessConfig.GetControlPlane(),
		DB:              g.Database(),
	})
}

func (g *Graph) newInputOptions() (inputoptions.Service, error) {
	return NewInputOptions(InputOptionsDeps{
		ConnectionVault: g.CredentialsConnectionvault(),
		Connections:     g.APIConnections(),
		ControlPlane:    g.ServerProcessConfig.GetControlPlane(),
		DB:              g.Database(),
	})
}

func (g *Graph) newGitHubAccess() (*ghaccess.Refresher, error) {
	return NewGitHubAccess(GitHubAccessDeps{
		ConnectionVault: g.CredentialsConnectionvault(),
		Vault:           g.Vault(),
		ControlPlane:    g.ServerProcessConfig.GetControlPlane(),
		Integrations:    g.IntegrationSettings(),
		DB:              g.Database(),
	})
}

func (g *Graph) newTriggerSenders() (TriggerSenders, error) {
	return NewTriggerSenders(TriggerSendersDeps{
		ConnectionVault: g.CredentialsConnectionvault(),
		Vault:           g.Vault(),
		ControlPlane:    g.ServerProcessConfig.GetControlPlane(),
		Integrations:    g.IntegrationSettings(),
		DB:              g.Database(),
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
		Updates:      g.chatUpdates(),
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
		Reconciler: g.ServerProcessConfig.GetReconciler(),
		Runs:       g.RunLifecycle(),
	})
}

func (g *Graph) newReliantKeySweep() (*ReliantKeySweep, error) {
	return NewReliantKeySweep(ReliantKeySweepDeps{
		Runtime: g.ProcessRuntime(),
	})
}

func (g *Graph) newLLMUsageRetention() (*LLMUsageRetention, error) {
	return NewLLMUsageRetention(LLMUsageRetentionDeps{
		Ledger: g.Llmusage(),
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
		Account:            g.Account(),
		Approval:           g.Approval(),
		Attachment:         g.Attachment(),
		Background:         g.Background(),
		Connection:         g.Connection(),
		Worktree:           g.Worktree(),
		Chat:               g.Chat(),
		Run:                g.Run(),
		Trigger:            g.Trigger(),
		Catalog:            g.Catalog(),
		Connector:          g.Connector(),
		Daemon:             g.Daemon(),
		DaemonRegistry:     g.DaemonRegistry(),
		FileSystem:         g.FileSystem(),
		Forge:              g.Forge(),
		Inbox:              g.Inbox(),
		MCP:                g.MCP(),
		Message:            g.Message(),
		ModelEndpoint:      g.ModelEndpoint(),
		PackageCommands:    g.PackageCommands(),
		Task:               g.Task(),
		Plan:               g.Plan(),
		Preset:             g.Preset(),
		Project:            g.Project(),
		ProviderAccount:    g.ProviderAccount(),
		Question:           g.Question(),
		Repo:               g.Repo(),
		Scenario:           g.Scenario(),
		Settings:           g.Settings(),
		Streaming:          g.Streaming(),
		System:             g.System(),
		Terminal:           g.Terminal(),
		Token:              g.Token(),
		ToolCall:           g.ToolCall(),
		Workflow:           g.Workflow(),
		Logger:             g.Log,
		Options:            apiMountOptions(g.ApiServerConfig.GetDisableForgeApi()),
		Usage:              g.Usage(),
		WorktreeReclaim:    g.WorktreeReclaim(),
		Sharing:            g.Sharing(),
		AutomationTakeover: g.AutomationTakeover(),
		OrgProviderAccount: g.OrgProviderAccount(),
		OrgUsage:           g.OrgUsage(),
		OrgPolicyImpact:    g.OrgPolicyImpact(),
		CoverageSource:     g.CoverageSource(),
		PullRequestRecord:  g.PullRequestRecord(),
		ChatTrace:          g.ChatTrace(),
		Pipeline:           g.Pipeline(),
		SvcGtm:             g.SvcGtm(),
	})
}

// newAccount constructs the account handler (internal/handlers/account). Its
// purge is an ordered multi-statement transaction across every user-owned
// table, with each credential package deleting its own rows inside it. The
// control plane is wired only when this deployment HAS one: self-hosted
// reliant has no platform account to delete.
func (g *Graph) newAccount() (*account.Service, error) {
	purger, err := accountpurge.New(accountpurge.Deps{
		DB:          g.Database().ORM(),
		Automation:  g.CredentialsAutomationtokens(),
		Accounts:    g.CredentialsProvideraccounts(),
		Connections: g.CredentialsConnectionvault(),
	})
	if err != nil {
		return nil, err
	}
	return account.New(account.Deps{
		Logger:       g.Log,
		Config:       g.Cfg,
		Purger:       purger,
		ControlPlane: g.CallerControlPlane().Client(),
	})
}

// newApproval constructs the approval handler (internal/handlers/approval).
func (g *Graph) newApproval() (*approval.Service, error) {
	return approval.New(approval.Deps{
		Logger:      g.Log,
		Config:      g.Cfg,
		Repo:        g.Database(),
		Chats:       store.NewChatExtStore(g.Database().ORM()),
		Signals:     g.PauseService(),
		Connections: g.APIConnections().Service,
	})
}

// newAttachment constructs the attachment handler (internal/handlers/attachment).
func (g *Graph) newAttachment() (*attachment.Service, error) {
	return attachment.New(attachment.Deps{
		Logger:      g.Log,
		Config:      g.Cfg,
		Attachments: store.NewAttachmentExtStore(g.Database().ORM()),
		API:         g.ApiServerConfig,
		DB:          g.ORM(),
		Orgs:        g.OrgResolver(),
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
	deps := connection.Deps{
		Logger:        g.Log,
		Config:        g.Cfg,
		Connections:   g.APIConnections().Service,
		OrgGitHubApps: nil, // optional — no provider
	}
	// The org's linked GitHub App installations, from control-plane: none
	// when self-hosted.
	apps, err := gitTokens(g.ServerProcessConfig.GetControlPlane())
	if err != nil {
		return nil, err
	}
	if apps != nil {
		deps.OrgGitHubApps = apps
	}
	return connection.New(deps)
}

// newWorktree constructs the worktree handler (internal/handlers/worktree).
func (g *Graph) newWorktree() (*worktree.Service, error) {
	credentials, err := gitTokens(g.ServerProcessConfig.GetControlPlane())
	if err != nil {
		return nil, err
	}
	return worktree.New(worktree.Deps{
		Refetch:        g.chatUpdates(),
		GitCredentials: recoveryCredentials{client: credentials},
		Logger:         g.Log,
		Config:         g.Cfg,
		Repo:           g.Database(),
		Chats:          store.NewChatsWithActivityExtStore(g.Database().ORM()),
		Rows:           store.NewWorktreeExtStore(g.Database().ORM()),
		Projects:       projectaccess.NewFromORM(g.Database().ORM()),
		ProjectRows:    store.NewProjectExtStore(g.Database().ORM()),
		Repos:          store.NewRepoExtStore(g.Database().ORM()),
		Daemons:        store.NewDaemonExtStore(g.Database().ORM()),
		Recoveries:     store.NewWorktreeRecoveryExtStore(g.Database().ORM()),
		Router:         g.APIDaemonRouter().NATSDaemonRouter,
		Temporal:       g.TemporalClient().Client,
		Settler:        g.WorktreeSweep(),
		PRLinks:        g.Chatprlinks(),
	})
}

// newChat constructs the chat handler (internal/handlers/chat).
func (g *Graph) newChat() (*chat.Service, error) {
	return chat.New(chat.Deps{
		Logger:           g.Log,
		Config:           g.Cfg,
		Repo:             g.Database(),
		TriggerReads:     store.NewTriggerEventExtStore(g.Database().ORM()),
		Chats:            chataccess.NewFromORM(g.Database().ORM()),
		ChatRows:         store.NewChatExtStore(g.Database().ORM()),
		ChatViews:        store.NewChatsWithActivityExtStore(g.Database().ORM()),
		QuestionResolver: questions.NewFromRepo(g.Database()),
		Workflows:        store.NewWorkflowExtStore(g.Database().ORM()),
		Worktrees:        store.NewWorktreeExtStore(g.Database().ORM()),
		ProjectRows:      store.NewProjectExtStore(g.Database().ORM()), TriggerEvents: store.NewTriggerEventExtStore(g.Database().ORM()),
		Questions:     store.NewQuestionExtStore(g.Database().ORM()),
		Spawns:        store.NewToolCallExtStore(g.Database().ORM()),
		Selections:    store.NewChatSelectionExtStore(g.Database().ORM()),
		Messages:      store.NewMessageExtStore(g.Database().ORM()),
		ToolBlocks:    store.NewMessageContentBlockExtStore(g.Database().ORM()),
		Senders:       store.NewThreadExtStore(g.Database().ORM()),
		Backoff:       g.Providerbackoff(),
		Temporal:      g.TemporalClient().Client,
		Pause:         g.PauseService(),
		Router:        g.APIDaemonRouter().NATSDaemonRouter,
		LocalModels:   local.NewRepoDirectory(g.Database(), store.NewModelEndpointExtStore(g.Database().ORM())),
		Orgs:          g.OrgResolver(),
		Mailbox:       agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(g.Database().ORM()), Announcer: g.chatUpdates()}),
		AgentMessages: store.NewAgentMessageExtStore(g.Database().ORM()),
	})
}

// newRun constructs the run handler (internal/handlers/run): the engine's
// execution API. It delegates to the chat handler rather than reimplementing
// anything, so mounting it adds surface without adding behavior.
func (g *Graph) newRun() (*run.Service, error) {
	return run.New(run.Deps{
		Logger:    g.Log,
		Config:    g.Cfg,
		Repo:      g.Database(),
		RunLists:  store.NewRunListExtStore(g.Database().ORM()),
		Workflows: store.NewWorkflowExtStore(g.Database().ORM()),
		ChatRows:  store.NewChatExtStore(g.Database().ORM()),
		ChatViews: store.NewChatsWithActivityExtStore(g.Database().ORM()),
		Chats:     g.Chat(),
		Orgs:      g.OrgResolver(),
	})
}

// newTrigger constructs the trigger handler (internal/handlers/trigger). It
// converges Temporal Schedules onto the triggers table
// (trigger.TemporalBackend, which also polls the integrations the inbound
// registry polls), and writes webhook and integration triggers against the
// inbound receivers.
func (g *Graph) newTrigger() (*trigger.Service, error) {
	in := g.TriggerInbound()
	syncer, fires := trigger.TemporalBackend(g.TemporalClient().Client, triggers.NewStores(g.Database()), v2workflow.SharedTaskQueue, in.Registry.IsPolled)
	// An org's linked GitHub App installations, from control-plane: nil when
	// self-hosted, which refuses binding a trigger to one.
	apps, err := gitTokens(g.ServerProcessConfig.GetControlPlane())
	if err != nil {
		return nil, err
	}
	return trigger.New(trigger.Deps{
		Projects:       g.Projectaccess(),
		Logger:         g.Log,
		Config:         g.Cfg,
		Repo:           g.Database(),
		Chats:          store.NewChatExtStore(g.Database().ORM()),
		Owners:         g.Triggerowner(),
		Firings:        store.NewTriggerEventExtStore(g.Database().ORM()),
		WebhookSecrets: store.NewTriggerExtStore(g.Database().ORM()), TriggerWrites: store.NewTriggerWritesExtStore(g.Database().ORM()), Triggers: store.NewTriggerExtStore(g.Database().ORM()), FiringReads: store.NewTriggerEventExtStore(g.Database().ORM()), Registrations: store.NewTriggerRegistrationExtStore(g.Database().ORM()),
		OrgMoves:       store.NewProjectOrgMoveExtStore(g.Database().ORM()),
		Pools:          store.NewMachinePoolExtStore(g.Database().ORM()),
		Syncer:         syncer,
		Fires:          fires,
		ControlPlane:   g.CallerControlPlane().Client(),
		AutomationKeys: g.CredentialsAutomationtokens(),
		Integrations:   g.IntegrationSettings().Config,
		Sealer:         in.Vault,
		Catalog:        in.Registry,
		Intake:         in.Intake,
		Access:         triggerAccess(in),
		Senders:        g.TriggerSenders(),
		Options:        optionsChecker{svc: g.InputOptions()},
		Alerting:       alertingProvisioner(g.TriggerAlerting()),
		Drafter:        automationDrafter(g.TemporalClient().Client, v2workflow.SharedTaskQueue),
		Orgs:           g.OrgResolver(),
		// A nil gitcredentialclient.Service converts to a nil interface.
		OrgGitHubApps: apps,
		// Binding is off until every replica and the rollback target honour it.
		OrgRollout: TriggerOrgRollout(g.ApiServerConfig),
		Gate:       nil, // optional — no provider
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
		Logger:       g.Log,
		Config:       g.Cfg,
		Repo:         g.Database(),
		Pools:        store.NewMachinePoolExtStore(g.Database().ORM()),
		Mirror:       g.Daemonmirror(),
		Router:       g.APIDaemonRouter().NATSDaemonRouter,
		IdleTimeouts: g.CallerControlPlane().Client(),
		Names:        g.CallerControlPlane().Client(),
	})
}

// newFileSystem constructs the file_system handler
// (internal/handlers/file_system): a proxy to the connected daemon, which
// holds the files.
func (g *Graph) newFileSystem() (*file_system.Service, error) {
	return file_system.New(file_system.Deps{
		Logger:    g.Log,
		Config:    g.Cfg,
		Router:    g.APIDaemonRouter().NATSDaemonRouter,
		Repo:      store.NewChatsWithActivityExtStore(g.Database().ORM()),
		Placement: daemonplacement.NewRecords(g.Database().ORM()),
	})
}

// newForge constructs the forge handler (internal/handlers/forge): a forge
// project's release/env/secret state, read off the DAEMON's filesystem
// through the router. Always mounted; RELIANT_DISABLE_FORGE_API is its mount
// option (apiMountOptions).
func (g *Graph) newForge() (*forge.Service, error) {
	return forge.New(forge.Deps{
		Projects: g.Projectaccess(),
		Logger:   g.Log,
		Config:   g.Cfg,
		Router:   g.APIDaemonRouter().NATSDaemonRouter,
		Repo:     g.Database(),
	})
}

// newInbox constructs the inbox handler (internal/handlers/inbox).
func (g *Graph) newInbox() (*inbox.Service, error) {
	return inbox.New(inbox.Deps{
		Logger:        g.Log,
		Config:        g.Cfg,
		Repo:          g.Database(),
		Chats:         store.NewChatsWithActivityExtStore(g.Database().ORM()),
		ProjectRows:   store.NewProjectExtStore(g.Database().ORM()),
		Machines:      store.NewDaemonExtStore(g.Database().ORM()),
		OrgKeys:       g.CredentialsOrgaccounts(),
		Items:         store.NewInboxDismissalExtStore(g.Database().ORM()),
		Firings:       store.NewTriggerEventExtStore(g.Database().ORM()),
		Notices:       store.NewTriggerExtStore(g.Database().ORM()),
		Registrations: store.NewTriggerRegistrationExtStore(g.Database().ORM()),
		Recoveries:    store.NewWorktreeRecoveryExtStore(g.Database().ORM()),
		Marks:         store.NewWorktreeExtStore(g.Database().ORM()),
		Storage:       worktreesweep.NewStores(g.Database(), store.NewWorktreeExtStore(g.Database().ORM())),
		Sweeper:       g.WorktreeSweep(),
		Orgs:          g.OrgResolver(),
		// The caller's live orgs, asked of the control plane as the caller;
		// nil when self-hosted. A nil Service converts to a nil OrgLister.
		MyOrgs: g.CallerControlPlane().Client(),
		// Which of the caller's orgs are teams, for the notice of their
		// automations still on their own accounts; nil without orgs.
		Members: inboxMembers(g.OrgResolver()),
	})
}

// newMCP constructs the mcp handler (internal/handlers/mcp).
func (g *Graph) newMCP() (*mcp.Service, error) {
	return mcp.New(mcp.Deps{
		Refetch:        g.chatUpdates(),
		Projects:       g.Projectaccess(),
		ProjectConfigs: store.NewProjectConfigExtStore(g.Database().ORM()),
		Logger:         g.Log,
		Config:         g.Cfg,
		Repo:           g.Database(),
		ProjectRows:    store.NewProjectExtStore(g.Database().ORM()),
		Router:         g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newMessage constructs the message handler (internal/handlers/message).
func (g *Graph) newMessage() (*message.Service, error) {
	return message.New(message.Deps{
		Logger:  g.Log,
		Config:  g.Cfg,
		Repo:    g.Database(),
		Chats:   store.NewChatExtStore(g.Database().ORM()),
		Senders: store.NewThreadExtStore(g.Database().ORM()),
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
		Endpoints:   store.NewModelEndpointExtStore(g.Database().ORM()),
		Machines:    g.Database(),
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
		Projects:  g.Projectaccess(),
		Logger:    g.Log,
		Config:    g.Cfg,
		Router:    g.APIDaemonRouter().NATSDaemonRouter,
		Repo:      g.Database(),
		Favorites: store.NewCommandFavoriteExtStore(g.Database().ORM()),
		Placement: daemonplacement.NewRecords(g.Database().ORM()),
	})
}

// newTask constructs the task handler (internal/handlers/task).
func (g *Graph) newTask() (*task.Service, error) {
	repo := g.Database()
	return task.New(task.Deps{
		Logger:  g.Log,
		Config:  g.Cfg,
		Plans:   store.NewPlanExtStore(repo.ORM()),
		Threads: store.NewThreadExtStore(repo.ORM()),
		Chats:   store.NewChatExtStore(repo.ORM()),
		Refetch: g.chatUpdates(),
		DB:      g.ORM(),
	})
}

// newPlan constructs the plan handler (internal/handlers/plan).
func (g *Graph) newPlan() (*plan.Service, error) {
	repo := g.Database()
	return plan.New(plan.Deps{
		Logger:  g.Log,
		Config:  g.Cfg,
		Plans:   store.NewPlanExtStore(repo.ORM()),
		Tasks:   store.NewTaskExtStore(repo.ORM()),
		Threads: store.NewThreadExtStore(repo.ORM()),
		Chats:   store.NewChatExtStore(repo.ORM()),
		Refetch: g.chatUpdates(),
		DB:      g.ORM(),
	})
}

// newPreset constructs the preset handler (internal/handlers/preset).
func (g *Graph) newPreset() (*preset.Service, error) {
	return preset.New(preset.Deps{
		Projects: g.Projectaccess(),
		Logger:   g.Log,
		Config:   g.Cfg,
		Repo:     g.Database(),
		Drafts:   g.Workflowdrafts(),
	})
}

// newProject constructs the project handler (internal/handlers/project). Only
// a deployment WITH a control plane can clone from GitHub on the user's
// behalf; self-hosted leaves ControlPlane nil.
func (g *Graph) newProject() (*project.Service, error) {
	o := g.Database().ORM()
	return project.New(project.Deps{
		Projects:       g.Projectaccess(),
		Purges:         g.Projectpurge(),
		ProjectConfigs: store.NewProjectConfigExtStore(o),
		ProjectRows:    store.NewProjectExtStore(o),
		Machines:       store.NewDaemonExtStore(o),
		Repos:          store.NewRepoExtStore(o),
		Logger:         g.Log,
		Config:         g.Cfg,
		Repo:           g.Database(),
		Lookups:        store.NewProjectExtStore(o),
		Installs:       store.NewProjectDaemonExtStore(o),
		Detachments:    store.NewProjectDaemonDetachmentExtStore(o),
		Ledger:         store.NewProjectOrgMoveExtStore(o),
		Shared:         store.NewShareEventExtStore(o),
		Router:         g.APIDaemonRouter().NATSDaemonRouter,
		ControlPlane:   g.CallerControlPlane().Client(),
		// The Projects list reads the org's hosted apps as the caller; nil
		// self-hosted, where it lists them as unavailable.
		Apps: g.CallerControlPlane().Client(),
		Orgs: g.OrgResolver(),
		// Moving a project is off until control-plane's half is in prod and
		// in the rollback target (orgs.project_moves_enabled).
		OrgRollout: ProjectOrgRollout(g.ApiServerConfig),
		Moves:      projectOrgMoveStarter{c: g.TemporalClient().Client},
		// The preview's control-plane facts; nil shows them as unknown.
		MoveFacts: g.InternalControlPlane().Client(),
		// A project delete pauses its triggers' schedules, a restore
		// resumes them, and a purge removes them.
		Schedules: g.ScheduleSyncer(),
		Now:       nil, // optional — no provider
	})
}

// newProviderAccount constructs the provider_account handler
// (internal/handlers/provider_account). Its usage refresher is the same
// refreshing token source the worker's poller uses, so a refresh never races
// a live request.
func (g *Graph) newProviderAccount() (*provider_account.Service, error) {
	return provider_account.New(provider_account.Deps{
		Refetch:     g.chatUpdates(),
		Logger:      g.Log,
		Config:      g.Cfg,
		Store:       g.Database(),
		Chats:       chataccess.NewFromORM(g.Database().ORM()),
		Accounts:    g.CredentialsProvideraccounts(),
		Funding:     store.NewChatPersonalFundingExtStore(g.Database().ORM()),
		ValidateKey: keyvalidate.Validate,
		UsageRefresher: &accountlimits.Fetcher{
			Store:  g.CredentialsProvideraccounts(),
			Tokens: drivers.RefreshingTokens{Store: g.CredentialsProvideraccounts()},
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
		Refetch:   g.chatUpdates(),
		Logger:    g.Log,
		Config:    g.Cfg,
		Repo:      g.Database(),
		ChatRows:  store.NewChatExtStore(g.Database().ORM()),
		ChatViews: store.NewChatsWithActivityExtStore(g.Database().ORM()),
		Resolver:  questions.NewFromRepo(g.Database()), Questions: store.NewQuestionExtStore(g.Database().ORM()),
		Mailbox:  agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(g.Database().ORM()), Announcer: g.chatUpdates()}),
		Signals:  g.PauseService(),
		Messages: g.Chat(),
	})
}

// newRepo constructs the repo handler (internal/handlers/repo).
func (g *Graph) newRepo() (*repo.Service, error) {
	return repo.New(repo.Deps{
		Projects: g.Projectaccess(),
		Repos:    store.NewRepoExtStore(g.Database().ORM()),
		Logger:   g.Log,
		Config:   g.Cfg,
		Repo:     g.Database(),
		Router:   g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newScenario constructs the scenario handler (internal/handlers/scenario).
func (g *Graph) newScenario() (*scenario.Service, error) {
	return scenario.New(scenario.Deps{
		Projects:       g.Projectaccess(),
		Logger:         g.Log,
		Config:         g.Cfg,
		Repo:           g.Database(),
		Drafts:         g.Workflowdrafts(),
		ProjectConfigs: store.NewProjectConfigExtStore(g.Database().ORM()),
		ProjectRows:    store.NewProjectExtStore(g.Database().ORM()),
		Router:         g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newSettings constructs the settings handler (internal/handlers/settings).
func (g *Graph) newSettings() (*settings.Service, error) {
	return settings.New(settings.Deps{
		Refetch:                 g.chatUpdates(),
		Logger:                  g.Log,
		Config:                  g.Cfg,
		Repo:                    g.Database(),
		Accounts:                g.CredentialsProvideraccounts(),
		Privacy:                 g.Privacy(),
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
		Seats:          g.Seatmatch(),
	})
}

// newStreaming constructs the streaming handler (internal/handlers/streaming).
func (g *Graph) newStreaming() (*streaminghandler.Service, error) {
	return streaminghandler.New(streaminghandler.Deps{
		Logger:       g.Log,
		Config:       g.Cfg,
		Repo:         g.Database(),
		TriggerReads: store.NewTriggerEventExtStore(g.Database().ORM()),
		ChatRows:     store.NewChatExtStore(g.Database().ORM()),
		Senders:      store.NewThreadExtStore(g.Database().ORM()),
		Projects:     store.NewProjectExtStore(g.Database().ORM()),
		SpawnCalls:   store.NewToolCallExtStore(g.Database().ORM()),
		Heads:        store.NewChatUpdateExtStore(g.Database().ORM()),
		UserLog:      store.NewUserUpdateExtStore(g.Database().ORM()),
		OrgFeed:      store.NewOrgUpdateExtStore(g.Database().ORM()),
		Siblings:     store.NewMessageExtStore(g.Database().ORM()),
		Deltas:       g.StreamingHub().Hub,
		UserUpdates:  g.UserUpdateHub(),
		ChatUpdates:  g.ChatUpdateHub(),
		Orgs:         g.OrgResolver(),
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
		Logger:    g.Log,
		Config:    g.Cfg,
		Repo:      g.Database(),
		Workflows: store.NewWorkflowExtStore(g.Database().ORM()),
		Chats:     store.NewChatExtStore(g.Database().ORM()),
		Temporal:  g.TemporalClient().Client,
		Router:    g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newWorkflowdrafts constructs the workflowdrafts package
// (internal/workflowdrafts): the one writer of saved workflows, which reads
// them scoped to what a caller may see. The Repo is its transaction runner,
// org locker and user-update publisher, so each write runs in the Repo's
// retry ladder, as it did on the facade.
func (g *Graph) newWorkflowdrafts() (workflowdrafts.Service, error) {
	repo := g.Database()
	return workflowdrafts.NewInstrumented(workflowdrafts.Deps{
		Logger:  g.Log,
		Config:  g.Cfg,
		Tx:      repo,
		Locks:   repo,
		Updates: repo,
		Drafts:  store.NewWorkflowDraftExtStore(repo.ORM()),
	})
}

// newWorkflow constructs the workflow handler (internal/handlers/workflow).
// The trigger handler stops the schedules of automations deleted with a
// workflow.
func (g *Graph) newWorkflow() (*workflowhandler.Service, error) {
	return workflowhandler.New(workflowhandler.Deps{
		Projects:    g.Projectaccess(),
		Logger:      g.Log,
		Config:      g.Cfg,
		Repo:        g.Database(),
		Triggers:    store.NewTriggerExtStore(g.Database().ORM()),
		ProjectRows: store.NewProjectExtStore(g.Database().ORM()),
		Pools:       store.NewMachinePoolExtStore(g.Database().ORM()),
		Drafts:      g.Workflowdrafts(),
		Router:      g.APIDaemonRouter().NATSDaemonRouter,
		Automations: g.Trigger(),
		Orgs:        g.OrgResolver(),
	})
}

// newUsage constructs the usage handler (internal/handlers/usage).
func (g *Graph) newUsage() (*usage.Service, error) {
	repo := g.Database()
	return usage.New(usage.Deps{
		Logger:   g.Log,
		Config:   g.Cfg,
		Usage:    store.NewLlmCallUsageExtStore(repo.ORM()),
		Accounts: g.CredentialsProvideraccounts(),
		Now:      nil, // optional — no provider
	})
}

// newWorktreeReclaim constructs the worktree-reclaim handler (internal/handlers/worktree_reclaim).
func (g *Graph) newWorktreeReclaim() (*worktree_reclaim.Service, error) {
	return worktree_reclaim.New(worktree_reclaim.Deps{
		Logger:  g.Log,
		Config:  g.Cfg,
		Repo:    g.Database(),
		Daemons: store.NewDaemonExtStore(g.Database().ORM()),
		Sender:  g.APIDaemonRouter().NATSDaemonRouter,
	})
}

// newChatUpdateHeadsBackfill constructs the ChatUpdateHeadsBackfill provider (NewChatUpdateHeadsBackfill, internal/app/providers_api.go).
func (g *Graph) newChatUpdateHeadsBackfill() (*ChatUpdateHeadsBackfill, error) {
	return NewChatUpdateHeadsBackfill(ChatUpdateHeadsBackfillDeps{
		DB: g.Database(),
	})
}

// newSharing constructs the sharing handler (internal/handlers/sharing):
// read-only sharing of chats, projects and saved workflows with their org's
// live members.
func (g *Graph) newSharing() (*sharing.Service, error) {
	o := g.Database().ORM()
	return sharing.New(sharing.Deps{
		Logger:         g.Log,
		Config:         g.Cfg,
		Chats:          store.NewChatsWithActivityExtStore(o),
		Projects:       store.NewProjectExtStore(o),
		WorkflowDrafts: g.Workflowdrafts(),
		Shares:         store.NewShareEventExtStore(o),
		Members:        store.NewChatMemberExtStore(o),
		Tx:             o,
		Orgs:           g.OrgResolver(),
		// SetChatMember grants only with chat_collaboration.enabled.
		Collaboration: g.ApiServerConfig.GetChatCollaboration(),
	})
}

// newAutomationTakeover constructs the automation-takeover handler
// (internal/handlers/automation_takeover): an org admin's takeover of a
// departed member's automations. It decides who may take a trigger; the
// trigger handler does the adopting, re-validating it as its new owner.
func (g *Graph) newAutomationTakeover() (*automation_takeover.Service, error) {
	return automation_takeover.New(automation_takeover.Deps{
		Logger:       g.Log,
		Config:       g.Cfg,
		TriggerReads: store.NewTriggerExtStore(g.Database().ORM()),
		Offers:       g.Triggerowner(),
		Triggers:     g.Trigger(),
		Now:          nil, // optional — no provider
	})
}

// newOrgProviderAccount constructs the org-provider-account handler
// (internal/handlers/org_provider_account). Adding an org account or binding a
// workspace is gated by ApiServerConfig orgs.provider_accounts_enabled
// (NewOrgProviderAccount); nothing else it does is.
func (g *Graph) newOrgProviderAccount() (*org_provider_account.Service, error) {
	return NewOrgProviderAccount(g.ApiServerConfig, org_provider_account.Deps{
		Logger:      g.Log,
		Config:      g.Cfg,
		Accounts:    g.CredentialsProvideraccounts(),
		OrgAccounts: g.CredentialsOrgaccounts(),
		ValidateKey: keyvalidate.Validate,
		OrgRollout:  g.ApiServerConfig.GetOrgs(), // NewOrgProviderAccount sets the same, from the config it is given
		Seats:       g.Seatmatch(),
		ChatWaker:   orgAccountChatWaker(g.Providerbackoff(), g.TemporalClient()),
	})
}

// newOrgCredentialSweep constructs the OrgCredentialSweep provider (NewOrgCredentialSweep, internal/app/org_credentials_sweep.go).
func (g *Graph) newOrgCredentialSweep() (*OrgCredentialSweep, error) {
	return NewOrgCredentialSweep(OrgCredentialSweepDeps{
		OrgAccounts: g.CredentialsOrgaccounts(),
		Authority:   g.TokenAuthority(),
	})
}

// newPrivacy constructs the privacy package (internal/privacy): each user's
// analytics and crash-reporting choices, kept as user-level settings.
func (g *Graph) newPrivacy() (privacy.Service, error) {
	svc, err := privacy.New(privacy.Deps{
		Logger:   g.Log,
		Config:   g.Cfg,
		Settings: store.NewSettingExtStore(g.Database().ORM()),
	})
	if err != nil {
		return nil, err
	}
	return privacy.NewServiceWithForgeMiddleware(svc), nil
}

// newProviderbackoff constructs the providerbackoff package
// (internal/providerbackoff): the markers a thread parked on a provider leaves.
func (g *Graph) newProviderbackoff() (providerbackoff.Service, error) {
	svc, err := providerbackoff.New(providerbackoff.Deps{
		Logger:   g.Log,
		Config:   g.Cfg,
		Markers:  store.NewProviderBackoffExtStore(g.Database().ORM()),
		Tx:       g.Database(),
		Activity: g.Database(),
	})
	if err != nil {
		return nil, err
	}
	return providerbackoff.NewServiceWithForgeMiddleware(svc), nil
}

// newLlmusage constructs the llmusage package (internal/llmusage): the
// per-call usage ledger and its retention.
func (g *Graph) newLlmusage() (llmusage.Service, error) {
	svc, err := llmusage.New(llmusage.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Ledger: store.NewLlmCallUsageExtStore(g.Database().ORM()),
		Tools:  store.NewToolCallUsageExtStore(g.Database().ORM()),
		Tx:     g.Database(),
	})
	if err != nil {
		return nil, err
	}
	return llmusage.NewServiceWithForgeMiddleware(svc), nil
}

// newDaemonmirror constructs the daemonmirror package (internal/daemonmirror):
// the registry replica of the control plane's daemon set.
func (g *Graph) newDaemonmirror() (daemonmirror.Service, error) {
	o := g.Database().ORM()
	svc, err := daemonmirror.New(daemonmirror.Deps{
		Logger:    g.Log,
		Config:    g.Cfg,
		Daemons:   daemonmirror.NewStores(o),
		Tx:        g.Database(),
		Lifecycle: g.Database(),
		Updates:   g.Database(),
	})
	if err != nil {
		return nil, err
	}
	return daemonmirror.NewServiceWithForgeMiddleware(svc), nil
}

// newSeatmatch constructs the seatmatch package (internal/seatmatch).
func (g *Graph) newSeatmatch() (seatmatch.Service, error) {
	svc, err := seatmatch.New(seatmatch.Deps{
		Logger:  g.Log,
		Config:  g.Cfg,
		Store:   seatmatchStore{g.CredentialsProvideraccounts(), g.CredentialsOrgaccounts()},
		Members: g.OrgResolver(),
		// Attributing seats is off until every replica and the rollback target honour a
		// seat's org (orgs.provider_workspace_seats_enabled).
		Rollout: g.ApiServerConfig.GetOrgs(),
	})
	if err != nil {
		return nil, err
	}
	return seatmatch.NewServiceWithForgeMiddleware(svc), nil
}

// newTriggerowner constructs the triggerowner package (internal/triggerowner):
// the one writer of a trigger's owner after it is created (the takeover).
func (g *Graph) newTriggerowner() (triggerowner.Service, error) {
	o := g.Database().ORM()
	owned, trigs := store.NewTriggerOwnerExtStore(o), store.NewTriggerExtStore(o)
	svc, err := triggerowner.New(triggerowner.Deps{
		Logger:    g.Log,
		Config:    g.Cfg,
		Tx:        o,
		Locks:     owned,
		Triggers:  trigs,
		Adoptions: owned,
		Offers:    owned,
		OrgReads:  owned,
		Now:       nil, // optional — no provider
	})
	if err != nil {
		return nil, err
	}
	return triggerowner.NewServiceWithForgeMiddleware(svc), nil
}

// newOrgUsage constructs the org-usage handler (internal/handlers/org_usage):
// an org's billing:read holders read its org-funded usage, priced at list
// through control-plane (InternalControlPlane; nil shows no estimates).
func (g *Graph) newOrgUsage() (*org_usage.Service, error) {
	repo := g.Database()
	return org_usage.New(org_usage.Deps{
		Logger:   g.Log,
		Config:   g.Cfg,
		Usage:    store.NewLlmOrgUsageDailyExtStore(repo.ORM()),
		Accounts: g.CredentialsProvideraccounts(),
		Prices:   g.InternalControlPlane().Client(),
		Now:      nil, // optional — no provider
	})
}

// newOrgPolicyImpact constructs the org-policy-impact handler (internal/handlers/org_policy_impact).
func (g *Graph) newOrgPolicyImpact() (*org_policy_impact.Service, error) {
	repo := g.Database()
	return org_policy_impact.New(org_policy_impact.Deps{
		Logger:     g.Log,
		Config:     g.Cfg,
		Calls:      store.NewLlmCallUsageExtStore(repo.ORM()),
		Chats:      store.NewChatsWithActivityExtStore(repo.ORM()),
		Now:        nil, // optional — no provider
		Attendance: g.OrgPolicyAttendance(),
	})
}

// newOrgPolicyAttendance constructs the OrgPolicyAttendance provider (NewOrgPolicyAttendance, internal/app/providers_api.go).
func (g *Graph) newOrgPolicyAttendance() (org_policy_impact.ChatAttendance, error) {
	return NewOrgPolicyAttendance(OrgPolicyAttendanceDeps{
		Store: g.Database(),
	})
}

// newWorkflowCheckSyncer constructs the WorkflowCheckSyncer provider (NewWorkflowCheckSyncer, internal/app/providers_worker.go).
func (g *Graph) newWorkflowCheckSyncer() (*workflowcheck.Syncer, error) {
	return NewWorkflowCheckSyncer(WorkflowCheckSyncerDeps{
		Checks:     g.SyncingChecks(),
		Policies:   g.OrgPolicyResolver(),
		Membership: g.OrgResolver(),
	})
}

// newCoverageSource constructs the coverage-source handler
// (internal/handlers/coverage_source): a covered file's source, read from
// GitHub with the viewer's own token once control-plane confirms a retained
// coverage report names it. Without a control plane, or without a way to
// read GitHub as the viewer, every read is NOT_YET_AVAILABLE.
func (g *Graph) newCoverageSource() (*coverage_source.Service, error) {
	blobs, err := CoverageSourceBlobs(g.Database(), g.CredentialsConnectionvault(), g.Vault(), g.ServerProcessConfig.GetControlPlane(), g.IntegrationSettings())
	if err != nil {
		return nil, err
	}
	return coverage_source.New(coverage_source.Deps{
		Logger:   g.Log,
		Config:   g.Cfg,
		Projects: g.Database(),
		Orgs:     g.OrgResolver(),
		Reports:  g.CallerControlPlane().Client(),
		Blobs:    blobs,
		Now:      nil, // optional — no provider
	})
}

// newPullRequestRecord constructs the pull-request-record handler (internal/handlers/pull_request_record).
func (g *Graph) newPullRequestRecord() (*pull_request_record.Service, error) {
	return pull_request_record.New(pull_request_record.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Store:  store.NewPrRecordExtStore(g.Database().ORM()),
		Now:    nil, // optional — no provider
	})
}

// newORM constructs the ORM provider (NewORM, internal/app/providers.go).
func (g *Graph) newORM() (orm.Context, error) {
	return NewORM(ORMDeps{
		Repo: g.Database(),
	})
}

// newIntegrationsWriteledger constructs the integrations/writeledger package (internal/integrations/writeledger).
func (g *Graph) newIntegrationsWriteledger() (writeledger.Service, error) {
	return writeledger.NewServiceWithForgeMiddleware(writeledger.New(writeledger.Deps{
		Store:     g.WriteLedgerStore(),
		Runs:      writeledger.NewRuns(g.Database().ORM()),
		Workflows: store.NewWorkflowExtStore(g.Database().ORM()),
		PRLinks:   g.Chatprlinks(),
		Now:       time.Now, // framework clock
	})), nil
}

// newWriteLedgerStore constructs the WriteLedgerStore provider (NewWriteLedgerStore, internal/app/providers_worker.go).
func (g *Graph) newWriteLedgerStore() (writeledger.Store, error) {
	return NewWriteLedgerStore(WriteLedgerStoreDeps{
		DB: g.Database(),
	})
}

// newProjectaccess constructs the projectaccess package (internal/projectaccess).
func (g *Graph) newProjectaccess() (projectaccess.Service, error) {
	return projectaccess.NewServiceWithForgeMiddleware(projectaccess.New(projectaccess.Deps{
		Store: g.Database(),
	})), nil
}

func (g *Graph) newCredentialsAutomationtokens() (automationtokens.Service, error) {
	svc, err := automationtokens.New(automationtokens.Deps{DB: g.Database().ORM(), Sealer: g.Vault()})
	if err != nil {
		return nil, err
	}
	return automationtokens.NewServiceWithForgeMiddleware(svc), nil
}

func (g *Graph) newCredentialsProvideraccounts() (provideraccounts.Service, error) {
	svc, err := provideraccounts.New(provideraccounts.Deps{DB: g.Database().ORM(), Sealer: g.Vault()})
	if err != nil {
		return nil, err
	}
	return provideraccounts.NewServiceWithForgeMiddleware(svc), nil
}

func (g *Graph) newCredentialsConnectionvault() (connectionvault.Service, error) {
	svc, err := connectionvault.New(connectionvault.Deps{DB: g.Database().ORM()})
	if err != nil {
		return nil, err
	}
	return connectionvault.NewServiceWithForgeMiddleware(svc), nil
}

func (g *Graph) newCredentialsOrgaccounts() (orgaccounts.Service, error) {
	svc, err := orgaccounts.New(orgaccounts.Deps{DB: g.Database().ORM(), Sealer: g.Vault()})
	if err != nil {
		return nil, err
	}
	return orgaccounts.NewServiceWithForgeMiddleware(svc), nil
}

func (g *Graph) newConnectorGrants() (connectorgrant.Store, error) {
	return NewConnectorGrants(ConnectorGrantsDeps{
		DB: g.Database(),
	})
}

// newChangeController constructs the ChangeController provider (NewChangeController, internal/app/providers_worker.go).
func (g *Graph) newChangeController() (tools.ChangeController, error) {
	return NewChangeController(ChangeControllerDeps{
		DB:           g.Database(),
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
	}), nil
}

// newRecordOnlyChecks constructs the RecordOnlyChecks provider (NewRecordOnlyChecks, internal/app/variant_record_only_checks.go).
func (g *Graph) newRecordOnlyChecks() (*RecordOnlyChecks, error) {
	return NewRecordOnlyChecks(RecordOnlyChecksDeps{
		DB: g.Database(),
	})
}

// newSyncingChecks constructs the SyncingChecks provider (NewSyncingChecks, internal/app/variant_syncing_checks.go).
func (g *Graph) newSyncingChecks() (*SyncingChecks, error) {
	return NewSyncingChecks(SyncingChecksDeps{
		DB:          g.Database(),
		Credentials: g.WorkerIntegrationCredentials(),
	})
}

// newProjectpurge constructs the projectpurge package (internal/projectpurge).
func (g *Graph) newProjectpurge() (projectpurge.Service, error) {
	return projectpurge.NewServiceWithForgeMiddleware(projectpurge.New(projectpurge.Deps{
		DB: g.Database().ORM(), // the project's ORM client
	})), nil
}

// newChatTrace constructs the chat-trace handler (internal/handlers/chat_trace):
// a chat's Trace/Cost tab, its spans read through control-plane
// (ChatTraceSpans; nil answers every trace without spans, PARTIAL).
func (g *Graph) newChatTrace() (*chat_trace.Service, error) {
	return chat_trace.New(chat_trace.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		Store:  g.Database(),
		Chats:  store.NewChatExtStore(g.Database().ORM()),
		Spans:  ChatTraceSpans(g.ServerProcessConfig.GetControlPlane()),
		Now:    nil, // optional — no provider
	})
}

// newChatprlinks constructs the chatprlinks package (internal/chatprlinks).
func (g *Graph) newChatprlinks() (chatprlinks.Service, error) {
	svc, err := chatprlinks.New(chatprlinks.Deps{
		Store: store.NewChatPrLinkExtStore(g.Database().ORM()),
		Chats: store.NewChatsWithActivityExtStore(g.Database().ORM()),
		Now:   time.Now, // framework clock
	})
	if err != nil {
		return nil, err
	}
	return chatprlinks.NewServiceWithForgeMiddleware(svc), nil
}

func (g *Graph) newCallerControlPlane() (*CallerControlPlane, error) {
	return NewCallerControlPlane(CallerControlPlaneDeps{
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
	})
}

func (g *Graph) newInternalControlPlane() (*InternalControlPlane, error) {
	return NewInternalControlPlane(InternalControlPlaneDeps{
		ControlPlane: g.ServerProcessConfig.GetControlPlane(),
	})
}

// newPipeline constructs the pipeline handler (internal/handlers/pipeline).
// pipeline.enabled reaches the API server here (G4): off, every rpc refuses.
func (g *Graph) newPipeline() (*pipeline.Service, error) {
	return pipeline.New(pipeline.Deps{
		Logger: g.Log,
		Config: g.Cfg,
		// Only the switch, never the whole pipeline block: the block also
		// holds the worker's merge-queue caller secret
		// (pipeline.github_queue_app_caller_secret), and the API root's
		// config reach must not include it.
		Pipeline: &configv1.PipelineConfig{Enabled: g.ServerProcessConfig.GetPipeline().GetEnabled()},
		Store:    g.Database(),
		Now:      nil, // optional — no provider
	})
}

// newSvcGtm constructs the gtm handler (internal/handlers/gtm).
func (g *Graph) newSvcGtm() (*svcGtm.Service, error) {
	return svcGtm.New(svcGtm.Deps{
		Logger:  g.Log,
		Config:  g.Cfg,
		Service: nil, // optional — no provider
	})
}
