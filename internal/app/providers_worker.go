// Copyright (c) 2025 Reliant Labs

package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/forge/pkg/observe"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/agentruns"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/changetools"
	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/chatupdates"
	reliantconfig "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/configadapter"
	"github.com/reliant-labs/reliant/internal/connectionproposer"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/credentials/automationtokens"
	"github.com/reliant-labs/reliant/internal/credentials/connectionvault"
	"github.com/reliant-labs/reliant/internal/credentials/orgaccounts"
	"github.com/reliant-labs/reliant/internal/credentials/provideraccounts"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/handlers/chat"
	"github.com/reliant-labs/reliant/internal/handlers/run"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	"github.com/reliant-labs/reliant/internal/hosteduse"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/credwire"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/integrations/ghcontents"
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
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/orgmove"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/pipeline/practions"
	"github.com/reliant-labs/reliant/internal/planning"
	"github.com/reliant-labs/reliant/internal/projectaccess"
	"github.com/reliant-labs/reliant/internal/projectpurge"
	"github.com/reliant-labs/reliant/internal/providerbackoff"
	"github.com/reliant-labs/reliant/internal/prrecords"
	"github.com/reliant-labs/reliant/internal/questions"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/stopnudge"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggerowner"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/recorddue"
	"github.com/reliant-labs/reliant/internal/triggers/workflowevent"
	"github.com/reliant-labs/reliant/internal/triggertools"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/reliant-labs/reliant/internal/videojobs"
	"github.com/reliant-labs/reliant/internal/workersetup"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/capacitywake"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	scenariorunner "github.com/reliant-labs/reliant/internal/workflow/scenario/runner"
	"github.com/reliant-labs/reliant/internal/workflowcheck"
	"github.com/reliant-labs/reliant/internal/workflowdrafts"
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
type WorkerDaemonRouter struct {
	*toolexec.NATSDaemonRouter
	// ControlPlane is the adapter the router resumes machines through, nil
	// when self-hosted. manage_machine stops and resizes through it too.
	ControlPlane controlplane.Service
	// ControlPlaneURL is the control plane's base URL, "" when self-hosted.
	// The Forge Changes tools call it as the user (change_controller.go).
	ControlPlaneURL string
}

// A run step's retry reattaches through the worker's router
// (toolexec.RemoteExecutor.ReattachTool); losing the method would quietly
// turn every reattach back into a refusal.
var _ toolexec.ToolRequestReattacher = (*WorkerDaemonRouter)(nil)

// WorkerDaemonRouterDeps is the bus the router routes over, the database the
// daemon records come from, and the control plane that wakes machines.
type WorkerDaemonRouterDeps struct {
	NATS         *NATSConn
	DB           *db.Repo
	ControlPlane *configv1.ControlPlaneConfig
	// AutomationTokens holds the trigger's delegated daemon:resume tokens.
	AutomationTokens automationtokens.Service
}

// NewWorkerDaemonRouter builds the worker's router. Hosted, the control plane
// resumes a suspended managed machine; the daemon records still come from
// the repository.
//
//forge:root-only worker
func NewWorkerDaemonRouter(d WorkerDaemonRouterDeps) (*WorkerDaemonRouter, error) {
	repo := d.DB
	opts := []toolexec.NATSRouterOption{toolexec.WithDatabase(repo)}
	var cpService controlplane.Service
	cpURL := ""
	if cp := ControlPlaneEndpoint(d.ControlPlane); cp.Hosted() {
		cpURL = cp.URL
		cpDeps := controlplane.Deps{ControlPlane: d.ControlPlane}
		if secret := cp.InternalServiceSecret; secret != "" {
			cpDeps.Sign = func() (string, error) { return auth.SignInternalServiceToken(secret) }
		}
		cpAdapter := controlplane.NewInstrumented(cpDeps)
		cpService = cpAdapter
		opts = append(opts,
			toolexec.WithDaemonResumer(cpAdapter),
			// The worker alone may fall back to a trigger's delegated
			// token: an unattended fire has no user JWT.
			toolexec.WithControlPlaneCredentials(automationcred.NewInstrumented(automationcred.Deps{Keys: d.AutomationTokens})))
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
	return &WorkerDaemonRouter{NATSDaemonRouter: router, ControlPlane: cpService, ControlPlaneURL: cpURL}, nil
}

// WorkerIntegrationCredentials is the credential source the worker's
// integration actions and polls authenticate with: saved connections, plus
// the brokers this deployment delegates to (control-plane's GitHub tokens,
// when hosted).
type WorkerIntegrationCredentials struct{ Source httpaction.CredentialSource }

// WorkerIntegrationCredentialsDeps is where saved connections and their
// secrets live, and the control plane delegated credentials come from.
type WorkerIntegrationCredentialsDeps struct {
	ConnectionVault connectionvault.Service
	DB              *db.Repo
	Vault           vault.Service
	Integrations    *IntegrationSettings
	ControlPlane    *configv1.ControlPlaneConfig
}

// NewWorkerIntegrationCredentials builds the source. It fails boot when a
// shipped manifest names an executor this binary did not register.
//
//forge:root-only worker
func NewWorkerIntegrationCredentials(d WorkerIntegrationCredentialsDeps) (*WorkerIntegrationCredentials, error) {
	resolver, err := newWorkerConnectionResolver(d.DB, d.ConnectionVault, d.Vault, d.Integrations.Webhooks.OAuthClients)
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
func newWorkerConnectionResolver(repo *db.Repo, vaultStore connectionvault.Service, keys vault.Service, clients connections.OAuthClients) (*connections.Resolver, error) {
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), clients)
	if err != nil {
		return nil, fmt.Errorf("connections: %w", err)
	}
	tokens := connections.NewTokenSource(vaultStore, keys, providers, nil)
	return connections.NewResolver(connections.ResolverDeps{Owners: connections.NewOwners(repo.ORM()), Runs: store.NewWorkflowExtStore(repo.ORM()), Store: vaultStore, Tokens: tokens}), nil
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
	ConnectionVault connectionvault.Service
	DB              *db.Repo
	Credentials     *WorkerIntegrationCredentials
}

// NewWorkerCatalogSearch builds the search.
//
//forge:root-only worker
func NewWorkerCatalogSearch(d WorkerCatalogSearchDeps) (*WorkerCatalogSearch, error) {
	svc, err := newWorkerCatalogSearch(d.ConnectionVault, d.Credentials.Source)
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

// WriteLedgerStoreDeps is where integration writes are recorded.
type WriteLedgerStoreDeps struct {
	DB *db.Repo
}

// NewWriteLedgerStore is the integration_writes table the write ledger
// (writeledger.Service, which compose.go builds) records every mutating
// integration call in, from the action node or an agent tool, before it is
// made, so the event it causes can be attributed.
func NewWriteLedgerStore(d WriteLedgerStoreDeps) (writeledger.Store, error) {
	return newWriteLedger(d.DB), nil
}

// newWriteLedger is the integration write ledger over repo's database.
func newWriteLedger(repo *db.Repo) *writeledger.Ledger {
	return writeledger.NewLedger(store.NewIntegrationWriteExtStore(repo.ORM()))
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
	o := d.DB.ORM()
	return reliantconfig.NewCachedStoredConfigProvider(configadapter.NewRepoConfigStore(configadapter.NewORMRecordReader(o)), store.NewProjectConfigExtStore(o), reliantconfig.DefaultParsedConfigCacheBytes), nil
}

// RunLifecycleDeps is what runs are recorded in and driven through.
type RunLifecycleDeps struct {
	DB       *db.Repo
	Backoff  providerbackoff.Service
	Temporal *TemporalClient
	Pause    *v2workflow.PauseService
}

// NewRunLifecycle is the run service: it records runs and drives their
// workflows.
func NewRunLifecycle(d RunLifecycleDeps) (*runs.Service, error) {
	return runs.New(runs.Deps{Repo: d.DB, Chats: store.NewChatsWithActivityExtStore(d.DB.ORM()), Resolver: questions.NewFromRepo(d.DB), Workflows: store.NewWorkflowExtStore(d.DB.ORM()), Questions: store.NewQuestionExtStore(d.DB.ORM()), Temporal: d.Temporal.Client, Pause: d.Pause, Waits: d.Backoff}), nil
}

// RunLauncherDeps is what a launch creates and starts.
type RunLauncherDeps struct {
	Projects projectaccess.Service
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
	return launch.New(launch.Deps{
		Store:         d.DB,
		Chats:         store.NewChatsWithActivityExtStore(d.DB.ORM()),
		Workflows:     store.NewWorkflowExtStore(d.DB.ORM()),
		Worktrees:     store.NewWorktreeExtStore(d.DB.ORM()),
		ProjectRows:   store.NewProjectExtStore(d.DB.ORM()),
		TriggerEvents: store.NewTriggerEventExtStore(d.DB.ORM()),
		Projects:      d.Projects,
		Selections:    store.NewChatSelectionExtStore(d.DB.ORM()),
		Threads:       threads.New(threads.Deps{Repo: d.DB}),
		Temporal:      d.Temporal.Client,
		Runs:          d.Runs,
		TaskQueue:     v2workflow.SharedTaskQueue,
		Pinner:        pinner,
	}), nil
}

// WorkerChat is the chat service as the run tools reach it on the worker.
type WorkerChat struct{ *chat.Service }

// WorkerChatDeps is the chat service's wiring on the worker.
type WorkerChatDeps struct {
	Logger   *slog.Logger
	Config   *config.Config
	DB       *db.Repo
	Backoff  providerbackoff.Service
	Temporal *TemporalClient
	Pause    *v2workflow.PauseService
	Router   *WorkerDaemonRouter
	// Updates writes the chat's refetch signals and publishes them to the hubs.
	Updates chatupdates.Service
}

// NewWorkerChat builds the chat service the run tools act through, as the
// calling chat's owner.
//
//forge:root-only worker
func NewWorkerChat(d WorkerChatDeps) (*WorkerChat, error) {
	svc, err := chat.New(chat.Deps{
		Logger:           d.Logger,
		Config:           d.Config,
		Repo:             d.DB,
		TriggerReads:     store.NewTriggerEventExtStore(d.DB.ORM()),
		Chats:            chataccess.NewFromORM(d.DB.ORM()),
		ChatRows:         store.NewChatExtStore(d.DB.ORM()),
		ChatViews:        store.NewChatsWithActivityExtStore(d.DB.ORM()),
		QuestionResolver: questions.NewFromRepo(d.DB),
		Workflows:        store.NewWorkflowExtStore(d.DB.ORM()),
		Worktrees:        store.NewWorktreeExtStore(d.DB.ORM()),
		ProjectRows:      store.NewProjectExtStore(d.DB.ORM()), TriggerEvents: store.NewTriggerEventExtStore(d.DB.ORM()),
		Questions:     store.NewQuestionExtStore(d.DB.ORM()),
		Spawns:        store.NewToolCallExtStore(d.DB.ORM()),
		Selections:    store.NewChatSelectionExtStore(d.DB.ORM()),
		Messages:      store.NewMessageExtStore(d.DB.ORM()),
		ToolBlocks:    store.NewMessageContentBlockExtStore(d.DB.ORM()),
		Senders:       store.NewThreadExtStore(d.DB.ORM()),
		Backoff:       d.Backoff,
		Temporal:      d.Temporal.Client,
		Pause:         d.Pause,
		Router:        d.Router,
		LocalModels:   local.NewRepoDirectory(d.DB, store.NewModelEndpointExtStore(d.DB.ORM())),
		Mailbox:       agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(d.DB.ORM()), Announcer: d.Updates}),
		AgentMessages: store.NewAgentMessageExtStore(d.DB.ORM()),
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
		Logger:    d.Logger,
		Config:    d.Config,
		Repo:      d.DB,
		RunLists:  store.NewRunListExtStore(d.DB.ORM()),
		Workflows: store.NewWorkflowExtStore(d.DB.ORM()),
		ChatRows:  store.NewChatExtStore(d.DB.ORM()),
		ChatViews: store.NewChatsWithActivityExtStore(d.DB.ORM()),
		Chats:     d.Chats.Service,
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
	ConnectionVault connectionvault.Service
	Logger          *slog.Logger
	Config          *config.Config
	DB              *db.Repo
	Temporal        *TemporalClient
	Vault           vault.Service
	Pollers         *webhook.Registry
	Integrations    *IntegrationSettings
	ControlPlane    *configv1.ControlPlaneConfig
	// AutomationKeys holds each daemon's delegated automation token;
	// trigger.New requires it whenever ControlPlane is set (hosted).
	AutomationKeys automationtokens.Service
	// Owners re-owns an orphaned trigger; trigger.New requires it.
	Owners triggerowner.Service
	// Gate decides which feature-gated tools an activation's approval card
	// names (trigger.Deps.Gate): the same gate the API server's trigger
	// service gets, so both roots answer one activation alike. Nil names no
	// gated tool.
	// forge:optional-dep
	Gate featuregate.Gate
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
	syncer, fires := trigger.TemporalBackend(temporalClient, triggers.NewStores(repo), v2workflow.SharedTaskQueue, d.Pollers.IsPolled)
	githubAccess, err := wireGitHubAccess(repo, d.ConnectionVault, d.Vault, d.ControlPlane, d.Integrations.Webhooks)
	if err != nil {
		return nil, fmt.Errorf("trigger service: %w", err)
	}
	var access map[string]trigger.IntegrationAccess
	if githubAccess != nil {
		access = map[string]trigger.IntegrationAccess{ghaccess.IntegrationID: gitHubAccess{githubAccess}}
	}
	// An org's linked GitHub App installations: a moved trigger bound to
	// one resumes only when its new org has it linked (ResumeAfterOrgMove).
	apps, err := gitTokens(d.ControlPlane)
	if err != nil {
		return nil, fmt.Errorf("trigger service: %w", err)
	}
	svc, err := trigger.New(trigger.Deps{
		OrgGitHubApps:  apps,
		Gate:           d.Gate,
		Logger:         d.Logger,
		Config:         d.Config,
		Repo:           repo,
		Chats:          store.NewChatExtStore(repo.ORM()),
		Firings:        store.NewTriggerEventExtStore(repo.ORM()),
		WebhookSecrets: store.NewTriggerExtStore(repo.ORM()), TriggerWrites: store.NewTriggerWritesExtStore(repo.ORM()), Triggers: store.NewTriggerExtStore(repo.ORM()), FiringReads: store.NewTriggerEventExtStore(repo.ORM()), Registrations: store.NewTriggerRegistrationExtStore(repo.ORM()),
		Owners:         d.Owners,
		Projects:       projectaccess.NewFromORM(repo.ORM()),
		OrgMoves:       store.NewProjectOrgMoveExtStore(repo.ORM()),
		Pools:          store.NewMachinePoolExtStore(repo.ORM()),
		Syncer:         syncer,
		Fires:          fires,
		ControlPlane:   hostedControlPlane(d.ControlPlane),
		AutomationKeys: d.AutomationKeys,
		Access:         access,
		Integrations:   d.Integrations.Config,
		Sealer:         d.Vault,
		Catalog:        d.Pollers,
		Intake: triggers.NewIntake(triggers.NewStores(repo), temporalClient, v2workflow.SharedTaskQueue).
			WithWorkflows(triggers.LaunchWorkflows{Repo: repo}).
			WithPoolCatalogs(machinepool.Catalogs{R: store.NewMachinePoolExtStore(repo.ORM())}).
			WithEnrichers(triggers.NewOriginEnricher(newWriteLedger(repo), store.NewTriggerEventExtStore(repo.ORM()), d.Integrations.GitHubAppSlug)),
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
	ConnectionVault connectionvault.Service
	DB              *db.Repo
	// Updates writes the plan, task and worktree refetches the tools announce.
	Updates       chatupdates.Service
	Backoff       providerbackoff.Service
	Network       *configv1.NetworkConfig
	Temporal      *TemporalClient
	AgentRuns     *agentruns.Runs
	Triggers      *WorkerTrigger
	Credentials   *WorkerIntegrationCredentials
	WriteLedger   writeledger.Service
	CatalogSearch *WorkerCatalogSearch
	Router        *WorkerDaemonRouter
	// WorkflowDrafts is the saved workflows the workflow and scenario tools
	// read and write.
	WorkflowDrafts workflowdrafts.Service
	// Membership names a person's org role for propose_connection, which
	// offers an org-wide connection only to an org owner or admin.
	Membership *orgs.Resolver
	// Logger and Config build the daemon registry service the machine label
	// and pool tools write through (machinePoolAdmin).
	Logger *slog.Logger
	Config *config.Config
	// ChangeController runs Forge Changes for list_changes / get_change /
	// manage_change; nil when self-hosted.
	// forge:optional-dep
	ChangeController tools.ChangeController
	// MachineController acts on the user's machines for list_machines /
	// manage_machine; nil when self-hosted.
	// forge:optional-dep
	MachineController tools.MachineController
}

// ChangeControllerDeps are what NewChangeController reads.
type ChangeControllerDeps struct {
	DB           *db.Repo
	ControlPlane *configv1.ControlPlaneConfig
}

// NewChangeController is nil when self-hosted: Changes live in the control
// plane, and the tools say so.
//
//forge:optional-component: Changes live in the control plane; a self-hosted worker has none
//forge:root-only worker
func NewChangeController(d ChangeControllerDeps) tools.ChangeController {
	if !ControlPlaneEndpoint(d.ControlPlane).Hosted() {
		return nil
	}
	return changetools.NewInstrumented(changetools.Deps{
		Projects:     store.NewProjectExtStore(d.DB.ORM()),
		ControlPlane: d.ControlPlane,
	})
}

// NewWorkerToolsFactory builds the factory.
//
//forge:root-only worker
func NewWorkerToolsFactory(d WorkerToolsFactoryDeps) (*WorkerToolsFactory, error) {
	repo, temporalClient := d.DB, d.Temporal.Client
	runMachine := toolexec.NewRunMachine(d.Router)
	planner, err := newToolPlanning(d.Logger, d.Config, repo)
	if err != nil {
		return nil, err
	}
	return &WorkerToolsFactory{tools.NewToolsFactory(&tools.ToolsOptions{
		Repo:               repo,
		SpawnQuestions:     store.NewQuestionExtStore(repo.ORM()),
		SpawnTree:          store.NewToolCallExtStore(repo.ORM()),
		SpawnMailbox:       agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}),
		Scenarios:          store.NewWorkflowScenarioExtStore(repo.ORM()),
		LaunchedRuns:       store.NewTriggerEventExtStore(repo.ORM()),
		WorktreeChats:      store.NewChatExtStore(repo.ORM()),
		WorkflowSlugs:      store.NewWorkflowDraftExtStore(repo.ORM()),
		Placement:          daemonplacement.NewRecords(repo.ORM()),
		Workflows:          store.NewWorkflowExtStore(repo.ORM()),
		Planning:           planner,
		Refetch:            d.Updates,
		AllowPrivateEgress: AllowPrivateEgress(d.Network),
		// The worker is where spawn_send actually executes (inside the
		// ExecuteTools activity), so this is the wiring that matters most for
		// agent-to-agent delivery.
		AgentMessageNotifier: temporal.NewAgentMessageNotifier(temporalClient, workersetup.ChatWorkflowLookup(tools.WithChats(repo))),
		// spawn_status reads which sub-agents are parked on a usage limit.
		ProviderBackoff: d.Backoff,
		// spawn_stop executes here too, and the stop is NOT best-effort: with
		// no stopper the tool reports that it cannot stop anything rather than
		// claiming a cancellation that never left the process.
		SpawnStopper: temporal.NewSpawnStopper(temporalClient, workersetup.ChatWorkflowLookup(tools.WithChats(repo)), repo),
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
		WorkflowDrafts: d.WorkflowDrafts,
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
		// list_machines / manage_machine act on the user's machines as the
		// user, through the router's control-plane adapter.
		MachineController: d.MachineController,
		// list_changes / get_change / manage_change run Forge Changes as the
		// user, through the control plane's ChangesService.
		ChangeController: d.ChangeController,
		// propose_connection raises its card and reads back the connection
		// the person made (metadata only, never the credential).
		ConnectionProposer: connectionproposer.New(connectionproposer.Deps{
			Approvals: store.NewApprovalExtStore(repo.ORM()), Connections: d.ConnectionVault, Roles: d.Membership,
		}),
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
		WorktreeSettler: worktreesweep.New(worktreesweep.NewStores(repo, store.NewWorktreeExtStore(repo.ORM())), d.Router.NATSDaemonRouter).
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
	Accounts provideraccounts.Service
}

// NewAccountRecorder builds the recorder.
//
//forge:root-only worker
func NewAccountRecorder(d AccountRecorderDeps) (*AccountRecorder, error) {
	return &AccountRecorder{Recorder: accountlimits.NewRecorder(d.Accounts, nil)}, nil
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
	// Checks is the record-only workflowcheck the fire and run activities
	// report to; the WorkflowCheckSyncer writes GitHub.
	Checks          *RecordOnlyChecks
	ConnectionVault connectionvault.Service
	DB              *db.Repo
	// Updates writes refetch signals and publishes them to the hubs.
	Updates          chatupdates.Service
	ProviderAccounts provideraccounts.Service
	OrgAccounts      orgaccounts.Service
	Temporal         *TemporalClient
	Streaming        *StreamingHub
	Tools            *WorkerToolsFactory
	Executor         *toolexec.RemoteExecutor
	Router           *WorkerDaemonRouter
	ConfigProvider   *reliantconfig.StoredConfigProvider
	Launcher         *launch.Launcher
	// Runs stops the run a trigger firing supersedes under
	// concurrency.cancel_running, the way a user's stop does.
	Runs         *runs.Service
	Credentials  *WorkerIntegrationCredentials
	WriteLedger  writeledger.Service
	Pollers      *webhook.Registry
	ControlPlane *configv1.ControlPlaneConfig
	Network      *configv1.NetworkConfig
	// Pipeline is ServerProcessConfig.pipeline: its enabled switch turns the
	// Reliant server actions (uses: reliant/...) on for this worker.
	Pipeline *configv1.PipelineConfig
	Recorder *AccountRecorder
	// WorkflowTaskSlots and WorkflowTaskPollers are WorkerConfig's
	// RELIANT_WORKER_MAX_CONCURRENT_WORKFLOW_TASKS and
	// RELIANT_WORKER_WORKFLOW_TASK_POLLERS (0: workersetup's default).
	WorkflowTaskSlots   int32
	WorkflowTaskPollers int32
	// Worker carries the hosted-action controller's endpoint and trust
	// bundle (hosted_actions_url, hosted_actions_ca_file); see HostedActions.
	Worker *configv1.WorkerConfig
	// NATS delivers the stop nudges the activities on this worker watch.
	NATS *NATSConn
	// Membership decides org resources in every activity (authz); the org
	// resolver.
	Membership *orgs.Resolver
	// Policies is the org policy resolver every activity carries
	// (orgpolicy).
	Policies *orgpolicy.Resolver
	// OrgMoves is the steps of a project's move to another org; nil, so
	// the activities are not registered, where NewOrgMoves says.
	// forge:optional-dep
	OrgMoves orgmove.Service
}

// orgAdminNotifier is the control plane as the worker's notifier of an org's
// admins, nil without one (a nil interface, never a nil client in one).
func orgAdminNotifier(d TemporalWorkerDeps) workersetup.OrgAdminNotifier {
	if d.Router == nil || d.Router.ControlPlane == nil {
		return nil
	}
	return d.Router.ControlPlane
}

// projectMoveAnnouncer is orgmove's Announcer: one refetch for the project's
// owner, written in the commit's own transaction (the ctx it is called with).
type projectMoveAnnouncer struct{ updates chatupdates.Service }

func (a projectMoveAnnouncer) AnnounceProjectMoved(ctx context.Context, ownerUserID, projectID string) error {
	return a.updates.EmitUserRefetch(ctx, ownerUserID, core.RefetchProjectOrgMoved, core.RefetchOpts{ProjectID: &projectID})
}

// orgMembers is the resolver as orgmove's Members, nil for a deployment with
// no orgs (a nil *orgs.Resolver would be a non-nil interface).
func orgMembers(r *orgs.Resolver) orgmove.Members {
	if r == nil {
		return nil
	}
	return r
}

// OrgMovesDeps are what NewOrgMoves reads.
type OrgMovesDeps struct {
	DB           *db.Repo
	Router       *WorkerDaemonRouter
	ControlPlane *configv1.ControlPlaneConfig
	// Triggers turns a moved project's triggers back on in their new org
	// (orgmove's ResumeTriggers step).
	Triggers   *WorkerTrigger
	Membership *orgs.Resolver
	// Updates announces the move to the owner's clients.
	Updates chatupdates.Service
}

// NewOrgMoves is the steps of a project's move to another org on this
// worker: nil, so the activities are not registered, without a control plane
// signed as the internal service (it cannot Prepare, Abort or Record) or the
// trigger service.
//
//forge:optional-component: a worker without a control plane signed as the internal service cannot move a project
//forge:root-only worker
func NewOrgMoves(d OrgMovesDeps) (orgmove.Service, error) {
	if d.Router == nil || d.Router.ControlPlane == nil || d.Triggers == nil || d.Triggers.Service == nil {
		return nil, nil
	}
	if ControlPlaneEndpoint(d.ControlPlane).InternalServiceSecret == "" {
		return nil, nil
	}
	o := d.DB.ORM()
	svc, err := orgmove.New(orgmove.Deps{
		Logger:       slog.Default(),
		Store:        store.NewProjectOrgMoveExtStore(o),
		Projects:     store.NewProjectExtStore(o),
		Announcer:    projectMoveAnnouncer{updates: d.Updates},
		ControlPlane: d.Router.ControlPlane,
		Triggers:     d.Triggers.Service,
		Members:      orgMembers(d.Membership),
	})
	if err != nil {
		return nil, err
	}
	return orgmove.NewServiceWithForgeMiddleware(svc), nil
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

// SecretResolver is the worker's resolver for declared secrets: the same private
// controller hop and trust as hosted actions, a different route. nil when there
// is no control plane (a step that uses a secret then fails with a clear
// reason rather than running with the placeholder).
func SecretResolver(log *slog.Logger, store hosteduse.Store, cfg *configv1.ControlPlaneConfig, worker *configv1.WorkerConfig) handlers.SecretResolver {
	if worker.GetHostedActionsUrl() == "" {
		return nil
	}
	cp := ControlPlaneEndpoint(cfg)
	if cp.InternalServiceSecret == "" {
		log.Warn("declared secrets are DISABLED: internal service signer is missing")
		return nil
	}
	secret := cp.InternalServiceSecret
	client, err := hosteduse.NewResolver(hosteduse.Deps{
		Store: store, Worker: worker,
		Sign: func() (string, error) { return auth.SignInternalServiceToken(secret) },
	})
	if err != nil {
		log.Warn("declared secrets are DISABLED on this worker until the control-plane URL is accepted", "error", err)
		return nil
	}
	return client
}

// disabledHostedActions refuses every hosted action with its configuration
// reason, which executeHosted publishes as the action's error.
type disabledHostedActions struct{ reason string }

func (d disabledHostedActions) ExecuteAction(context.Context, string, string, string, hostedhttp.Request) (*hostedhttp.Result, error) {
	return nil, &handlers.HostedDisabledError{Reason: d.reason}
}

// WorkerServerActions is the worker's Reliant server actions, as
// NewTemporalWorker hands them to workersetup: on only when the pipeline
// switch is. Its handlers are bound by NewTemporalWorker
// (workerServerActionHandlers); the dispatcher refuses every call while the
// switch is off, bound or not. The dispatcher's resolver is workersetup's,
// over the worker's database.
func WorkerServerActions(pipeline *configv1.PipelineConfig) serveraction.Config {
	enabled := pipeline.GetEnabled()
	if enabled {
		slog.Default().Info("Reliant server actions are ON (pipeline.enabled)")
	}
	return serveraction.Config{Enabled: enabled}
}

// workerServerActionHandlers binds the pipeline's server actions this
// worker implements: the pr.* actions (internal/pipeline/practions), over
// the worker's database, GitHub through the integration catalog with the
// run's credentials (writes recorded in the write ledger, as an action
// node's are), and the Inbox as async questions.
func workerServerActionHandlers(d TemporalWorkerDeps, creds httpaction.CredentialSource) (map[string]serveraction.Handler, error) {
	var runner writeledger.Runner = httpaction.NewRunner(netguard.New())
	if d.WriteLedger != nil {
		runner = d.WriteLedger.Wrap(runner)
	}
	actions, err := practions.New(practions.Deps{
		Store:  d.DB,
		GitHub: &practions.CatalogGitHub{Catalog: catalog.MustBuiltin(), Runner: runner, Credentials: creds},
		Inbox: practions.QuestionInbox{
			Writer: questions.NewFromRepo(d.DB), Reader: store.NewQuestionExtStore(d.DB.ORM()), Refetch: d.DB.UpdateWriter(),
		},
		Logger: slog.Default(),
	})
	if err != nil {
		return nil, err
	}
	return actions.Handlers(), nil
}

// NewTemporalWorker is the Temporal worker on the shared task queue
// (workersetup.Worker: its Drain releases in-flight activities, its Ready is
// "polling"). A scheduled trigger fire launches through the same launcher
// the run tools use.
//
//forge:root-only worker
func NewTemporalWorker(d TemporalWorkerDeps) (*workersetup.Worker, error) {
	creds := d.Credentials.Source
	hostedActions := HostedActions(slog.Default(), store.NewHostedHttpActionTicketExtStore(d.DB.ORM()), d.ControlPlane, d.Worker)
	secretResolver := SecretResolver(slog.Default(), store.NewHostedHttpActionTicketExtStore(d.DB.ORM()), d.ControlPlane, d.Worker)
	// Stop nudges reach this worker's activities over NATS (stopnudge). If
	// the subscription cannot be made, stops still arrive on the regular
	// heartbeat, one throttle window later: degrade, never refuse to run.
	nudges := stopnudge.NewHub()
	if _, err := natsutil.SubscribeWithRecovery(d.NATS.Conn, stopnudge.Subject, func(m *nats.Msg) { nudges.HandleMessage(m.Data) }); err != nil {
		slog.Default().Warn("stop nudges are DISABLED on this worker: pause and interrupt reach running activities on their next heartbeat",
			"subject", stopnudge.Subject, "error", err)
	}
	serverActions := WorkerServerActions(d.Pipeline)
	handlers, err := workerServerActionHandlers(d, creds)
	if err != nil {
		return nil, fmt.Errorf("server actions: %w", err)
	}
	serverActions.Handlers = handlers
	worker, _, err := workersetup.NewWorker(&workersetup.Config{
		OrgMoves:               d.OrgMoves,
		StopNudges:             nudges,
		AccountLimits:          d.Recorder.Recorder,
		OrgAdmins:              orgAdminNotifier(d),
		TemporalClient:         d.Temporal.Client,
		Database:               d.DB,
		Checks:                 d.Checks,
		ProviderAccounts:       d.ProviderAccounts,
		Connections:            d.ConnectionVault,
		OrgAccounts:            d.OrgAccounts,
		StreamingHub:           d.Streaming.Hub,
		ToolsFactory:           d.Tools.ToolsFactory,
		ToolExecutor:           d.Executor,
		DaemonRouter:           d.Executor.DaemonRouter(),
		MCPBinder:              toolexec.NewDaemonMCPContextBinder(d.Router),
		ConfigProvider:         d.ConfigProvider,
		TriggerLauncher:        d.Launcher,
		TriggerRunCanceller:    triggerRunCanceller(d.Runs),
		Membership:             d.Membership,
		Policies:               d.Policies,
		IntegrationCredentials: creds,
		HostedActions:          hostedActions,
		SecretResolver:         secretResolver,
		ServerActions:          serverActions,
		WriteLedger:            d.WriteLedger,
		TriggerPollers:         d.Pollers,
		TriggerPollInbox:       webhook.PollInbox{Store: d.DB},
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

// triggerRunCanceller is the run service as the trigger firers' canceller,
// or nil (no canceller, rather than a nil *runs.Service inside an interface).
func triggerRunCanceller(r *runs.Service) triggers.RunCanceller {
	if r == nil {
		return nil
	}
	return r
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
	return workflowevent.NewRelay(store.NewRunEventExtStore(d.DB.ORM()), d.Temporal.Client, v2workflow.SharedTaskQueue).
		WithRoutes(store.NewTriggerExtStore(d.DB.ORM())), nil
}

type TriggerDeliveryRelayDeps struct {
	ConnectionVault connectionvault.Service
	DB              *db.Repo
	Temporal        *TemporalClient
	Integrations    *IntegrationSettings
	Membership      *orgs.Resolver
	Policies        *orgpolicy.Resolver
	// Pipeline is the pipeline.enabled switch: on, the PR-record projector
	// also projects the PR pipeline's enrolled repositories (prrecords
	// Deps.Pipeline). Off, it is exactly the records projector.
	Pipeline *configv1.PipelineConfig
	// ControlPlane is where the projector mints the GitHub App token it
	// reads a repository's .reliant/pipeline.yaml with; none means no App.
	ControlPlane *configv1.ControlPlaneConfig
}

// TriggerDeliveryRelay gives the inbox live membership and policy context.
type TriggerDeliveryRelay struct {
	relay      *webhook.Relay
	membership authz.Membership
	policies   *orgpolicy.Resolver
}

// NewTriggerDeliveryRelay routes authenticated receipts on the worker.
//
//forge:root-only worker
func NewTriggerDeliveryRelay(d TriggerDeliveryRelayDeps) (*TriggerDeliveryRelay, error) {
	repo := d.DB
	intake := triggers.NewIntake(triggers.NewStores(repo), d.Temporal.Client, v2workflow.SharedTaskQueue).
		WithWorkflows(triggers.LaunchWorkflows{Repo: repo}).
		WithPoolCatalogs(machinepool.Catalogs{R: store.NewMachinePoolExtStore(repo.ORM())}).
		WithEnrichers(triggers.NewOriginEnricher(newWriteLedger(repo), store.NewTriggerEventExtStore(repo.ORM()), d.Integrations.GitHubAppSlug))
	var membership authz.Membership
	if d.Membership != nil {
		membership = d.Membership
	}
	relay := webhook.NewRelay(webhook.NewStores(repo), intake, d.ConnectionVault, triggers.NewAutoPause(store.NewTriggerWritesExtStore(repo.ORM()), nil))
	records := prrecords.Deps{Store: store.NewPrRecordExtStore(repo.ORM()), Scope: prrecords.NewScope(d.Integrations.PRRecordRepositories)}
	if d.Pipeline.GetEnabled() {
		records.Pipeline = repo
		tokens, err := gitTokens(d.ControlPlane)
		if err != nil {
			return nil, err
		}
		if tokens != nil {
			records.Contents = ghcontents.NewInstrumented(ghcontents.Deps{Tokens: tokens})
		}
	}
	if !records.Scope.Empty() || records.Pipeline != nil {
		relay.WithDeliveryConsumer(webhook.PRRecordConsumer(prrecords.NewInstrumented(records)))
	}
	return &TriggerDeliveryRelay{relay: relay, membership: membership, policies: d.Policies}, nil
}

func (r *TriggerDeliveryRelay) Run(ctx context.Context) error {
	return r.relay.Run(orgpolicy.WithResolver(authz.WithMembership(ctx, r.membership), r.policies))
}

// RecordDueScannerDeps is what the record_due due scan needs.
type RecordDueScannerDeps struct {
	DB       *db.Repo
	Temporal *TemporalClient
	// Pipeline is the pipeline.enabled switch: off, there is no scanner.
	Pipeline   *configv1.PipelineConfig
	Membership *orgs.Resolver
	Policies   *orgpolicy.Resolver
}

// NewRecordDueScanner is the worker's due scan for record_due triggers
// (recorddue): every worker runs one while pipeline.enabled is on, and the
// scans split the due rows (SKIP LOCKED). Off, nil: no scanner runs and no
// record_due trigger fires.
//
//forge:root-only worker
func NewRecordDueScanner(d RecordDueScannerDeps) (recorddue.Service, error) {
	if !d.Pipeline.GetEnabled() {
		return nil, nil
	}
	repo := d.DB
	intake := triggers.NewIntake(triggers.NewStores(repo), d.Temporal.Client, v2workflow.SharedTaskQueue).
		WithWorkflows(triggers.LaunchWorkflows{Repo: repo}).
		WithPoolCatalogs(machinepool.Catalogs{R: store.NewMachinePoolExtStore(repo.ORM())})
	var membership authz.Membership = authz.NoOrgs{}
	if d.Membership != nil {
		membership = d.Membership
	}
	policies := d.Policies
	return recorddue.NewInstrumented(recorddue.Deps{Store: repo, Intake: intake, Scope: func(ctx context.Context) context.Context {
		return orgpolicy.WithResolver(authz.WithMembership(ctx, membership), policies)
	}}), nil
}

// WorkflowCheckSyncerDeps is the GitHub-writing checks configuration and the
// org policies a write is judged by.
type WorkflowCheckSyncerDeps struct {
	Checks   *SyncingChecks
	Policies *orgpolicy.Resolver
	// Membership answers whether a check's owner is still a member of its
	// trigger's org, asked before every write (workflowcheck.ownerMayWrite);
	// nil is a deployment with no orgs.
	Membership *orgs.Resolver
}

// NewWorkflowCheckSyncer writes the GitHub check runs workflows' `check:`
// blocks declare (workflowcheck): every worker runs one; checks are leased
// with SKIP LOCKED, so they share the queue. What it writes with, and why a
// deployment with no control plane writes nothing, is SyncingChecks.
//
//forge:root-only worker
func NewWorkflowCheckSyncer(d WorkflowCheckSyncerDeps) (*workflowcheck.Syncer, error) {
	policies := d.Policies
	var membership authz.Membership = authz.NoOrgs{}
	if d.Membership != nil {
		membership = d.Membership
	}
	return workflowcheck.NewSyncer(d.Checks.Service, func(ctx context.Context) context.Context {
		return orgpolicy.WithResolver(authz.WithMembership(ctx, membership), policies)
	}), nil
}

// AccountUsagePollerDeps is where the polled accounts are kept, and the
// Temporal client that wakes a user's blocked chats when a poll finds an
// account serves again.
type AccountUsagePollerDeps struct {
	DB       *db.Repo
	Accounts provideraccounts.Service
	Backoff  providerbackoff.Service
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
	return &accountlimits.Poller{
		Store: d.Accounts,
		Fetcher: &accountlimits.Fetcher{
			Store:  d.Accounts,
			Tokens: drivers.RefreshingTokens{Store: d.Accounts},
			// A poll that lifts a bench wakes the user's blocked chats, so their
			// threads re-route onto the account now rather than at a reset.
			OnCapacity: func(ctx context.Context, userID, _ string) {
				capacitywake.WakeUser(ctx, d.Backoff, d.Temporal.Client, userID, "a usage poll found a limited account serving again")
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
	return &WriteLedgerPrune{ledger: d.Ledger, writes: newWriteLedger(d.DB)}, nil
}

// Run prunes until ctx ends.
func (p *WriteLedgerPrune) Run(ctx context.Context) error {
	p.ledger.PruneLoop(ctx, p.writes)
	return nil
}

// ledgerRetentionInterval is how often the ledger retention pass runs. Each
// pass covers only what aged out since the last one, so an hour keeps it
// small once the first pass has worked off the backlog.
const ledgerRetentionInterval = time.Hour

// ledgerRetentionPause paces a pass's batches (db.LedgerRetentionPause).
const ledgerRetentionPause = db.LedgerRetentionPause(20 * time.Millisecond)

// LedgerRetention compacts chat update logs and prunes finished runs' internal
// steps and node observations past their retention
// (internal/db/ledger_retention.go), hourly. Every
// replica runs it: the work is idempotent and skips what another replica is
// deleting.
type LedgerRetention struct {
	repo         *db.Repo
	chatUpdates  time.Duration
	steps        time.Duration
	observations time.Duration
	startDelay   time.Duration
	deleted      metric.Int64Counter

	// after and runPass are time.After and pass; a test replaces them.
	after   func(time.Duration) <-chan time.Time
	runPass func(ctx context.Context, now time.Time)
}

// LedgerRetentionDeps is the repository, the three retention periods (0: off)
// and how long after start the first pass waits (0: at once).
type LedgerRetentionDeps struct {
	DB                     *db.Repo
	ChatUpdateRetention    time.Duration
	StepExecutionRetention time.Duration
	// ObservationRetention is node_observations' period (worker config
	// node_observation_retention).
	ObservationRetention time.Duration
	StartDelay           time.Duration
}

// NewLedgerRetention builds the retention loop.
//
//forge:root-only worker
func NewLedgerRetention(d LedgerRetentionDeps) (*LedgerRetention, error) {
	deleted, err := otel.Meter("github.com/reliant-labs/reliant/internal/app/ledger_retention").Int64Counter(
		"reliant.ledger_retention.rows_deleted",
		metric.WithUnit("{row}"),
		metric.WithDescription("Rows the ledger retention pass deleted, by table."))
	// An instrument the SDK refuses still comes back usable (a no-op).
	otel.Handle(err)
	l := &LedgerRetention{
		repo: d.DB, chatUpdates: d.ChatUpdateRetention, steps: d.StepExecutionRetention,
		observations: d.ObservationRetention, startDelay: d.StartDelay, deleted: deleted, after: time.After,
	}
	l.runPass = l.pass
	return l, nil
}

// Run waits out the start delay, then runs a pass, then one an hour, until
// ctx ends.
//
// The delay is what keeps compaction from racing a rollout. An api-server
// of a release before the compaction watermark existed does not read it, so
// it would replay a stale reconnect straight through compacted rows. A worker
// that starts in the same rollout deletes nothing until the delay is over, by
// which time the api-servers are on a release that reads the watermark. A
// rollout longer than the delay re-opens that window, and only the rollout
// that introduces the watermark has such servers at all: every later release
// reads it.
func (l *LedgerRetention) Run(ctx context.Context) error {
	if l.chatUpdates <= 0 && l.steps <= 0 && l.observations <= 0 {
		logging.InfoContext(ctx, "ledger retention is off: every retention period is 0")
		return nil
	}
	if l.startDelay > 0 {
		logging.InfoContext(ctx, "ledger retention: first pass waits out the start delay", "delay", l.startDelay)
		select {
		case <-ctx.Done():
			return nil
		case <-l.after(l.startDelay):
		}
	}
	ticker := time.NewTicker(ledgerRetentionInterval)
	defer ticker.Stop()
	for {
		l.runPass(ctx, time.Now())
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// pass is one retention pass at now. A failure is logged and retried by the
// next pass: everything it records (watermarks, the step cursor) is recorded
// only for work that is done.
func (l *LedgerRetention) pass(ctx context.Context, now time.Time) {
	if l.chatUpdates > 0 {
		cutoff := now.Add(-l.chatUpdates)
		var res db.ChatCompactionResult
		err := observe.RunRoot(ctx, "worker.ledger_retention.chat_updates", func(ctx context.Context) error {
			var err error
			res, err = l.repo.CompactChatUpdates(ctx, cutoff, ledgerRetentionPause)
			return err
		})
		l.deleted.Add(context.WithoutCancel(ctx), res.Deleted, metric.WithAttributes(attribute.String("table", "chat_updates")))
		attrs := []any{"chats", res.Chats, "examined", res.Examined, "deleted", res.Deleted, "cutoff", cutoff}
		switch {
		case err != nil && ctx.Err() == nil:
			logging.WarnContext(ctx, "ledger retention: compacting chat updates failed", append(attrs, "error", err)...)
		case res.Chats > 0:
			logging.InfoContext(ctx, "ledger retention: compacted chat updates", attrs...)
		}
	}
	if l.steps > 0 {
		cutoff := now.Add(-l.steps)
		var res db.StepRetentionResult
		err := observe.RunRoot(ctx, "worker.ledger_retention.step_executions", func(ctx context.Context) error {
			var err error
			res, err = l.repo.PruneStepExecutions(ctx, cutoff, ledgerRetentionPause)
			return err
		})
		l.deleted.Add(context.WithoutCancel(ctx), res.Deleted, metric.WithAttributes(attribute.String("table", "step_executions")))
		attrs := []any{"runs", res.Runs, "deleted", res.Deleted, "cutoff", cutoff}
		switch {
		case err != nil && ctx.Err() == nil:
			logging.WarnContext(ctx, "ledger retention: pruning step executions failed", append(attrs, "error", err)...)
		case res.Runs > 0:
			logging.InfoContext(ctx, "ledger retention: pruned step executions", attrs...)
		}
	}
	if l.observations > 0 {
		cutoff := now.Add(-l.observations)
		var res db.StepRetentionResult
		err := observe.RunRoot(ctx, "worker.ledger_retention.node_observations", func(ctx context.Context) error {
			var err error
			res, err = l.repo.PruneNodeObservations(ctx, cutoff, ledgerRetentionPause)
			return err
		})
		l.deleted.Add(context.WithoutCancel(ctx), res.Deleted, metric.WithAttributes(attribute.String("table", "node_observations")))
		attrs := []any{"runs", res.Runs, "deleted", res.Deleted, "cutoff", cutoff}
		switch {
		case err != nil && ctx.Err() == nil:
			logging.WarnContext(ctx, "ledger retention: pruning node observations failed", append(attrs, "error", err)...)
		case res.Runs > 0:
			logging.InfoContext(ctx, "ledger retention: pruned node observations", attrs...)
		}
	}
}

// The project purge (internal/db/project_schedule.go): a deleted project is
// hidden at once and purged once its restore window (core.ProjectDeleteWindow)
// has passed. Every replica runs it: each project is claimed for
// projectPurgeLease before its batches run, so replicas do not purge the same
// one at once, and a claim a crashed replica left is taken again after it.
const (
	// projectPurgeInterval is how often a pass runs: a project is purged at
	// most this long after its window ends.
	projectPurgeInterval = 15 * time.Minute
	// projectPurgeLease is how long a claim holds a project: the one
	// projectpurge.Lease names, which an immediate purge's detached batches
	// stay inside (internal/handlers/project).
	projectPurgeLease = projectpurge.Lease
	// projectPurgeBatch bounds the projects a pass purges, and the orphaned
	// rows per table it sweeps.
	projectPurgeBatch = 50
)

// ProjectPurge purges the projects whose scheduled deletion is due, every
// projectPurgeInterval, until its context ends.
type ProjectPurge struct {
	purges projectpurge.Service
}

// ProjectPurgeDeps is the purge service.
type ProjectPurgeDeps struct {
	Purges projectpurge.Service
}

// NewProjectPurge builds the purge loop.
//
//forge:root-only worker
func NewProjectPurge(d ProjectPurgeDeps) (*ProjectPurge, error) {
	return &ProjectPurge{purges: d.Purges}, nil
}

// Run runs a pass, then one every projectPurgeInterval, until ctx ends. A
// purged project's trigger rows go with it; their schedules (paused when the
// delete paused the triggers) are dropped by the API's startup sync, which
// removes every schedule whose row is gone.
func (p *ProjectPurge) Run(ctx context.Context) error {
	// The line dev-docs/project-delete-rollout.md checks for after a deploy.
	logging.InfoContext(ctx, "project purge: running", "interval", projectPurgeInterval, "lease", projectPurgeLease)
	ticker := time.NewTicker(projectPurgeInterval)
	defer ticker.Stop()
	for {
		err := observe.RunRoot(ctx, "worker.project_purge", func(ctx context.Context) error {
			res, err := p.purges.PurgeDueProjects(ctx, time.Now(), projectPurgeLease, projectPurgeBatch)
			if res.Purged > 0 || res.Failed > 0 || res.OrphansSwept > 0 {
				logging.InfoContext(ctx, "project purge: pass done", "purged", res.Purged, "failed", res.Failed, "orphans_swept", res.OrphansSwept)
			}
			return err
		})
		if err != nil && ctx.Err() == nil {
			logging.WarnContext(ctx, "project purge: the pass could not list the projects due; the next pass retries", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// newToolPlanning is the plan, task and dependency store the planning tools
// write through.
func newToolPlanning(logger *slog.Logger, cfg *config.Config, repo *db.Repo) (planning.Service, error) {
	return planning.NewInstrumented(planning.Deps{
		Logger:       logger,
		Config:       cfg,
		Plans:        store.NewPlanExtStore(repo.ORM()),
		Tasks:        store.NewTaskExtStore(repo.ORM()),
		Dependencies: store.NewTaskDependencyExtStore(repo.ORM()),
	})
}
