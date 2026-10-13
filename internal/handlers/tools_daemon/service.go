package tools_daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the tools_daemon service. The daemon-gateway
// (internal/servergateway) constructs it today and hosts it on its own
// server (internal/grpc/daemon_server.go), not on the API server; forge's
// composition root takes over in P3c (research/forge-migration/PLAN.md).
// The rpc methods on *Service are where this service's logic lives; see the
// forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo holds what the gateway learns from its daemons: the daemon rows,
	// their attachment leases, the projects and project configs they
	// report. It calls far more than ~15 db.Repository methods, so it takes
	// the whole interface until P6 splits it.
	Repo db.Repository
	// UserUpdateHub forwards daemon heartbeats, list changes and local-model
	// inventories to the owner's web clients as ephemeral user updates.
	// Without it they are not published.
	// forge:optional-dep
	UserUpdateHub UserUpdatePublisher
	// StatePublisher emits daemon.v1.state.* lifecycle events. Without it
	// they are not emitted.
	// forge:optional-dep
	StatePublisher LifecyclePublisher
}

// UserUpdatePublisher publishes one ephemeral user update.
// streaming.UpdateHub[db.UserUpdate] implements it.
type UserUpdatePublisher interface {
	Publish(ctx context.Context, event streaming.UpdateEvent[db.UserUpdate])
}

// LifecyclePublisher emits a daemon's daemon.v1.state.* lifecycle events.
// *daemonstate.Publisher implements it.
type LifecyclePublisher interface {
	Connected(daemonID, userID, daemonType string)
	Disconnected(daemonID, userID, daemonType string)
	Activity(daemonID, userID, daemonType, reason string)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("tools_daemon: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("tools_daemon: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("tools_daemon: Deps.Repo is required")
	}
	return nil
}

// Service implements the tools_daemon Connect RPC service: bidirectional
// streaming for tool execution. It manages daemon connections and routes
// tool requests/responses.
type Service struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	deps Deps

	// Active daemon connections, keyed by daemonID.
	connections map[string]*daemonConnection
	// Secondary index: userID → list of connected daemonIDs.
	userDaemons map[string][]string
	mu          sync.RWMutex

	// listeners are notified (outside the mutex) when daemons connect/disconnect.
	listeners   []toolexec.DaemonConnectionListener
	listenersMu sync.RWMutex

	// now returns the current time. Overridable for tests; defaults to
	// time.Now (UTC). Used by the stale-connection sweeper so tests can
	// jump the clock without sleeping.
	now func() time.Time
}

// New creates a new tools_daemon service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
// Daemon connection routability is derived from the daemon_attachment
// table, so no background monitor is required; Start runs the
// stale-connection sweeper.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return newService(deps), nil
}

// newService builds the service's connection state around deps. Tests of
// the in-memory routing alone build it with no Repo; the lease and
// list-changed writes skip a nil one.
func newService(deps Deps) *Service {
	return &Service{
		deps:        deps,
		connections: make(map[string]*daemonConnection),
		userDaemons: make(map[string][]string),
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "tools_daemon"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewToolsDaemonServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_tools_daemon_proto
