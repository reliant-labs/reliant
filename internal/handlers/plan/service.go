package plan

import (
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the plan service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Plans reads and writes plans.
	Plans PlanStore

	// Tasks lists a plan's tasks.
	Tasks TaskStore

	// Threads and Chats resolve the thread and chat that authorize a plan.
	Threads ThreadStore
	Chats   ChatStore

	// Refetch tells a chat's clients a plan or task changed.
	Refetch ChatRefetcher

	// DB is the handle the generated CRUD ops run on, and the transactions
	// of their scoped writes. *orm.Client satisfies it.
	DB orm.Context
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("plan: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("plan: Deps.Config is required")
	}
	if d.Plans == nil {
		return fmt.Errorf("plan: Deps.Plans is required")
	}
	if d.Tasks == nil {
		return fmt.Errorf("plan: Deps.Tasks is required")
	}
	if d.Threads == nil {
		return fmt.Errorf("plan: Deps.Threads is required")
	}
	if d.Chats == nil {
		return fmt.Errorf("plan: Deps.Chats is required")
	}
	if d.Refetch == nil {
		return fmt.Errorf("plan: Deps.Refetch is required")
	}
	if d.DB == nil {
		return fmt.Errorf("plan: Deps.DB is required")
	}
	return nil
}

// Service implements the plan Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedPlanServiceHandler
	deps Deps
}

// New creates a new plan service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "plan"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewPlanServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_plan_proto
