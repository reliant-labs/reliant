package worktree_reclaim

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The worktree-reclaim service answers a machine's worktree-reclaim inbox
// item. The item is built from the proposals the machine's daemon put on its
// last disk report (DiskReport.worktree_reclaim, stored in
// daemons.storage_state). Each decision is sent back to that daemon as
// worktree.reclaim_decide, which records it exactly as `reliant worktree
// reclaim approve|decline` does; each recorded approval is then sent as
// worktree.reclaim_approve, the authenticated command that is the only thing
// that makes the daemon remove a worktree with work only it holds (owner
// decision 2026-11-10). The daemon, not this server, removes it, capture
// first and every check made again.

// Repository is what the service reads: the caller's machine, its stored
// report, and whether it is connected.
type Repository interface {
	GetDaemon(ctx context.Context, id string) (*core.Daemon, error)
	GetDaemonStorageState(ctx context.Context, daemonID string) (string, error)
	ListAttachedDaemonIDsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]string, error)
	// GetLiveChatIDForWorktree is a non-archived chat bound to a Reliant
	// workspace, "" when none: its checkout is never removed on approval.
	GetLiveChatIDForWorktree(ctx context.Context, worktreeID string) (string, error)
}

// Sender sends one command to one of the user's daemons and returns its
// reply.
type Sender interface {
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// Deps holds dependencies for the worktree_reclaim service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type. The rpc methods on *Service are where this service's logic lives;
// see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	Repo   Repository
	// Sender reaches the machine's daemon. nil when no daemon router is
	// wired; DecideWorktreeReclaim then reports unavailable.
	// forge:optional-dep
	Sender Sender
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("worktree_reclaim: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("worktree_reclaim: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("worktree_reclaim: Deps.Repo is required")
	}
	return nil
}

// Service implements the worktree_reclaim Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedWorktreeReclaimServiceHandler
	deps Deps
}

// New creates a new worktree_reclaim service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "worktree_reclaim"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewWorktreeReclaimServiceHandler(s, opts...)
	mux.Handle(path, handler)
}

// RegisterHTTP registers plain HTTP handlers (callbacks, REST endpoints)
// outside Connect RPC. The stack provides recovery/logging/audit; auth is
// per-route. Override to add endpoints — forge.yaml webhooks are wired
// automatically (do not call RegisterWebhookRoutes here). See the forge
// `api` skill.
func (s *Service) RegisterHTTP(_ *http.ServeMux, _ func(http.Handler) http.Handler) {
}

// Ensure the generated protobuf package import remains referenced even before methods exist.
var _ = pb.File_reliant_v1_worktree_reclaim_proto
