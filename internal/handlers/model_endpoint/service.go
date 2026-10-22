package model_endpoint

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/modelendpoints"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the model_endpoint service. The composition
// graph constructs it (internal/app/compose.go). The rpc methods on *Service
// are where this service's logic lives; see the forge `api` / `services`
// skill. Service is the thin Connect layer over modelendpoints.Manager, which
// New builds from these.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo stores the caller's endpoints.
	Repo modelendpoints.Store
	// Router relays VIA_DAEMON endpoints to the caller's machine. Without it
	// (no daemon router configured) those report the relay unavailable.
	// forge:optional-dep
	Router Router
	// Credentials seals endpoint API keys. Without it credential storage is
	// unavailable.
	// forge:optional-dep
	Credentials modelendpoints.EndpointCredentials
	// ControlPlane is the control plane hosting this deployment. Its URL
	// picks which addresses a user-supplied endpoint URL may resolve to
	// (netguard.ForDeployment): a URL is hosted, so no private targets; none
	// is self-hosted, so the user's own network. Required, and an empty
	// block (not nil) is self-hosted, so a forgotten wire fails New instead
	// of silently choosing a policy.
	ControlPlane *configv1.ControlPlaneConfig
}

// Router is the slice of toolexec.DaemonRouter a VIA_DAEMON endpoint
// relays through. toolexec.DaemonRouter implements it.
type Router interface {
	RefreshLocalModels(ctx context.Context, userID, daemonID string) (*pb.LocalModelInventory, error)
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("model_endpoint: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("model_endpoint: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("model_endpoint: Deps.Repo is required")
	}
	if d.ControlPlane == nil {
		return fmt.Errorf("model_endpoint: Deps.ControlPlane is required (an empty ControlPlaneConfig is self-hosted)")
	}
	return nil
}

// Service implements the model_endpoint Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedModelEndpointServiceHandler
	deps    Deps
	manager *modelendpoints.Manager
}

// New creates a new model_endpoint service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field. The
// network policy for endpoint targets is derived from Deps.ControlPlane and
// fixed at construction.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	cfg := modelendpoints.Config{
		Store:       deps.Repo,
		Credentials: deps.Credentials,
		Policy:      netguard.ForDeployment(deps.ControlPlane.GetUrl()),
	}
	if deps.Router != nil {
		cfg.Daemons = routerDaemonControl{router: deps.Router}
	}
	return &Service{deps: deps, manager: modelendpoints.NewManager(cfg)}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "model_endpoint"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewModelEndpointServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_model_endpoint_proto
