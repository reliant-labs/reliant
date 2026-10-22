package workflow

import (
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the workflow service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo stores the caller's workflow drafts and reads the projects,
	// project-synced workflows and visibility settings a listing merges.
	Repo Repository
	// Router writes a saved workflow back to its project file on the
	// caller's machine (SaveWorkflow with a source path). The API server
	// always has one; without it that save answers Unavailable.
	// forge:optional-dep
	Router Sender
	// Automations stops what outlives the rows of automations deleted with
	// their workflow (their schedules, the access only they held). Without
	// it the rows are still deleted; their schedules are left to the
	// periodic sync.
	// forge:optional-dep
	Automations AutomationRetirer
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("workflow: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("workflow: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("workflow: Deps.Repo is required")
	}
	return nil
}

// Service implements the workflow Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedWorkflowServiceHandler
	deps Deps
}

// New creates a new workflow service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "workflow"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewWorkflowServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_workflow_proto
