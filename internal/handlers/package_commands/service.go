package package_commands

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the package_commands service.
// internal/grpc/server.go constructs it today, and mounts it only when the
// API server has a daemon router: command discovery runs on the user's
// machine. forge's composition root takes over in P3c
// (research/forge-migration/PLAN.md). The rpc methods on *Service are where
// this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Router carries command discovery to the user's daemon.
	Router Router
	// Repo resolves worktrees and stores command favorites.
	Repo Repository
	// Processes runs package commands and tracks their processes, on this
	// server (see RunCommand).
	Processes Processes
}

// Router is the slice of toolexec.DaemonRouter ListCommands uses
// (daemoncmd.Sender).
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// Repository is the slice of db.Repository the package-commands handlers use.
type Repository interface {
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	ListCommandFavorites(ctx context.Context, userID, projectID string) ([]string, error)
	AddCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error
	RemoveCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error
}

// Processes is the slice of *pkgmgr.Service the process rpcs use.
type Processes interface {
	RunCommand(ctx context.Context, req pkgmgr.RunRequest) (*pkgmgr.RunResponse, error)
	GetProcessesByWorktree(worktreeID string) []*shell.BackgroundProcess
	GetAllProcesses() []*shell.BackgroundProcess
	GetProcess(processID string) (*shell.BackgroundProcess, error)
	GetProcessOutput(processID string) (stdout, stderr string, err error)
	GetCombinedOutput(processID string) ([]shell.OutputLine, error)
	KillProcess(processID string) error
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("package_commands: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("package_commands: Deps.Config is required")
	}
	if d.Router == nil {
		return fmt.Errorf("package_commands: Deps.Router is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("package_commands: Deps.Repo is required")
	}
	if d.Processes == nil {
		return fmt.Errorf("package_commands: Deps.Processes is required")
	}
	return nil
}

// Service implements the package_commands Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedPackageCommandsServiceHandler
	deps Deps
}

// New creates a new package_commands service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "package_commands"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewPackageCommandsServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_package_commands_proto
