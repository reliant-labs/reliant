package project

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the project service. internal/grpc/server.go
// constructs it today; forge's composition root takes over in P3c
// (research/forge-migration/PLAN.md). The rpc methods on *Service are where
// this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo stores projects and what hangs off them: their daemons, repos,
	// main worktree, seeded config row and prompts.
	Repo Repository
	// Router runs every filesystem and git operation on the caller's
	// daemon, which owns the filesystem. The API server always has one; in
	// a composition without one, the daemon-backed steps fail through their
	// existing error paths and the database-only rpcs still work.
	// forge:optional-dep
	Router Router
	// ControlPlane clones a repository onto a managed daemon for
	// CreateProjectFromRepo. Nil in self-hosted deployments, where that rpc
	// reports that cloning needs a cloud account: the user's GitHub token
	// lives in the control plane and nowhere else, and defaulting to
	// localhost would fail obscurely against a control plane that is not
	// there.
	// forge:optional-dep
	ControlPlane CloneDispatcher
}

// Repository is the slice of db.Repository the project handlers use.
type Repository interface {
	CreateProject(ctx context.Context, project *db.Project) error
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetProjectByPathAndUser(ctx context.Context, path, userID string) (*db.Project, error)
	GetProjectByRemoteURLAndUser(ctx context.Context, remoteURL, userID string) (*db.Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)
	UpdateProject(ctx context.Context, project *db.Project, userID string) error
	SetProjectForgeName(ctx context.Context, id, userID, forgeProjectName string) (bool, error)
	TouchProject(ctx context.Context, id string, userID string) error
	DeleteProject(ctx context.Context, id string, userID string) error

	UpsertProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string) error
	UpsertQueuedProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string, requestID string) error
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error)
	ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemon, error)
	DeleteProjectDaemon(ctx context.Context, projectID, daemonID string) error

	CreateRepo(ctx context.Context, repo *core.Repo) error
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)

	CreateWorktree(ctx context.Context, worktree *db.Worktree) error
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	UpdateWorktree(ctx context.Context, worktree *db.Worktree) error

	UpsertProjectConfigRecord(ctx context.Context, record *db.ProjectConfigRecord) error

	CreateSetting(ctx context.Context, setting *db.Setting) error
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*db.Setting, error)
	UpdateSetting(ctx context.Context, setting *db.Setting) error
}

// Router sends commands to a user's daemon. toolexec.DaemonRouter
// implements it.
type Router interface {
	// SendDaemonCommand runs one request/response command on the daemon the
	// user's commands route to.
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// SendDaemonCommandToDaemon runs one on a specific daemon (reading a
	// fresh clone's forge name).
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// ResolveDaemonID names the daemon SendDaemonCommand would route to.
	ResolveDaemonID(ctx context.Context, userID string) (string, error)
	// EnqueueDaemonCommand queues a command for the user's daemons'
	// next connect.
	EnqueueDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) (int, error)
	SendLoadProjectConfigs(ctx context.Context, userID, projectPath, requestID string) error
	SendWatchProjectConfigs(ctx context.Context, userID, projectPath string, includeInitial bool) error
}

// CloneDispatcher asks the control plane to clone a repository onto one of
// the caller's daemons. controlplane.Client implements it.
type CloneDispatcher interface {
	CloneRepoOntoDaemon(ctx context.Context, jwt string, req controlplane.CloneRepoRequest) (controlplane.CloneRepoResult, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("project: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("project: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("project: Deps.Repo is required")
	}
	return nil
}

// Service implements the project Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedProjectServiceHandler
	deps Deps
}

// New creates a new project service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "project"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewProjectServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_project_proto
