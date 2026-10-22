package system

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"go.temporal.io/sdk/client"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the system service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
//
// Every collaborator is optional: Ready checks the dependencies this process
// actually runs with, so nil means "not in use here", not "misconfigured".
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo is pinged by Ready.
	// forge:optional-dep
	Repo Pinger
	// Temporal is health-checked by Ready.
	// forge:optional-dep
	Temporal TemporalHealth
	// NATSConnected reports whether the NATS connection is up; nil means
	// this process does not use NATS.
	// forge:optional-dep
	NATSConnected func() bool
	// StreamingHub is reported not ready while disconnected.
	// forge:optional-dep
	StreamingHub ConnectionReporter
	// OAuthProvider is the provider StartOAuthSignIn logs in through
	// (RELIANT_AUTH_URL / RELIANT_AUTH_KEY); zero, the sign-in reports it is
	// not configured.
	// forge:optional-dep
	OAuthProvider auth.Provider
}

// Pinger is the database health check Ready runs.
type Pinger interface {
	Ping(ctx context.Context) error
}

// TemporalHealth is the Temporal health check Ready runs.
type TemporalHealth interface {
	CheckHealth(ctx context.Context, request *client.CheckHealthRequest) (*client.CheckHealthResponse, error)
}

// ConnectionReporter reports whether a long-lived connection is up.
type ConnectionReporter interface {
	IsConnected() bool
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("system: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("system: Deps.Config is required")
	}
	return nil
}

// Service implements the system Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedSystemServiceHandler
	deps Deps
}

// New creates a new system service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "system"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewSystemServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_system_proto
