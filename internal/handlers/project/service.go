package project

import (
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the project service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo stores projects and what hangs off them: their daemons, repos,
	// main worktree, seeded config row and prompts.
	Repo Repository
	// Router runs every filesystem and git operation on the caller's
	// daemon, which owns the filesystem. The API server always has one; in
	// a composition without one, the daemon-backed steps fail through their
	// existing error paths and the database-only rpcs still work.
	// forge:optional-dep
	Router Router
	// ControlPlane clones a repository onto a managed daemon for
	// CreateProjectFromRepo. Nil in self-hosted deployments, where that rpc
	// reports that cloning needs a cloud account: the user's GitHub token
	// lives in the control plane and nowhere else, and defaulting to
	// localhost would fail obscurely against a control plane that is not
	// there.
	// forge:optional-dep
	ControlPlane CloneDispatcher
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("project: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("project: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("project: Deps.Repo is required")
	}
	return nil
}

// Service implements the project Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedProjectServiceHandler
	deps Deps
}

// New creates a new project service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "project"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewProjectServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_project_proto
