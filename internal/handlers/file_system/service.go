package file_system

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

// Deps holds dependencies for the file_system service. internal/grpc/server.go
// constructs it today, and mounts it only when the API server has a daemon
// router: the files live on the user's machines, never on this server.
// forge's composition root takes over in P3c (research/forge-migration/PLAN.md).
// The rpc methods on *Service are where this service's logic lives; see the
// forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Router carries each request to the machine that holds its workspace.
	Router Router
	// Repo resolves a request's workspace, the machine it is on, and that
	// machine's owner.
	Repo Repository
}

// Repository is the slice of db.Repository the filesystem handlers use.
type Repository interface {
	// GetChat, GetWorktree and GetProject resolve a request's workspace
	// (filepreview.ResolveBasePath) and its machine (daemonplacement).
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	// ListProjectDaemonsForProject reads a missing checkout's clone records.
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error)
	// GetDaemon checks that a machine about to be woken is the caller's
	// (machinewake.Owners).
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// Router is the slice of toolexec.DaemonRouter the filesystem handlers use.
// A router that can also resume a suspended machine (machinewake.Resumer)
// has asleep machines woken; see wake.
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	ResolveDaemonID(ctx context.Context, userID string) (string, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("file_system: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("file_system: Deps.Config is required")
	}
	if d.Router == nil {
		return fmt.Errorf("file_system: Deps.Router is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("file_system: Deps.Repo is required")
	}
	return nil
}

// Service implements the file_system Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedFileSystemServiceHandler
	deps Deps
}

// New creates a new file_system service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "file_system"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewFileSystemServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_filesystem_proto
