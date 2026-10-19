package provider_account

import (
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the provider_account service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type. The rpc methods on *Service are where this service's logic lives;
// see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// UsageRefresher reads an account's usage on demand
	// (RefreshProviderAccountUsage). A server that cannot reach the account
	// store leaves it nil and the rpc answers Unavailable.
	// forge:optional-dep
	UsageRefresher accountlimits.Refresher
	// Add your dependencies here (e.g. Repo Repository). Required ones get
	// a validateDeps() check below. Tag intentionally-optional fields with
	// the `forge:optional-dep` directive directly above the field.
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("provider_account: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("provider_account: Deps.Config is required")
	}
	// Add checks for your required Deps fields here. Example:
	//   if d.Repo == nil { return fmt.Errorf("provider_account: Deps.Repo is required") }
	return nil
}

// Service implements the provider_account Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedProviderAccountServiceHandler
	deps Deps
}

// New creates a new provider_account service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "provider_account"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewProviderAccountServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_provider_account_proto
