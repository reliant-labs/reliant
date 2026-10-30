package worktree

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the worktree service. The composition graph
// constructs it (internal/app/compose.go), and the api root mounts it with the
// machine-wake interceptor (apiMountOptions): a worktree lives on one of the
// user's machines, and a request that finds that machine asleep wakes it. The
// rpc methods on *Service are where this service's logic lives; see the forge
// `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads and writes worktrees, their projects, repos and chats
	// (deps.go).
	Repo Repository
	// GitCredentials resolves the owner's current credential only at call time.
	GitCredentials GitCredentialProvider
	// Router carries each worktree command to the machine that holds the
	// workspace. A router that can also resume a suspended machine
	// (machinewake.Resumer) has asleep machines woken; see wake. The API
	// server always has one; without it every rpc that needs the machine
	// fails, and the DB-only rpcs still answer.
	// forge:optional-dep
	Router Router
	// Temporal cancels the workflows of the chats archived with a worktree.
	// Without it archiving leaves them running to their own end.
	// forge:optional-dep
	Temporal WorkflowCanceler
	// Settler removes an archived worktree's directory when that is provably
	// safe (worktreesweep). Without it archiving leaves the directory for the
	// periodic sweep, and a permanent delete does not wait on the machine.
	// forge:optional-dep
	Settler Settler
}

// Router is the slice of toolexec.DaemonRouter the worktree handlers use.
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	ResolveDaemonID(ctx context.Context, userID string) (string, error)
}

// WorkflowCanceler cancels a running Temporal workflow. client.Client
// implements it.
type WorkflowCanceler interface {
	CancelWorkflow(ctx context.Context, workflowID string, runID string) error
}

// Settler is what archiving needs from the sweep: one immediate attempt to
// settle the directory of a worktree that was just archived.
// *worktreesweep.Sweeper implements it.
type Settler interface {
	Settle(ctx context.Context, userID string, cand *core.ReclaimCandidate)
	SettleBlocking(ctx context.Context, userID string, cand *core.ReclaimCandidate) *core.CleanupMetadata
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("worktree: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("worktree: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("worktree: Deps.Repo is required")
	}
	if d.GitCredentials == nil {
		return fmt.Errorf("worktree: Deps.GitCredentials is required")
	}
	return nil
}

// Service implements the worktree Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedWorktreeServiceHandler
	deps Deps
	// owners settles the machine of a workspace whose row records none; see
	// owner.
	owners *daemonplacement.Resolver
}

// New creates a new worktree service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	probe, _ := deps.Router.(daemonplacement.OwnerProbe)
	return &Service{deps: deps, owners: daemonplacement.NewResolver(deps.Repo, probe)}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "worktree"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewWorktreeServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_worktree_proto
