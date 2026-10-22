package inbox

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the inbox service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads what waits on the caller (pending rows, automation
	// health, machines and their storage) and records dismissals.
	Repo Repository
	// Sweeper runs the confirmed storage clean-up on a machine's daemon.
	// nil when no daemon router is wired; CleanupStorage then reports
	// unavailable.
	// forge:optional-dep
	Sweeper Sweeper
}

// Repository is the slice of db.Repository the inbox handlers use. It
// includes worktreesweep.StorageStore because the storage view and the
// clean-up read through the same store.
type Repository interface {
	worktreesweep.StorageStore
	// WorkflowResolver reads an activation's workflow, to tell which
	// automations still need setting up.
	launch.WorkflowResolver

	ListInboxPending(ctx context.Context, userID string) ([]*core.InboxPending, error)
	ListDismissedInboxItemIDs(ctx context.Context, userID string, itemIDs []string) (map[string]bool, error)
	DismissInboxItems(ctx context.Context, userID string, itemIDs []string, at time.Time) error
	RestoreInboxItems(ctx context.Context, userID string, itemIDs []string) error

	ListTriggers(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error)
	RecentTriggerFirings(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
	FiringsSinceLastSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
	ListTriggerRegistrations(ctx context.Context, userID string, triggerIDs []string) (map[string]*core.TriggerRegistration, error)

	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
}

// Sweeper starts a storage clean-up on one of the caller's machines.
// *worktreesweep.Sweeper implements it.
type Sweeper interface {
	StartCleanup(ctx context.Context, store worktreesweep.StorageStore, userID, daemonID string, wantIDs []string) (accepted []string, skipped []worktreesweep.CleanupResult, err error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("inbox: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("inbox: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("inbox: Deps.Repo is required")
	}
	return nil
}

// Service implements the inbox Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedInboxServiceHandler
	deps Deps
	// storageCache holds each user's storage view for storageViewTTL.
	storageCache sync.Map
}

// New creates a new inbox service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "inbox"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewInboxServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_inbox_proto
