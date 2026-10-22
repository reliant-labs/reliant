package tools_daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
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
// (the gateway root of internal/app's composition graph) constructs it and
// serves it on its own daemon listener (internal/grpc/daemon_listener.go), not
// on the API server.
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
	// ResultPublisher delivers a tool result that arrives with no waiter in
	// this process — its request was forwarded by another replica, or over
	// an earlier stream that has since dropped — to the worker waiting on
	// the request's result subject. Without it such a result is dropped and
	// left unacknowledged, so the daemon keeps it for the next stream.
	// forge:optional-dep
	ResultPublisher ToolResultPublisher
}

// ToolResultPublisher publishes a tool result to the worker waiting for it.
// *toolexec.ToolResultPublisher implements it. It returns only once the
// result is safely on its way: the daemon forgets a result as soon as it is
// acknowledged, and the acknowledgement follows this call.
type ToolResultPublisher interface {
	PublishToolResult(ctx context.Context, userID, daemonID string, resp *toolexec.ToolExecutionResponse) error
}

// DaemonDrainListener is told, per held daemon stream, that this replica has
// started draining, before the daemon is told to move. Optional on a
// connection listener; *toolexec.NATSToolBridge implements it, to stop routing
// the daemon's new requests here.
type DaemonDrainListener interface {
	OnDaemonDraining(userID, daemonID string)
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

	// draining is set once the first hand-over has run (handOver): this
	// replica is shutting down and refuses new daemon streams, so a daemon moving off it (or one
	// the load balancer has not yet stopped sending) goes to a live replica.
	draining atomic.Bool
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

// Register registers the service handlers with the HTTP mux. ConnectDaemon
// stops its stream's reader before it returns by aborting the request body
// (serverkit.AbortReceive), which the serverkit listener the mux is served by
// makes possible; it refuses a stream served any other way.
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
