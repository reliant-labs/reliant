package settings

import (
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the settings service. internal/grpc/server.go
// constructs it today; forge's composition root takes over in P3c
// (research/forge-migration/PLAN.md). The rpc methods on *Service are where
// this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo stores settings, provider credentials and privacy choices. It is
	// the whole db.Repository: the handlers call over 25 of its methods and
	// hand it to drivers.HasStaleReliantKey, which takes the whole thing.
	Repo db.Repository
	// ControlPlane mints the Reliant provider's LLM key (SyncReliantProvider).
	// server.go passes the deployment's client.
	ControlPlane controlplane.Client
	// ExchangeClaudeCode trades a Claude OAuth authorization code for tokens.
	// server.go passes claude.ExchangeClaudeAuthorizationCode; a test passes a
	// fake so CompleteClaudeOAuth never reaches Anthropic's token endpoint.
	ExchangeClaudeCode func(code, codeVerifier, redirectURI, state string) (*claude.ClaudeTokens, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("settings: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("settings: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("settings: Deps.Repo is required")
	}
	if d.ControlPlane == nil {
		return fmt.Errorf("settings: Deps.ControlPlane is required")
	}
	if d.ExchangeClaudeCode == nil {
		return fmt.Errorf("settings: Deps.ExchangeClaudeCode is required")
	}
	return nil
}

// Service implements the settings Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedSettingsServiceHandler
	deps Deps
}

// New creates a new settings service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "settings"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewSettingsServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_settings_proto
