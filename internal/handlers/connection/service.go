package connection

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the connection service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Connections owns every connection: the sealed credentials, the OAuth
	// broker and the audit trail. The handlers validate the wire, extract
	// the caller, convert and map errors; no rpc returns a secret value.
	Connections Connections

	// OrgGitHubApps lists the GitHub App installations an org has linked at
	// control-plane, which ListConnections shows the org's members as the
	// org's GitHub connections (org-sharing.md §7.1). Unset (self-hosted, no
	// control plane) shows none.
	//
	// forge:optional-dep
	OrgGitHubApps OrgGitHubApps
}

// OrgGitHubApps is the slice of control-plane's git credential service that
// names an org's linked installations. gitcredentialclient's client satisfies
// it.
type OrgGitHubApps interface {
	ListOrgGitHubInstallations(ctx context.Context, orgID string) ([]gitcredentialclient.OrgInstallation, error)
}

// Connections is the slice of *connections.Service the handlers serve.
// *connections.Service implements it.
type Connections interface {
	ListIntegrations() []connections.Integration
	OrgIntegrationPolicies(ctx context.Context, userID, orgID string, integrationIDs []string) (map[string]connections.IntegrationPolicy, error)
	List(ctx context.Context, userID, integrationID string) ([]*core.Connection, error)
	ListOrg(ctx context.Context, userID, orgID, integrationID string) ([]*core.Connection, error)
	Get(ctx context.Context, userID, id string) (*core.Connection, error)
	CreateAPIKey(ctx context.Context, p connections.CreateAPIKeyParams) (*core.Connection, error)
	StartOAuth(ctx context.Context, p connections.StartParams) (string, error)
	CompleteOAuthFlow(ctx context.Context, p connections.CompleteParams) (*connections.Completion, error)
	Test(ctx context.Context, userID, id string) (*connections.TestResult, error)
	Rename(ctx context.Context, userID, id, name string) (*core.Connection, error)
	SetDefault(ctx context.Context, userID, id string) (*core.Connection, error)
	Delete(ctx context.Context, userID, id string) error
	Events(ctx context.Context, userID, id string, limit int, beforeID int64) ([]core.ConnectionEvent, error)

	// An org's own connections (owner_org_id on the request).
	GetOrg(ctx context.Context, userID, orgID, id string) (*core.Connection, error)
	TestOrg(ctx context.Context, userID, orgID, id string) (*connections.TestResult, error)
	RenameOrg(ctx context.Context, userID, orgID, id, name string) (*core.Connection, error)
	SetDefaultOrg(ctx context.Context, userID, orgID, id string) (*core.Connection, error)
	DeleteOrg(ctx context.Context, userID, orgID, id string) error
	EventsOrg(ctx context.Context, userID, orgID, id string, limit int, beforeID int64) ([]core.ConnectionEvent, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("connection: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("connection: Deps.Config is required")
	}
	if d.Connections == nil {
		return fmt.Errorf("connection: Deps.Connections is required")
	}
	return nil
}

// Service implements the connection Connect RPC service over
// connections.Service.
type Service struct {
	reliantv1connect.UnimplementedConnectionServiceHandler
	deps Deps
}

// New creates a new connection service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "connection"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewConnectionServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_connection_proto
