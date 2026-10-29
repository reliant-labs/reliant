package package_commands

import (
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the package_commands service. The composition
// graph constructs it (internal/app/compose.go); every rpc reaches a daemon
// through the router, because command discovery and every package-command
// process run on the machine that holds the workspace.
// The rpc methods on *Service are where this service's logic lives; see the
// forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Router carries command discovery to the user's daemon.
	Router Router
	// Repo resolves worktrees and stores command favorites.
	Repo Repository
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("package_commands: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("package_commands: Deps.Config is required")
	}
	if d.Router == nil {
		return fmt.Errorf("package_commands: Deps.Router is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("package_commands: Deps.Repo is required")
	}
	return nil
}

// Service implements the package_commands Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedPackageCommandsServiceHandler
	deps Deps
	// owners settles the machine of a workspace whose row records none.
	owners *daemonplacement.Resolver
}

// New creates a new package_commands service instance with the supplied Deps.
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
	return "package_commands"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewPackageCommandsServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_package_commands_proto
