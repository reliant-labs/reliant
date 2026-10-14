package repo

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the repo service. internal/grpc/server.go
// constructs it today; forge's composition root takes over in P3c
// (research/forge-migration/PLAN.md). The rpc methods on *Service are where
// this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads and writes a project's nested repos and checks that the
	// caller owns the project.
	Repo Repository
	// Router runs repo discovery and git inspection on the caller's
	// daemon, which owns the filesystem. The API server always has one; in
	// a composition without one, the rpcs that need the filesystem fail
	// and listing and removing still work.
	// forge:optional-dep
	Router Sender
}

// Repository is the slice of db.Repository the repo handlers use.
type Repository interface {
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	UpdateProject(ctx context.Context, project *db.Project, userID string) error

	GetRepo(ctx context.Context, id string) (*core.Repo, error)
	GetRepoByProjectAndPath(ctx context.Context, projectID, relativePath string) (*core.Repo, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	CreateRepo(ctx context.Context, repo *core.Repo) error
	DeleteRepo(ctx context.Context, id string) error
}

// Sender sends one request/response command to a user's daemon.
// toolexec.DaemonRouter implements it.
type Sender interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("repo: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("repo: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("repo: Deps.Repo is required")
	}
	return nil
}

// Service implements the repo Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedRepoServiceHandler
	deps Deps
}

// New creates a new repo service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "repo"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewRepoServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_repo_proto
