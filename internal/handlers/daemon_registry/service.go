package daemon_registry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the daemon_registry service. The composition
// graph constructs it (internal/app/compose.go). The rpc methods on *Service
// are where this service's logic lives; see the forge `api` / `services`
// skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads the caller's daemons, their attachment leases and the
	// telemetry the gateway keeps on the daemon row.
	Repo Repository
	// Router reaches the caller's attached daemons. ListDaemons nudges it
	// to register a local daemon before reading; ClearMachineCaches and the
	// local-model rpcs relay through it. Without it (cloud mode, where the
	// daemon is external) the relays report Unavailable.
	// forge:optional-dep
	Router Router
}

// Repository is the slice of db.Repository the daemon_registry handlers use.
type Repository interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	ListFreshDaemonAttachmentsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]*db.DaemonAttachment, error)
	ListDaemonLocalModels(ctx context.Context, userID string) (map[string]string, error)
	ListDaemonStorageState(ctx context.Context, userID string) (map[string]string, error)
}

// Router is the slice of toolexec.DaemonRouter the daemon_registry handlers
// use. toolexec.DaemonRouter implements it.
type Router interface {
	IsDaemonOnline(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (bool, error)
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	RefreshLocalModels(ctx context.Context, userID, daemonID string) (*pb.LocalModelInventory, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("daemon_registry: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("daemon_registry: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("daemon_registry: Deps.Repo is required")
	}
	return nil
}

// Service implements the daemon_registry Connect RPC service: daemon
// registry queries (list/get/resolve/resume) and the relays to an attached
// machine. Daemon credentials are `rlat_` access tokens managed through
// TokenService.
type Service struct {
	reliantv1connect.UnimplementedDaemonRegistryServiceHandler
	deps Deps
}

// New creates a new daemon_registry service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "daemon_registry"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewDaemonRegistryServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_daemon_registry_proto
