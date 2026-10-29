// Copyright (c) 2025 Reliant Labs

package app

import (
	"context"
	"fmt"
	"log/slog"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/agentruns"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/automationcred"
	reliantconfig "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/configadapter"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/handlers/chat"
	"github.com/reliant-labs/reliant/internal/handlers/run"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	"github.com/reliant-labs/reliant/internal/hosteduse"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/credwire"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/workflowevent"
	"github.com/reliant-labs/reliant/internal/triggertools"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/reliant-labs/reliant/internal/videojobs"
	"github.com/reliant-labs/reliant/internal/workersetup"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/capacitywake"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	scenariorunner "github.com/reliant-labs/reliant/internal/workflow/scenario/runner"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
	"github.com/reliant-labs/reliant/pkg/config"
	"github.com/reliant-labs/reliant/pkg/hostedhttp"
)

// The Temporal worker's providers (the worker root, roots_worker.go).
//
// A node wired differently here than in the api-server has a type of its own
// (WorkerDaemonRouter, WorkerChat, WorkerRun, WorkerTrigger,
// WorkerToolsFactory, WorkerCatalogSearch, WorkerIntegrationCredentials): the
// graph's identity is the type, so the api root can never be handed the
// worker's variant — the daemon router that may fall back to a trigger's
// delegated token above all — nor the worker the api's.

// WorkerDaemonRouter is the worker's daemon router: tool execution routed to
// the run's daemon over NATS. Unlike the api-server's it may act without a
// user — an unattended trigger fire has no user JWT — so it alone falls back
// to the trigger's delegated token, and resumes a held run's machine as the
// worker.
type WorkerDaemonRouter struct{ *toolexec.NATSDaemonRouter }

// WorkerDaemonRouterDeps is the bus the router routes over, the database the
// daemon records come from, and the control plane that wakes machines.
type WorkerDaemonRouterDeps struct {
	NATS         *NATSConn
	DB           *db.Repo
	ControlPlane *configv1.ControlPlaneConfig
}

// NewWorkerDaemonRouter builds the worker's router. Hosted, the control plane
// resumes a suspended managed machine; the daemon records still come from
// the repository.
//
//forge:root-only worker
func NewWorkerDaemonRouter(d WorkerDaemonRouterDeps) (*WorkerDaemonRouter, error) {
	repo := d.DB
	opts := []toolexec.NATSRouterOption{toolexec.WithDatabase(repo)}
	if cp := ControlPlaneEndpoint(d.ControlPlane); cp.Hosted() {
		cpDeps := controlplane.Deps{ControlPlane: d.ControlPlane}
		if secret := cp.InternalServiceSecret; secret != "" {
			cpDeps.Sign = func() (string, error) { return auth.SignInternalServiceToken(secret) }
		}
		cpAdapter := controlplane.NewInstrumented(cpDeps)
		opts = append(opts,
			toolexec.WithDaemonResumer(cpAdapter),
			// The worker alone may fall back to a trigger's delegated
			// token: an unattended fire has no user JWT.
			toolexec.WithControlPlaneCredentials(automationcred.NewInstrumented(automationcred.Deps{Keys: repo})))
		// A run that resumes after a hold wakes its machine as the worker
		// (the owner's session token may have expired meanwhile); see
		// toolexec.RunWaker. That needs the worker's internal-service
		// identity; without one, WakeForRun wakes with the owner's token.
		if cpDeps.Sign != nil {
			opts = append(opts, toolexec.WithRunResumer(cpAdapter))
		}
	}
	router := toolexec.NewNATSDaemonRouter(d.NATS.Conn, opts...)
	logging.Info("Tool execution routing via NATS")
	return &WorkerDaemonRouter{router}, nil
}

// WorkerIntegrationCredentials is the credential source the worker's
// integration actions and polls authenticate with: saved connections, plus
// the brokers this deployment delegates to (control-plane's GitHub tokens,
// when hosted).
type WorkerIntegrationCredentials struct{ Source httpaction.CredentialSource }

// WorkerIntegrationCredentialsDeps is where saved connections and their
// secrets live, and the control plane delegated credentials come from.
type WorkerIntegrationCredentialsDeps struct {
	DB           *db.Repo
	Vault        vault.Service
	Integrations *IntegrationSettings
	ControlPlane *configv1.ControlPlaneConfig
}

// NewWorkerIntegrationCredentials builds the source. It fails boot when a
// shipped manifest names an executor this binary did not register.
//
//forge:root-only worker
func NewWorkerIntegrationCredentials(d WorkerIntegrationCredentialsDeps) (*WorkerIntegrationCredentials, error) {
	resolver, err := newWorkerConnectionResolver(d.DB, d.Vault, d.Integrations.Webhooks.OAuthClients)
	if err != nil {
		return nil, err
	}
	if err := checkExecutors(); err != nil {
		return nil, err
	}
	src, err := newWorkerIntegrationCredentials(resolver, d.ControlPlane)
	if err != nil {
		return nil, err
	}
	return &WorkerIntegrationCredentials{Source: src}, nil
}

// checkExecutors fails boot when a shipped manifest names a go: executor this
// binary did not register, instead of failing that action at its first call.
// Executors register from their package's init; the integration packages are
// linked in through webhook (gmail), so a missing one is a wiring defect.
func checkExecutors() error {
	if err := httpaction.Executors().CheckManifests(catalog.MustBuiltin().Manifests()); err != nil {
		return fmt.Errorf("integrations: %w", err)
	}
	return nil
}

// newWorkerConnectionResolver builds the worker-side resolver. It shares the
// vault key with the api-server: the worker is the only process that reads
// secrets, and the only one besides the api-server that writes them (on
// refresh).
func newWorkerConnectionResolver(repo *db.Repo, keys vault.Service, clients connections.OAuthClients) (*connections.Resolver, error) {
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), clients)
	if err != nil {
		return nil, fmt.Errorf("connections: %w", err)
	}
	store := repo.Connections()
	tokens := connections.NewTokenSource(store, keys, providers, nil)
	return connections.NewResolver(repo, store, tokens), nil
}

// newWorkerIntegrationCredentials is the credential source integration
// actions authenticate with: saved connections plus the delegated brokers
// this deployment registers (see credwire).
func newWorkerIntegrationCredentials(resolver *connections.Resolver, cp *configv1.ControlPlaneConfig) (httpaction.CredentialSource, error) {
	return credwire.New(resolver, cp)
}

// triggerCredentials is what the poll activity resolves a polled trigger's
// credential with: the same source integration actions use, asked by trigger
// id rather than by run. nil (the poll is skipped) when the source is not one
// that can.
func triggerCredentials(creds httpaction.CredentialSource) triggers.PollCredentials {
	if cs, ok := creds.(connauth.Service); ok {
		return cs
	}
	return nil
}

// WorkerCatalogSearch is the caller-aware integration catalog search the
// search_integrations / get_integration_schema tools read on the worker.
type WorkerCatalogSearch struct{ *catalogindex.Service }

// WorkerCatalogSearchDeps is the connections it reports on and the
// credential source that decides what "connected" means here.
type WorkerCatalogSearchDeps struct {
	DB          *db.Repo
	Credentials *WorkerIntegrationCredentials
}

// NewWorkerCatalogSearch builds the search.
//
//forge:root-only worker
func NewWorkerCatalogSearch(d WorkerCatalogSearchDeps) (*WorkerCatalogSearch, error) {
	svc, err := newWorkerCatalogSearch(d.DB.Connections(), d.Credentials.Source)
	if err != nil {
		return nil, err
	}
	return &WorkerCatalogSearch{svc}, nil
}

// newWorkerCatalogSearch is the catalog search over conns. "Connected" is
// judged against the brokers this worker ACTUALLY registered on its
// credential source, so the tool never calls an integration usable that the
// action node on this same worker could not authenticate.
func newWorkerCatalogSearch(conns catalogindex.ConnectionLister, creds httpaction.CredentialSource) (*catalogindex.Service, error) {
	idx, err := catalogindex.Builtin()
	if err != nil {
		return nil, fmt.Errorf("integration catalog index: %w", err)
	}
	cs, _ := creds.(connauth.Service)
	delegated := func(brokerID string) (bool, string) {
		if cs != nil {
			if _, ok := cs.Broker(brokerID); ok {
				return true, ""
			}
		}
		return false, "delegated broker " + brokerID + " is not registered on this worker"
	}
	return catalogindex.NewService(idx, conns, delegated), nil
}

// WriteLedgerDeps is where integration writes are recorded.
type WriteLedgerDeps struct {
	DB *db.Repo
}

// NewWriteLedger records every mutating integration call, from the action
// node or an agent tool, before it is made, so the event it causes can be
// attributed.
func NewWriteLedger(d WriteLedgerDeps) (writeledger.Service, error) {
	return writeledger.NewInstrumented(writeledger.Deps{Store: d.DB.IntegrationWrites(), Runs: d.DB}), nil
}

// StoredConfigProviderDeps is where project configs are stored.
type StoredConfigProviderDeps struct {
	DB *db.Repo
}

// NewStoredConfigProvider is the project-config provider CallLLM resolves on
// every turn. Cached: the record can be tens of MB of skills. Each call still
// checks the record's version, so a daemon push is seen immediately on every
// worker replica.
func NewStoredConfigProvider(d StoredConfigProviderDeps) (*reliantconfig.StoredConfigProvider, error) {
	repo := d.DB
	return reliantconfig.NewCachedStoredConfigProvider(configadapter.NewRepoConfigStore(repo), repo, reliantconfig.DefaultParsedConfigCacheBytes), nil
}

// RunLifecycleDeps is what runs are recorded in and driven through.
type RunLifecycleDeps struct {
	DB       *db.Repo
	Temporal *TemporalClient
	Pause    *v2workflow.PauseService
}

// NewRunLifecycle is the run service: it records runs and drives their
// workflows.
func NewRunLifecycle(d RunLifecycleDeps) (*runs.Service, error) {
	return runs.NewService(d.DB, d.Temporal.Client, d.Pause), nil
}

// RunLauncherDeps is what a launch creates and starts.
type RunLauncherDeps struct {
	DB       *db.Repo
	Temporal *TemporalClient
	Runs     *runs.Service
	// Router places a run's chat on a machine before it starts, so a trigger
	// or scheduled run is pinned like an interactive one.
	Router *WorkerDaemonRouter
}

// NewRunLauncher launches runs on the shared task queue: the launcher the
// run tools and scheduled trigger fires launch through.
func NewRunLauncher(d RunLauncherDeps) (*launch.Launcher, error) {
	pinner := daemonplacement.NewPinner(d.DB, d.Router.NATSDaemonRouter)
	return launch.NewLauncher(d.DB, threads.NewService(d.DB), d.Temporal.Client, d.Runs, v2workflow.SharedTaskQueue,
		launch.WithChatPinner(pinner)), nil
}

// WorkerChat is the chat service as the run tools reach it on the worker.
type WorkerChat struct{ *chat.Service }

// WorkerChatDeps is the chat service's wiring on the worker.
type WorkerChatDeps struct {
	Logger   *slog.Logger
	Config   *config.Config
	DB       *db.Repo
	Temporal *TemporalClient
	Pause    *v2workflow.PauseService
	Router   *WorkerDaemonRouter
}

// NewWorkerChat builds the chat service the run tools act through, as the
// calling chat's owner.
//
//forge:root-only worker
func NewWorkerChat(d WorkerChatDeps) (*WorkerChat, error) {
	svc, err := chat.New(chat.Deps{
		Logger:      d.Logger,
		Config:      d.Config,
		Repo:        d.DB,
		Temporal:    d.Temporal.Client,
		Pause:       d.Pause,
		Router:      d.Router,
		LocalModels: local.NewRepoDirectory(d.DB),
	})
	if err != nil {
		return nil, fmt.Errorf("chat service: %w", err)
	}
	return &WorkerChat{svc}, nil
}

// WorkerRun is the run service as the run tools reach it on the worker.
type WorkerRun struct{ *run.Service }

// WorkerRunDeps is the run service's wiring on the worker.
type WorkerRunDeps struct {
	Logger *slog.Logger
	Config *config.Config
	DB     *db.Repo
	Chats  *WorkerChat
}

// NewWorkerRun builds the run service control_run and send_to_run act
// through.
//
//forge:root-only worker
func NewWorkerRun(d WorkerRunDeps) (*WorkerRun, error) {
	svc, err := run.New(run.Deps{
		Logger: d.Logger,
		Config: d.Config,
		Repo:   d.DB,
		Chats:  d.Chats.Service,
	})
	if err != nil {
		return nil, fmt.Errorf("run service: %w", err)
	}
	return &WorkerRun{svc}, nil
}

// AgentRunsDeps is the launcher and the run service the run tools use.
type AgentRunsDeps struct {
	Launcher *launch.Launcher
	Runs     *WorkerRun
}

// NewAgentRuns is what start_run, control_run and send_to_run execute
// through, as the calling chat's owner: the same launcher and run service
// the api-server uses.
//
//forge:root-only worker
func NewAgentRuns(d AgentRunsDeps) (*agentruns.Runs, error) {
	return agentruns.New(d.Launcher, d.Runs.Service), nil
}

// WorkerTrigger is the trigger service as list_triggers reaches it on the
// worker.
type WorkerTrigger struct{ *trigger.Service }

// WorkerTriggerDeps is the trigger service's wiring on the worker: its
// inbound half needs the provider registry and the vault (signed webhooks),
// and the control plane, which hosts the GitHub App and mints the tokens an
// unattended fire wakes its machine with.
type WorkerTriggerDeps struct {
	Logger       *slog.Logger
	Config       *config.Config
	DB           *db.Repo
	Temporal     *TemporalClient
	Vault        vault.Service
	Pollers      *webhook.Registry
	Integrations *IntegrationSettings
	ControlPlane *configv1.ControlPlaneConfig
}

// NewWorkerTrigger builds the trigger service list_triggers reads through and
// the activation tools (create_trigger_activation, …) write through. Those
// writes must validate as the api-server's TriggerService does, so the
// control plane and GitHub's access gate are wired here as there: without
// them every GitHub activation was refused ("connect github first" — hosted
// GitHub has no saved connection) or could not act as the App. The access
// refresh loop and the input-option reachability check stay api-only.
// PUBLIC_URL is used when the worker has it; without it a webhook's URL is
// reported as a bare path.
//
//forge:root-only worker
func NewWorkerTrigger(d WorkerTriggerDeps) (*WorkerTrigger, error) {
	repo, temporalClient := d.DB, d.Temporal.Client
	syncer, fires := trigger.TemporalBackend(temporalClient, repo, v2workflow.SharedTaskQueue, d.Pollers.IsPolled)
	githubAccess, err := wireGitHubAccess(repo, d.Vault, d.ControlPlane, d.Integrations.Webhooks)
	if err != nil {
		return nil, fmt.Errorf("trigger service: %w", err)
	}
	var access map[string]trigger.IntegrationAccess
	if githubAccess != nil {
		access = map[string]trigger.IntegrationAccess{ghaccess.IntegrationID: gitHubAccess{githubAccess}}
	}
	svc, err := trigger.New(trigger.Deps{
		Logger:       d.Logger,
		Config:       d.Config,
		Repo:         repo,
		Syncer:       syncer,
		Fires:        fires,
		ControlPlane: hostedControlPlane(d.ControlPlane),
		Access:       access,
		Integrations: d.Integrations.Config,
		Sealer:       d.Vault,
		Catalog:      d.Pollers,
		Intake: triggers.NewIntake(repo, temporalClient, v2workflow.SharedTaskQueue).
			WithWorkflows(triggers.LaunchWorkflows{Repo: repo}).
			WithEnrichers(triggers.NewOriginEnricher(repo.IntegrationWrites(), repo, d.Integrations.GitHubAppSlug)),
	})
	if err != nil {
		return nil, fmt.Errorf("trigger service: %w", err)
	}
	return &WorkerTrigger{svc}, nil
}

// WorkerToolsFactory is the tools factory the worker EXECUTES tools with.
// (The api-server's reads only the catalog.)
type WorkerToolsFactory struct{ *tools.ToolsFactory }

// WorkerToolsFactoryDeps is everything a tool executing on the worker
// reaches.
type WorkerToolsFactoryDeps struct {
	DB            *db.Repo
	Network       *configv1.NetworkConfig
	Temporal      *TemporalClient
	AgentRuns     *agentruns.Runs
	Triggers      *WorkerTrigger
	Credentials   *WorkerIntegrationCredentials
	WriteLedger   writeledger.Service
	CatalogSearch *WorkerCatalogSearch
	Router        *WorkerDaemonRouter
}

// NewWorkerToolsFactory builds the factory.
//
//forge:root-only worker
func NewWorkerToolsFactory(d WorkerToolsFactoryDeps) (*WorkerToolsFactory, error) {
	repo, temporalClient := d.DB, d.Temporal.Client
	runMachine := toolexec.NewRunMachine(d.Router)
	return &WorkerToolsFactory{tools.NewToolsFactory(&tools.ToolsOptions{
		Repo:               repo,
		AllowPrivateEgress: AllowPrivateEgress(d.Network),
		// The worker is where spawn_send actually executes (inside the
		// ExecuteTools activity), so this is the wiring that matters most for
		// agent-to-agent delivery.
		AgentMessageNotifier: temporal.NewAgentMessageNotifier(temporalClient, workersetup.ChatWorkflowLookup(repo)),
		// spawn_stop executes here too, and the stop is NOT best-effort: with
		// no stopper the tool reports that it cannot stop anything rather than
		// claiming a cancellation that never left the process.
		SpawnStopper: temporal.NewSpawnStopper(temporalClient, workersetup.ChatWorkflowLookup(repo), repo),
		// generate_image executes here, inside the ExecuteTools activity, so
		// this is the wiring that actually decides whether the tool works.
		ImageGeneratorResolver: resolveImageGenerator,
		// generate_video executes in the worker; the api-server only reads its
		// catalog metadata. The job store is what makes a render resumable.
		VideoGeneratorResolver: resolveVideoGenerator,
		VideoJobs:              videojobs.NewSQLStore(repo.ORM()),
		// run_scenario / write_scenario execute on the real runtime via the
		// scenario runner; injected because the runner imports this package's
		// dependents.
		ScenarioRunner: scenariorunner.RunScenario,
		// start_run / control_run / send_to_run execute here, inside the
		// ExecuteTools activity.
		RunStarter:   d.AgentRuns,
		RunLifecycle: d.AgentRuns,
		RunMessenger: d.AgentRuns,
		// Trigger tools execute here through the owner-authorized service.
		TriggerLister:      triggertools.New(d.Triggers.Service),
		TriggerFirer:       triggertools.New(d.Triggers.Service),
		TriggerEventLister: triggertools.New(d.Triggers.Service),
		ActivationManager:  d.Triggers.Service,
		// http__request executes here, inside the ExecuteTools activity, and
		// resolves its `connection` for the run's owner through the same
		// source the action node uses: saved connections, plus GitHub tokens
		// delegated by control-plane when one is configured.
		IntegrationCredentials: d.Credentials.Source,
		WriteLedger:            d.WriteLedger,
		// search_integrations / get_integration_schema execute here too, and
		// read the run owner's connections to report what is connected.
		CatalogSearch: d.CatalogSearch.Service,
		// The worktree tool executes here, on a worker with no access to the
		// user's files: its git work goes to the run's machine instead.
		RunMachine:      runMachine,
		MachineObserver: runMachine,
		// delete archives here and asks for one immediate settle, as the UI's
		// archive does; the api-server's periodic sweep catches the rest.
		WorktreeSettler: worktreesweep.New(repo, d.Router.NATSDaemonRouter).
			WithKeepFiles(worktreesweep.KeepSetting{R: repo}),
	})}, nil
}

// resolveImageGenerator adapts the driver layer's image-model selection to the
// narrow interface the generate_image tool declares. The selector arrives from
// the tool's bound `model` parameter — tags:[image-gen] unless a human bound
// something else — and ResolveImageGenerator pins the image output modality on
// top of it, so no selector can degrade into a text model.
func resolveImageGenerator(ctx context.Context, userID string, selector models.ModelSelector) (tools.ImageGenerator, error) {
	return drivers.ResolveImageGenerator(ctx, userID, selector)
}

// resolveVideoGenerator adapts the driver layer's video-model selection to the
// narrow interface the generate_video tool declares.
func resolveVideoGenerator(ctx context.Context, userID string, selector models.ModelSelector) (tools.VideoGenerator, error) {
	return drivers.ResolveVideoGenerator(ctx, userID, selector)
}

// RemoteExecutorDeps is where tools execute: the run's daemon, or this
// process.
type RemoteExecutorDeps struct {
	Router *WorkerDaemonRouter
	Tools  *WorkerToolsFactory
	// Egress bounds the egress tools the executor runs in this process.
	// compose.go builds it from WorkerConfig's RELIANT_WORKER_EGRESS_*.
	Egress egresslimit.Limits
}

// NewRemoteExecutor is the worker's tool executor: daemon-placed tools go to
// the run's daemon through the router; PlacementServer / PlacementAny tools
// execute in this process without a daemon round-trip; and a server-side
// tool that still needs the daemon's filesystem or exec gets a per-request
// daemon client bound to the run's machine.
//
//forge:root-only worker
func NewRemoteExecutor(d RemoteExecutorDeps) (*toolexec.RemoteExecutor, error) {
	executor := toolexec.NewRemoteExecutor(d.Router)
	server := toolexec.NewLocalToolExecutor(d.Tools.ToolsFactory)
	server.SetMCPContextBinder(toolexec.NewDaemonMCPContextBinder(d.Router))
	executor.SetServerExecutor(server)
	executor.SetDaemonClientFactory(toolexec.RemoteDaemonClients(d.Router))
	// Fetch, websearch, sourcegraph and the integration actions leave through
	// one shared NAT address; bound what one user, org, host and turn may
	// push through it. In-process state, budgets divided by Replicas: see
	// internal/egresslimit.
	limiter, err := egresslimit.NewInstrumented(egresslimit.Deps{Logger: slog.Default(), Limits: d.Egress})
	if err != nil {
		return nil, fmt.Errorf("egress limiter: %w", err)
	}
	executor.SetEgressLimiter(limiter)
	return executor, nil
}

// AccountRecorder is the worker's account recorder: it folds every
// response's rate-limit headers into the account that served it, and benches
// an account the provider refused. One per worker.
type AccountRecorder struct{ Recorder *accountlimits.Recorder }

// AccountRecorderDeps is where account state is kept.
type AccountRecorderDeps struct {
	DB *db.Repo
}

// NewAccountRecorder builds the recorder.
//
//forge:root-only worker
func NewAccountRecorder(d AccountRecorderDeps) (*AccountRecorder, error) {
	return &AccountRecorder{Recorder: accountlimits.NewRecorder(d.DB, nil)}, nil
}

// Close flushes the rate-limit state the activities recorded, after every
// node that records into it has stopped.
func (a *AccountRecorder) Close(context.Context) error {
	a.Recorder.Close()
	return nil
}

// TemporalWorkerDeps is everything the worker's workflows and activities
// reach.
type TemporalWorkerDeps struct {
	DB             *db.Repo
	Temporal       *TemporalClient
	Streaming      *StreamingHub
	Tools          *WorkerToolsFactory
	Executor       *toolexec.RemoteExecutor
	Router         *WorkerDaemonRouter
	ConfigProvider *reliantconfig.StoredConfigProvider
	Launcher       *launch.Launcher
	Credentials    *WorkerIntegrationCredentials
	WriteLedger    writeledger.Service
	Pollers        *webhook.Registry
	ControlPlane   *configv1.ControlPlaneConfig
	Network        *configv1.NetworkConfig
	Recorder       *AccountRecorder
	// WorkflowTaskSlots and WorkflowTaskPollers are WorkerConfig's
	// RELIANT_WORKER_MAX_CONCURRENT_WORKFLOW_TASKS and
	// RELIANT_WORKER_WORKFLOW_TASK_POLLERS (0: workersetup's default).
	WorkflowTaskSlots   int32
	WorkflowTaskPollers int32
	// Worker carries the hosted-action controller's endpoint and trust
	// bundle (hosted_actions_url, hosted_actions_ca_file); see HostedActions.
	Worker *configv1.WorkerConfig
}

// HostedActions is the worker's hosted HTTP action executor: a separate
// fixed-node execution path, never installed in WorkerIntegrationCredentials
// or the tool factory. nil when there is no control plane (each hosted action
// fails closed with "hosted HTTP execution is not configured").
//
// The dedicated hosted-action URL requires verified TLS outside loopback.
// It never falls back to the unrelated general control-plane API URL. A bad
// optional executor degrades explicitly without stopping ordinary chats.
func HostedActions(log *slog.Logger, store hosteduse.Store, cfg *configv1.ControlPlaneConfig, worker *configv1.WorkerConfig) handlers.HostedActionExecutor {
	if worker.GetHostedActionsUrl() == "" {
		return nil
	}
	cp := ControlPlaneEndpoint(cfg)
	if cp.InternalServiceSecret == "" {
		log.Warn("hosted HTTP actions are DISABLED: internal service signer is missing")
		return disabledHostedActions{reason: "internal service signer is missing"}
	}
	secret := cp.InternalServiceSecret
	client, err := hosteduse.NewInstrumented(hosteduse.Deps{
		Store: store, Worker: worker,
		Sign: func() (string, error) { return auth.SignInternalServiceToken(secret) },
	})
	if err != nil {
		reason := "control-plane URL is refused: " + err.Error()
		log.Warn("hosted HTTP actions are DISABLED on this worker; every hosted action will fail until the control-plane URL is accepted", "reason", reason, "error", err)
		return disabledHostedActions{reason: reason}
	}
	return client
}

// disabledHostedActions refuses every hosted action with its configuration
// reason, which executeHosted publishes as the action's error.
type disabledHostedActions struct{ reason string }

func (d disabledHostedActions) ExecuteAction(context.Context, string, string, string, hostedhttp.Request) (*hostedhttp.Result, error) {
	return nil, &handlers.HostedDisabledError{Reason: d.reason}
}

// NewTemporalWorker is the Temporal worker on the shared task queue
// (workersetup.Worker: its Drain releases in-flight activities, its Ready is
// "polling"). A scheduled trigger fire launches through the same launcher
// the run tools use.
//
//forge:root-only worker
func NewTemporalWorker(d TemporalWorkerDeps) (*workersetup.Worker, error) {
	creds := d.Credentials.Source
	hostedActions := HostedActions(slog.Default(), d.DB, d.ControlPlane, d.Worker)
	worker, _, err := workersetup.NewWorker(&workersetup.Config{
		AccountLimits:          d.Recorder.Recorder,
		TemporalClient:         d.Temporal.Client,
		Database:               d.DB,
		StreamingHub:           d.Streaming.Hub,
		ToolsFactory:           d.Tools.ToolsFactory,
		ToolExecutor:           d.Executor,
		DaemonRouter:           d.Executor.DaemonRouter(),
		MCPBinder:              toolexec.NewDaemonMCPContextBinder(d.Router),
		ConfigProvider:         d.ConfigProvider,
		TriggerLauncher:        d.Launcher,
		IntegrationCredentials: creds,
		HostedActions:          hostedActions,
		WriteLedger:            d.WriteLedger,
		TriggerPollers:         d.Pollers,
		TriggerCredentials:     triggerCredentials(creds),
		AllowPrivateEgress:     AllowPrivateEgress(d.Network),
		WorkflowTaskSlots:      int(d.WorkflowTaskSlots),
		WorkflowTaskPollers:    int(d.WorkflowTaskPollers),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create worker: %w", err)
	}
	return worker, nil
}

// WorkflowEventRelayDeps is the outbox and the workflows it dispatches to.
type WorkflowEventRelayDeps struct {
	DB       *db.Repo
	Temporal *TemporalClient
}

// NewWorkflowEventRelay moves run-event outbox rows into the workflow-event
// triggers' dispatch workflows. Every worker runs a relay; rows are leased
// with SKIP LOCKED, so they share the queue rather than duplicating it.
//
//forge:root-only worker
func NewWorkflowEventRelay(d WorkflowEventRelayDeps) (*workflowevent.Relay, error) {
	return workflowevent.NewRelay(d.DB, d.Temporal.Client, v2workflow.SharedTaskQueue), nil
}

// AccountUsagePollerDeps is where the polled accounts are kept, and the
// Temporal client that wakes a user's blocked chats when a poll finds an
// account serves again.
type AccountUsagePollerDeps struct {
	DB       *db.Repo
	Temporal *TemporalClient
}

// NewAccountUsagePoller polls the providers' usage APIs for accounts the
// rate-limit headers do not cover. Every worker runs a poller; accounts are
// leased with SKIP LOCKED, so they share the queue. An expired access token
// is refreshed through the same per-account coordinator the LLM drivers use,
// so a poll never consumes a refresh token a live request is about to
// consume.
//
//forge:root-only worker
func NewAccountUsagePoller(d AccountUsagePollerDeps) (*accountlimits.Poller, error) {
	repo := d.DB
	return &accountlimits.Poller{
		Store: repo,
		Fetcher: &accountlimits.Fetcher{
			Store:  repo,
			Tokens: drivers.RefreshingTokens{Store: repo},
			// A poll that lifts a bench wakes the user's blocked chats, so their
			// threads re-route onto the account now rather than at a reset.
			OnCapacity: func(ctx context.Context, userID, _ string) {
				capacitywake.WakeUser(ctx, repo, d.Temporal.Client, userID, "a usage poll found a limited account serving again")
			},
		},
	}, nil
}

// WriteLedgerPrune prunes the integration-write ledger until its context
// ends.
type WriteLedgerPrune struct {
	ledger writeledger.Service
	writes writeledger.Pruner
}

// WriteLedgerPruneDeps is the ledger and the rows it prunes.
type WriteLedgerPruneDeps struct {
	Ledger writeledger.Service
	DB     *db.Repo
}

// NewWriteLedgerPrune builds the prune loop.
//
//forge:root-only worker
func NewWriteLedgerPrune(d WriteLedgerPruneDeps) (*WriteLedgerPrune, error) {
	return &WriteLedgerPrune{ledger: d.Ledger, writes: d.DB.IntegrationWrites()}, nil
}

// Run prunes until ctx ends.
func (p *WriteLedgerPrune) Run(ctx context.Context) error {
	p.ledger.PruneLoop(ctx, p.writes)
	return nil
}
