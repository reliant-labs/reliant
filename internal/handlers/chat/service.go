package chat

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the chat service. The composition graph
// constructs it (internal/app/compose.go), and the worker root builds its own
// for the run-management tools (internal/app/providers_worker.go). The rpc
// methods on *Service are where this service's logic lives; see the forge
// `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo stores chats, threads, messages and runs. Repository (deps.go)
	// is what the handlers call plus the roles of launch, threads, runs and
	// the other callees they hand it to.
	Repo Repository
	// Chats reads a chat the caller may write, the owner check and the org
	// check in one call (chataccess).
	Chats chataccess.Service
	// ChatRows and ChatViews read a chat's own row and its computed view
	// (ChatRows, ChatViews; deps.go).
	ChatRows  ChatRows
	ChatViews ChatViews
	// QuestionResolver resolves a question when a chat resumes past it.
	QuestionResolver QuestionResolver
	// Backoff reads and releases the provider-backoff markers of the chats' runs.
	Backoff Backoff
	// Questions lists the chat's open questions (Questions, deps.go).
	Questions Questions
	// Spawns reads the spawn tree (Spawns, deps.go).
	Spawns Spawns
	// Selections stores each chat's composer selection (chatselection.Store;
	// store.ChatSelectionExtStore satisfies it).
	Selections chatselection.Store
	// Mailbox queues messages for a thread and announces them (Mailbox,
	// deps.go).
	Mailbox Mailbox
	// AgentMessages reads and removes mailbox rows (AgentMessages, deps.go).
	AgentMessages AgentMessages
	// Messages answers the send's idempotency checks (Messages, deps.go).
	Messages Messages
	// ToolBlocks reads tool-result blocks for the branch repair and
	// GetToolResult (ToolBlocks, deps.go).
	ToolBlocks ToolBlocks
	// Senders titles the senders of the messages a read assembles
	// (store.ThreadExtStore satisfies it).
	Senders Senders
	// Temporal starts, queries, signals and cancels chat runs.
	Temporal Temporal
	// Pause pauses and resumes runs. runs.Service drives it; the handlers
	// never call it themselves.
	Pause runs.PauseController
	// Router reaches the user's daemon. Interrupting a thread cancels its
	// running tools through it, and an attended turn wakes a suspended
	// machine through it when the router can wake (toolexec.DaemonWaker).
	// Without it neither happens: the interrupt still lands, and the turn's
	// tool calls wait for the machine.
	// forge:optional-dep
	Router Router
	// LocalModels lists the models the user's machines and endpoints serve,
	// so an explicit pick of a local model is checked the way its run will
	// resolve it. Without it a local pick cannot be checked and is refused.
	// forge:optional-dep
	LocalModels local.Directory
	// Orgs says which organization a new chat acts in and verifies the org
	// a list names (internal/orgs). Nil, or a nil *orgs.Resolver, is a
	// deployment with no orgs: chats take their project's org, or none, and
	// lists ignore org_id.
	// forge:optional-dep
	Orgs OrgResolver

	// Workflows, Worktrees and ProjectRows are the by-id reads the run
	// lifecycle and the launcher make (store.WorkflowExtStore,
	// WorktreeExtStore, ProjectExtStore).
	Workflows   launch.WorkflowLoader
	Worktrees   launch.WorktreeReader
	ProjectRows launch.ProjectReader
	// TriggerEvents is the ledger a launch dedupes against
	// (store.TriggerEventExtStore).
	TriggerEvents launch.TriggerEventStore
	// TriggerReads reads the launch event of an existing chat, for the restarts
	// that rebuild its WorkflowInput.
	TriggerReads TriggerReads
}

// Temporal is the slice of the Temporal client the chat handlers use, and
// what they hand to runs and launch.
type Temporal interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error)
	// QueryWorkflowWithOptions, not QueryWorkflow: every query goes through
	// temporal.QueryOpenRun, which targets the current run and refuses a
	// closed one instead of having a worker replay it.
	QueryWorkflowWithOptions(ctx context.Context, request *client.QueryWorkflowWithOptionsRequest) (*client.QueryWorkflowWithOptionsResponse, error)
	SignalWorkflow(ctx context.Context, workflowID string, runID string, signalName string, arg interface{}) error
	CancelWorkflow(ctx context.Context, workflowID string, runID string) error
	DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
}

// Router is the slice of the daemon router the chat handlers use: it cancels
// one running tool execution (threads.ToolCanceler).
type Router interface {
	SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error
}

// Backoff reads and releases the provider-backoff markers of a chat's runs.
// providerbackoff.Service satisfies it.
type Backoff interface {
	// ClearUsageLimitWait releases a stopped run's markers (runs.UsageLimitWaits).
	ClearUsageLimitWait(ctx context.Context, chatID, threadID string, now time.Time) error
	// ListCapacityWakeTargets lists the root runs of a user's chats that may
	// have a thread blocked on a provider (WakeUserChats).
	ListCapacityWakeTargets(ctx context.Context, userID string) ([]string, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("chat: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("chat: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("chat: Deps.Repo is required")
	}
	if d.TriggerReads == nil {
		return fmt.Errorf("chat: Deps.TriggerReads is required")
	}
	if d.Chats == nil {
		return fmt.Errorf("chat: Deps.Chats is required")
	}
	if d.ChatRows == nil {
		return fmt.Errorf("chat: Deps.ChatRows is required")
	}
	if d.ChatViews == nil {
		return fmt.Errorf("chat: Deps.ChatViews is required")
	}
	if d.QuestionResolver == nil {
		return fmt.Errorf("chat: Deps.QuestionResolver is required")
	}
	if d.Backoff == nil {
		return fmt.Errorf("chat: Deps.Backoff is required")
	}
	if d.Questions == nil {
		return fmt.Errorf("chat: Deps.Questions is required")
	}
	if d.Spawns == nil {
		return fmt.Errorf("chat: Deps.Spawns is required")
	}
	if d.Selections == nil {
		return fmt.Errorf("chat: Deps.Selections is required")
	}
	if d.Mailbox == nil {
		return fmt.Errorf("chat: Deps.Mailbox is required")
	}
	if d.AgentMessages == nil {
		return fmt.Errorf("chat: Deps.AgentMessages is required")
	}
	if d.Messages == nil {
		return fmt.Errorf("chat: Deps.Messages is required")
	}
	if d.ToolBlocks == nil {
		return fmt.Errorf("chat: Deps.ToolBlocks is required")
	}
	if d.Senders == nil {
		return fmt.Errorf("chat: Deps.Senders is required")
	}
	if d.Temporal == nil {
		return fmt.Errorf("chat: Deps.Temporal is required")
	}
	if d.Pause == nil {
		return fmt.Errorf("chat: Deps.Pause is required")
	}
	return nil
}

// Service implements the chat Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedChatServiceHandler
	deps Deps

	// runs owns pause/resume. Handlers ask it for an outcome and render that;
	// they hold no PauseService handle, so the lifecycle machinery has exactly
	// one door. See internal/runs.
	runs    *runs.Service
	threads *threads.Service

	// launcher owns the start path — see internal/launch. It is shared with the
	// worker, which cannot import this package, so every helper StartChat and
	// SendMessage need lives there and this service calls through.
	launcherOnce sync.Once
	launcherImpl *launch.Launcher

	// pinner chooses and stores the machine of a chat that names none. Lazy
	// for the same reason the launcher is.
	pinnerOnce sync.Once
	pinnerImpl *daemonplacement.Pinner
}

// fixtureChats is nil in every binary, so production must supply
// Deps.Chats: validateDeps refuses a nil one, and the composition root's
// wiring is held to it by internal/app's TestChatAccessIsWired. Only this
// package's tests set it (chats_fixture_test.go), so the many fixtures that
// build a Service over a repository without naming Chats read chats through
// that repository (chataccesstest.For) instead of each wiring it by hand.
var fixtureChats func(Repository) chataccess.Service

// chats is Deps.Chats; in this package's tests, a fixture that left it nil
// gets the one fixtureChats builds over its repository.
func (s *Service) chats() chataccess.Service {
	if s.deps.Chats == nil && fixtureChats != nil {
		return fixtureChats(s.deps.Repo)
	}
	return s.deps.Chats
}

// fixtureChatReads fills ChatRows and ChatViews for the tests (nil in every
// binary, like fixtureChats). The generated test harness hands an unnamed role
// its mock, so a fixture that named neither is recognized by that mock and
// reads through its own repository instead.
var fixtureChatReads func(Repository) (ChatRows, ChatViews)

// isMockReads reports a role the harness filled with its generated mock.
func isMockReads(v any) bool {
	switch v.(type) {
	case nil, *MockChatRows, *MockChatViews:
		return true
	}
	return false
}

// fixtureQuestionResolver fills QuestionResolver for the tests, as
// fixtureChatReads fills the chat reads: nil in every binary.
var fixtureQuestionResolver func(Repository) QuestionResolver

// isMockResolver reports a role the harness filled with its generated mock.
func isMockResolver(v QuestionResolver) bool {
	switch v.(type) {
	case nil, *MockQuestionResolver:
		return true
	}
	return false
}

// questionResolver is Deps.QuestionResolver; in this package's tests a fixture
// that left it unnamed resolves through its own repository.
func (s *Service) questionResolver() QuestionResolver {
	if isMockResolver(s.deps.QuestionResolver) && fixtureQuestionResolver != nil {
		return fixtureQuestionResolver(s.deps.Repo)
	}
	return s.deps.QuestionResolver
}

// chatRows is Deps.ChatRows; in this package's tests, a fixture that left it
// unnamed gets the one fixtureChatReads builds over its repository.
func (s *Service) chatRows() ChatRows {
	if isMockReads(s.deps.ChatRows) && fixtureChatReads != nil {
		rows, _ := fixtureChatReads(s.deps.Repo)
		return rows
	}
	return s.deps.ChatRows
}

// chatViews is Deps.ChatViews, with the same fixture default.
func (s *Service) chatViews() ChatViews {
	if isMockReads(s.deps.ChatViews) && fixtureChatReads != nil {
		_, views := fixtureChatReads(s.deps.Repo)
		return views
	}
	return s.deps.ChatViews
}

// fixtureTriggerReads fills TriggerReads for the tests (nil in every binary:
// validateDeps refuses a nil one), as fixtureChatReads fills the chat reads.
var fixtureTriggerReads func(Repository, TriggerReads) TriggerReads

// triggerReads is Deps.TriggerReads; in this package's tests a fixture that
// left it unnamed (the harness's mock) reads through its own repository.
func (s *Service) triggerReads() TriggerReads {
	if fixtureTriggerReads != nil {
		return fixtureTriggerReads(s.deps.Repo, s.deps.TriggerReads)
	}
	return s.deps.TriggerReads
}

// New creates a new chat service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if deps.Chats == nil && fixtureChats != nil {
		deps.Chats = fixtureChats(deps.Repo)
	}
	if fixtureQuestionResolver != nil && isMockResolver(deps.QuestionResolver) {
		deps.QuestionResolver = fixtureQuestionResolver(deps.Repo)
	}
	if fixtureChatReads != nil {
		rows, views := fixtureChatReads(deps.Repo)
		if isMockReads(deps.ChatRows) {
			deps.ChatRows = rows
		}
		if isMockReads(deps.ChatViews) {
			deps.ChatViews = views
		}
	}
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{
		deps:    deps,
		runs:    runs.New(runs.Deps{Repo: deps.Repo, Chats: deps.ChatViews, Resolver: deps.QuestionResolver, Workflows: deps.Workflows, Questions: deps.Questions, Temporal: deps.Temporal, Pause: deps.Pause, Waits: deps.Backoff}),
		threads: threads.New(threads.Deps{Repo: deps.Repo, Chats: deps.Chats, Signaler: deps.Temporal, ToolCanceler: deps.Router}),
	}, nil
}

// launcher returns this service's start-path launcher, building it on first use.
//
// It is built lazily rather than in New because tests construct Service as a
// struct literal (`&Service{deps: Deps{Repo: repo}}`) to exercise one method,
// and a launcher that only existed when the constructor ran would make every
// one of those a nil dereference. The dependencies it takes are the service's
// own fields, so a lazily built launcher and an eagerly built one cannot
// disagree.
func (s *Service) launcher() *launch.Launcher {
	s.launcherOnce.Do(func() {
		s.launcherImpl = launch.New(launch.Deps{Store: s.deps.Repo, Chats: s.chatViews(), Workflows: s.deps.Workflows, Worktrees: s.deps.Worktrees, ProjectRows: s.deps.ProjectRows, TriggerEvents: s.deps.TriggerEvents, Projects: s.deps.Repo, Selections: s.deps.Selections, Threads: threads.New(threads.Deps{Repo: s.deps.Repo}), Temporal: s.deps.Temporal, Runs: s.runs, TaskQueue: workflow.SharedTaskQueue, Pinner: s.chatPinner()})
	})
	return s.launcherImpl
}

// chatPinner returns the pinner every run start in this service goes through:
// a chat that names no machine is pinned to one before its run starts, so it
// stays there when another machine connects (research/CHAT_MACHINE_PLACEMENT.md).
// A router that cannot place (none wired, or a test double) leaves only the
// chat's own pin and its worktree's owner to place it.
func (s *Service) chatPinner() *daemonplacement.Pinner {
	s.pinnerOnce.Do(func() {
		machines, _ := s.deps.Router.(daemonplacement.Machines)
		s.pinnerImpl = daemonplacement.NewPinner(s.deps.Repo, machines)
	})
	return s.pinnerImpl
}

// durable is the repository and spawn tree together, for the runtime's
// *FromDurableState helpers.
func (s *Service) durable() durableState {
	return durableState{Repository: s.deps.Repo, Spawns: s.deps.Spawns}
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "chat"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewChatServiceHandler(s, opts...)
	mux.Handle(path, handler)
}

// RegisterHTTP registers plain HTTP handlers (callbacks, REST endpoints)
// outside Connect RPC. The stack provides recovery/logging/audit; auth is
// per-route. Override to add endpoints — forge.yaml webhooks are wired
// automatically (do not call RegisterWebhookRoutes here). See the forge
// `api` skill.
func (s *Service) RegisterHTTP(_ *http.ServeMux, _ func(http.Handler) http.Handler) {
	// No HTTP endpoints by default. Example:
	//   mux.Handle("POST /api/callbacks/oauth", stack(http.HandlerFunc(s.HandleOAuthCallback)))
}

// Ensure the generated protobuf package import remains referenced even before methods exist.
var _ = pb.File_reliant_v1_chat_proto
