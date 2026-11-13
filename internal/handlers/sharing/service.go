package sharing

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Repository is the slice of *db.Repo read-only sharing uses: the rows it
// decides on, the one writer of a visibility (with its share event), and the
// shared lists.
type Repository interface {
	GetChat(ctx context.Context, id string) (*core.Chat, error)
	GetProject(ctx context.Context, id string) (*core.Project, error)
	ApplyShareChange(ctx context.Context, c core.ShareChange) (bool, error)
	ListSharedChats(ctx context.Context, f core.SharedChatFilters) ([]*core.Chat, error)
	ListSharedProjects(ctx context.Context, f core.SharedProjectFilters) ([]*core.Project, error)
}

// OrgResolver names the org a shared list is of: the one the request names,
// verified a live membership of the caller, else the caller's default.
// *orgs.Resolver is one.
type OrgResolver interface {
	Named(ctx context.Context, userID, requested string) (string, error)
	DefaultOrg(ctx context.Context, userID string) (string, error)
}

// Deps holds dependencies for the sharing service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	Repo   Repository
	// Orgs resolves the org a shared list is of. Without one (a deployment
	// with no control plane) there are no orgs, so nothing is shared and the
	// lists are empty.
	//
	// forge:optional-dep
	Orgs OrgResolver
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("sharing: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("sharing: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("sharing: Deps.Repo is required")
	}
	return nil
}

// Service implements the sharing Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedSharingServiceHandler
	deps Deps
}

// New creates a new sharing service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "sharing"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewSharingServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_sharing_proto
