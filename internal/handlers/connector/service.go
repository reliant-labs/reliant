package connector

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the connector service. internal/grpc/server.go
// constructs it today; forge's composition root takes over in P3c
// (research/forge-migration/PLAN.md). The rpc methods on *Service are where
// this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Grants stores connector grants (the POLICY), their audit log and the
	// OAuth clients bound to them.
	Grants GrantStore

	// Tokens mints and revokes each grant's credential: an `rlat_` access
	// token with mcp:connector, bound to resource connector:<grantID>,
	// acting as the grant's user. The grant row holds the POLICY; the token
	// authority holds the credential.
	Tokens TokenAuthority

	// PublicURL is the externally reachable base URL, used to tell the user
	// where to point their MCP client. Empty answers with the path alone.
	PublicURL string
}

// GrantStore is the slice of connectorgrant.Store the connector handlers use.
type GrantStore interface {
	CreateGrant(ctx context.Context, g *connectorgrant.Grant) error
	SetTokenPrefix(ctx context.Context, id, prefix string) error
	GetGrant(ctx context.Context, userID, id string) (*connectorgrant.Grant, error)
	ListGrantsByUser(ctx context.Context, userID string) ([]*connectorgrant.Grant, error)
	RevokeGrant(ctx context.Context, userID, id string) (bool, error)
	ListAuditByUser(ctx context.Context, userID string, limit int) ([]*connectorgrant.AuditRecord, error)
	ListAuditByGrant(ctx context.Context, userID, grantID string, limit int) ([]*connectorgrant.AuditRecord, error)
	PutBinding(ctx context.Context, b *connectorgrant.ClientBinding) error
	ListBindingsByUser(ctx context.Context, userID string) ([]*connectorgrant.ClientBinding, error)
	DeleteBinding(ctx context.Context, userID, clientID string) (bool, error)
}

// TokenAuthority is the slice of tokenauthority.Authority that issues and
// kills grant credentials.
type TokenAuthority interface {
	MintForUser(ctx context.Context, req tokenauthority.MintRequest) (tokenauthority.Minted, error)
	RevokeResource(ctx context.Context, resource fat.Resource) (int64, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("connector: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("connector: Deps.Config is required")
	}
	if d.Grants == nil {
		return fmt.Errorf("connector: Deps.Grants is required")
	}
	if d.Tokens == nil {
		return fmt.Errorf("connector: Deps.Tokens is required")
	}
	return nil
}

// Service is the JWT-authed surface for managing connector grants.
//
// Connector credentials themselves authenticate the MCP endpoint, never this
// service: a connector cannot mint or widen itself, only a logged-in user can.
type Service struct {
	reliantv1connect.UnimplementedConnectorServiceHandler
	deps Deps
}

// New creates a new connector service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "connector"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewConnectorServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_connector_proto
