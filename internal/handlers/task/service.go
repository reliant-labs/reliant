package task

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the task service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads and writes tasks, and resolves the plan, thread and chat
	// that authorize them.
	Repo Repository
}

// Repository is the slice of db.Repository the task handlers use.
type Repository interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	GetPlan(ctx context.Context, id string) (*db.Plan, error)
	CreateTask(ctx context.Context, task *db.Task) error
	GetTask(ctx context.Context, id string) (*db.Task, error)
	ListTasksByPlan(ctx context.Context, planID string) ([]*db.Task, error)
	UpdateTask(ctx context.Context, task *db.Task) error
	DeleteTask(ctx context.Context, id string) error
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("task: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("task: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("task: Deps.Repo is required")
	}
	return nil
}

// Service implements the task Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedTaskServiceHandler
	deps Deps
}

// New creates a new task service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "task"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewTaskServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_task_proto
