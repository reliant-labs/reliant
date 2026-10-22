package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the mcp service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads the caller's projects and keeps the stored copy of their
	// MCP configuration (the project config record) in step with what the
	// daemon writes to disk.
	Repo Repository
	// Router runs the user's MCP servers and reads and writes their config
	// files on the user's daemon, which owns the filesystem. The API server
	// always has one; in a composition without one, listing answers from the
	// stored configuration with no live status, and the rpcs that need the
	// daemon fail through their existing error paths.
	// forge:optional-dep
	Router Router
}

// Repository is the slice of db.Repository the mcp handlers use.
type Repository interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)

	GetProjectConfigRecord(ctx context.Context, projectID string) (*db.ProjectConfigRecord, error)
	GetProjectMCPConfigsJSON(ctx context.Context, projectID string) (*string, error)
	UpsertProjectConfigRecord(ctx context.Context, record *db.ProjectConfigRecord) error

	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error
}

// Router sends one request/response command to a user's daemon.
// toolexec.DaemonRouter implements it.
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("mcp: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("mcp: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("mcp: Deps.Repo is required")
	}
	return nil
}

// Service implements the mcp Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedMCPServiceHandler
	deps Deps
	// statuses holds the servers' last-known status per user and project
	// (see mcpStatusCache), so reading the MCP page does not wait on the
	// user's machine.
	statuses *mcpStatusCache
}

// New creates a new mcp service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	s := &Service{deps: deps}
	s.statuses = newMCPStatusCache(func(ctx context.Context, userID, projectPath string) (*daemonMCPServerStatus, error) {
		return newDaemonMCPProxy(s.deps.Router, userID).serverStatus(ctx, projectPath)
	})
	return s, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "mcp"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewMCPServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_mcp_proto
