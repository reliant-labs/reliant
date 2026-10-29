package chat

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"connectrpc.com/connect"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
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

	// owners settles the machine of a workspace whose row records none, so a
	// chat started in it is placed and checked against its real machine.
	ownersOnce sync.Once
	ownersImpl *daemonplacement.Resolver
}

// New creates a new chat service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{
		deps: deps,
		runs: runs.NewService(deps.Repo, deps.Temporal, deps.Pause),
		threads: threads.NewService(deps.Repo,
			threads.WithTemporalSignaler(deps.Temporal),
			threads.WithToolCanceler(deps.Router),
		),
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
		s.launcherImpl = launch.NewLauncher(s.deps.Repo, threads.NewService(s.deps.Repo), s.deps.Temporal, s.runs, workflow.SharedTaskQueue,
			launch.WithChatPinner(s.chatPinner()))
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

// workspaceOwners returns the resolver every check of a chat's workspace
// machine goes through. A router that cannot report which machines are
// connected (none wired, or a test double) leaves unattributed rows as they
// are.
func (s *Service) workspaceOwners() *daemonplacement.Resolver {
	s.ownersOnce.Do(func() {
		probe, _ := s.deps.Router.(daemonplacement.OwnerProbe)
		s.ownersImpl = daemonplacement.NewResolver(s.deps.Repo, probe)
	})
	return s.ownersImpl
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
