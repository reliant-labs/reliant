package forge

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

// Deps holds dependencies for the forge service. internal/grpc/server.go
// constructs it today, and mounts it only when the API server has a daemon
// router and RELIANT_DISABLE_FORGE_API is unset: a forge project's state is
// on the user's machine, never on this server. forge's composition root takes
// over in P3c (research/forge-migration/PLAN.md). The rpc methods on *Service
// are where this service's logic lives; see the forge `api` / `services`
// skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Router carries each forge.* command to the caller's daemon.
	Router Router
	// Repo resolves a project to its path on the daemon, enforcing that the
	// caller owns it, and records forge's name for the project.
	Repo Repository
}

// Router is the slice of toolexec.DaemonRouter the forge handlers use
// (daemoncmd.Sender).
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// Repository is the only database capability this service needs: resolve a
// project id to its row, enforcing that the caller owns it, and record forge's
// name for the project when a topology report carries one.
//
// Two methods are all this service uses, and a narrow dependency is what makes
// the handler testable without a database — which is what lets the
// secret-safety and reachability tests run as plain unit tests.
// db.Repository satisfies it.
type Repository interface {
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	SetProjectForgeName(ctx context.Context, id, userID, forgeProjectName string) (bool, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("forge: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("forge: Deps.Config is required")
	}
	if d.Router == nil {
		return fmt.Errorf("forge: Deps.Router is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("forge: Deps.Repo is required")
	}
	return nil
}

// Service implements the forge Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedForgeServiceHandler
	deps Deps
}

// New creates a new forge service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "forge"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewForgeServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_forge_proto
